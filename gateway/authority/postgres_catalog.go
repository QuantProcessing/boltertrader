package authority

import (
	"context"
	"fmt"
)

type CatalogRow interface {
	Scan(...any) error
}

type CatalogRows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}

type CatalogQuerier interface {
	QueryRow(context.Context, string, ...any) CatalogRow
	Query(context.Context, string, ...any) (CatalogRows, error)
}

func CollectPostgresRuntimeCatalog(ctx context.Context, queryer CatalogQuerier, matrix Matrix, schemaVersionQuery, migrationDigest string) (CatalogSnapshot, error) {
	snapshot := CatalogSnapshot{MigrationDigest: migrationDigest}
	if err := queryer.QueryRow(ctx, schemaVersionQuery).Scan(&snapshot.SchemaVersion); err != nil {
		return snapshot, catalogQueryError("schema-version")
	}
	if err := queryer.QueryRow(ctx, `
		SELECT current_user, runtime.rolinherit,
		       pg_has_role(current_user, $1, 'MEMBER'),
		       pg_get_userbyid(database.datdba), pg_get_userbyid(namespace.nspowner),
		       EXISTS (
		         SELECT 1 FROM aclexplode(COALESCE(database.datacl, acldefault('d', database.datdba))) acl
		         WHERE acl.grantee=0 AND acl.privilege_type='CONNECT'
		       ),
		       has_database_privilege(current_user, current_database(), 'CONNECT'),
		       has_database_privilege($1, current_database(), 'CONNECT')
		FROM pg_database database
		JOIN pg_namespace namespace ON namespace.nspname='public'
		JOIN pg_roles runtime ON runtime.rolname=current_user
		WHERE database.datname=current_database()`, matrix.Roles.Writer).Scan(
		&snapshot.RuntimeRole, &snapshot.RuntimeInherits, &snapshot.WriterMembership,
		&snapshot.DatabaseOwner, &snapshot.SchemaOwner, &snapshot.PublicConnect,
		&snapshot.RuntimeConnect, &snapshot.WriterConnect,
	); err != nil {
		return snapshot, catalogQueryError("identity")
	}
	publicSchemaRows, err := queryer.Query(ctx, `
		SELECT acl.privilege_type
		FROM pg_namespace namespace
		CROSS JOIN LATERAL aclexplode(COALESCE(namespace.nspacl, acldefault('n', namespace.nspowner))) acl
		WHERE namespace.nspname='public' AND acl.grantee=0
		ORDER BY acl.privilege_type`)
	if err != nil {
		return snapshot, catalogQueryError("public-schema")
	}
	for publicSchemaRows.Next() {
		var privilege string
		if publicSchemaRows.Scan(&privilege) != nil {
			publicSchemaRows.Close()
			return snapshot, catalogQueryError("public-schema")
		}
		snapshot.PublicSchemaPrivileges = append(snapshot.PublicSchemaPrivileges, privilege)
	}
	if publicSchemaRows.Err() != nil {
		publicSchemaRows.Close()
		return snapshot, catalogQueryError("public-schema")
	}
	publicSchemaRows.Close()

	for _, principal := range []struct {
		role       string
		privileges *[]string
	}{{matrix.Roles.Runtime, &snapshot.RuntimeSchemaPrivileges}, {matrix.Roles.Writer, &snapshot.WriterSchemaPrivileges}} {
		for _, privilege := range []string{"CREATE", "USAGE"} {
			var granted bool
			if err := queryer.QueryRow(ctx, `SELECT has_schema_privilege($1, 'public', $2)`, principal.role, privilege).Scan(&granted); err != nil {
				return snapshot, catalogQueryError("schema-privilege")
			}
			if granted {
				*principal.privileges = append(*principal.privileges, privilege)
			}
		}
	}

	defaultRows, err := queryer.Query(ctx, `
		SELECT COALESCE(role.rolname, 'PUBLIC'),
		       CASE defaults.defaclobjtype WHEN 'r' THEN 'TABLE' WHEN 'S' THEN 'SEQUENCE' WHEN 'f' THEN 'FUNCTION' ELSE defaults.defaclobjtype::text END || ':' || acl.privilege_type
		FROM pg_default_acl defaults
		CROSS JOIN LATERAL aclexplode(defaults.defaclacl) acl
		LEFT JOIN pg_roles role ON role.oid=acl.grantee
		WHERE (defaults.defaclnamespace=0 OR defaults.defaclnamespace=(SELECT oid FROM pg_namespace WHERE nspname='public'))
		  AND (acl.grantee=0 OR role.rolname IN ($1,$2))
		ORDER BY 1,2`, matrix.Roles.Runtime, matrix.Roles.Writer)
	if err != nil {
		return snapshot, catalogQueryError("default-privilege")
	}
	for defaultRows.Next() {
		var principal, privilege string
		if defaultRows.Scan(&principal, &privilege) != nil {
			defaultRows.Close()
			return snapshot, catalogQueryError("default-privilege")
		}
		switch principal {
		case "PUBLIC":
			snapshot.DefaultPublicPrivileges = append(snapshot.DefaultPublicPrivileges, privilege)
		case matrix.Roles.Runtime:
			snapshot.DefaultRuntimePrivileges = append(snapshot.DefaultRuntimePrivileges, privilege)
		case matrix.Roles.Writer:
			snapshot.DefaultWriterPrivileges = append(snapshot.DefaultWriterPrivileges, privilege)
		}
	}
	if defaultRows.Err() != nil {
		defaultRows.Close()
		return snapshot, catalogQueryError("default-privilege")
	}
	defaultRows.Close()

	objectRows, err := queryer.Query(ctx, `
		SELECT kind, schema_name, object_name, owner_name FROM (
		  SELECT CASE WHEN class.relkind='S' THEN 'sequence' ELSE 'table' END AS kind,
		         namespace.nspname AS schema_name, class.relname AS object_name, pg_get_userbyid(class.relowner) AS owner_name
		  FROM pg_class class JOIN pg_namespace namespace ON namespace.oid=class.relnamespace
		  WHERE namespace.nspname='public' AND class.relkind IN ('r','p','v','m','f','S')
		  UNION ALL
		  SELECT 'routine', namespace.nspname,
		         procedure.proname || '(' || oidvectortypes(procedure.proargtypes) || ')',
		         pg_get_userbyid(procedure.proowner)
		  FROM pg_proc procedure JOIN pg_namespace namespace ON namespace.oid=procedure.pronamespace
		  WHERE namespace.nspname='public' AND procedure.prokind IN ('f','p')
		) inventory ORDER BY kind, schema_name, object_name`)
	if err != nil {
		return snapshot, catalogQueryError("object-inventory")
	}
	objects := make(map[string]int)
	for objectRows.Next() {
		var kind string
		var object CatalogObject
		if objectRows.Scan(&kind, &object.Schema, &object.Name, &object.Owner) != nil {
			objectRows.Close()
			return snapshot, catalogQueryError("object-inventory")
		}
		object.Kind = ObjectKind(kind)
		snapshot.Objects = append(snapshot.Objects, object)
		objects[catalogObjectKey(object.Schema, object.Name, object.Kind)] = len(snapshot.Objects) - 1
	}
	if objectRows.Err() != nil {
		objectRows.Close()
		return snapshot, catalogQueryError("object-inventory")
	}
	objectRows.Close()

	aclRows, err := queryer.Query(ctx, `
		WITH object_acl AS (
		  SELECT CASE WHEN class.relkind='S' THEN 'sequence' ELSE 'table' END AS kind,
		         namespace.nspname AS schema_name, class.relname AS object_name, class.relowner AS owner_oid,
		         class.relacl AS object_acl, CASE WHEN class.relkind='S' THEN 'S'::"char" ELSE 'r'::"char" END AS acl_kind
		  FROM pg_class class JOIN pg_namespace namespace ON namespace.oid=class.relnamespace
		  WHERE namespace.nspname='public' AND class.relkind IN ('r','p','v','m','f','S')
		  UNION ALL
		  SELECT 'routine', namespace.nspname,
		         procedure.proname || '(' || oidvectortypes(procedure.proargtypes) || ')',
		         procedure.proowner, procedure.proacl, 'f'::"char"
		  FROM pg_proc procedure JOIN pg_namespace namespace ON namespace.oid=procedure.pronamespace
		  WHERE namespace.nspname='public' AND procedure.prokind IN ('f','p')
		)
		SELECT object_acl.kind, object_acl.schema_name, object_acl.object_name,
		       COALESCE(role.rolname, 'PUBLIC'), acl.privilege_type
		FROM object_acl
		CROSS JOIN LATERAL aclexplode(COALESCE(object_acl.object_acl, acldefault(object_acl.acl_kind, object_acl.owner_oid))) acl
		LEFT JOIN pg_roles role ON role.oid=acl.grantee
		WHERE acl.grantee=0 OR role.rolname IN ($1,$2)
		ORDER BY 1,2,3,4,5`, matrix.Roles.Runtime, matrix.Roles.Writer)
	if err != nil {
		return snapshot, catalogQueryError("object-acl")
	}
	for aclRows.Next() {
		var kind, schema, name, principal, privilege string
		if aclRows.Scan(&kind, &schema, &name, &principal, &privilege) != nil {
			aclRows.Close()
			return snapshot, catalogQueryError("object-acl")
		}
		objectIndex, ok := objects[catalogObjectKey(schema, name, ObjectKind(kind))]
		if !ok {
			aclRows.Close()
			return snapshot, catalogQueryError("object-acl")
		}
		object := &snapshot.Objects[objectIndex]
		switch principal {
		case "PUBLIC":
			object.PublicPrivileges = append(object.PublicPrivileges, privilege)
		case matrix.Roles.Runtime:
			object.RuntimePrivileges = append(object.RuntimePrivileges, privilege)
		case matrix.Roles.Writer:
			object.WriterPrivileges = append(object.WriterPrivileges, privilege)
		}
	}
	if aclRows.Err() != nil {
		aclRows.Close()
		return snapshot, catalogQueryError("object-acl")
	}
	aclRows.Close()

	for _, expected := range matrix.Objects {
		objectIndex, ok := objects[catalogObjectKey(expected.Schema, expected.Name, expected.Kind)]
		if !ok {
			continue
		}
		object := &snapshot.Objects[objectIndex]
		identity := expected.Schema + "." + expected.Name
		for _, privilege := range PrivilegeUniverse(expected.Kind) {
			var runtimeGranted, writerGranted bool
			query := map[ObjectKind]string{
				Table:    `SELECT has_table_privilege(current_user, $1, $2), has_table_privilege($3, $1, $2)`,
				Sequence: `SELECT has_sequence_privilege(current_user, $1, $2), has_sequence_privilege($3, $1, $2)`,
				Routine:  `SELECT has_function_privilege(current_user, $1, $2), has_function_privilege($3, $1, $2)`,
			}[expected.Kind]
			if err := queryer.QueryRow(ctx, query, identity, privilege, matrix.Roles.Writer).Scan(&runtimeGranted, &writerGranted); err != nil {
				return snapshot, catalogQueryError("effective-privilege")
			}
			if runtimeGranted {
				object.EffectiveRuntimePrivileges = append(object.EffectiveRuntimePrivileges, privilege)
			}
			if writerGranted {
				object.EffectiveWriterPrivileges = append(object.EffectiveWriterPrivileges, privilege)
			}
		}
	}
	return snapshot, nil
}

func catalogQueryError(stage string) error {
	return fmt.Errorf("AUTHORITY_CATALOG_QUERY_FAILED: %s", stage)
}
