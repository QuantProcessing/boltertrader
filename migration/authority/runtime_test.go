package authority_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/migration/authority"
)

func TestVerifyRuntimeCatalogAcceptsPreAndPostActivationAuthority(t *testing.T) {
	for _, writerMembership := range []bool{false, true} {
		t.Run(map[bool]string{false: "pre-activation", true: "post-activation"}[writerMembership], func(t *testing.T) {
			matrix := authority.PlatformV15()
			snapshot := validRuntimeCatalog(matrix, writerMembership)
			result, err := authority.VerifyRuntimeCatalog(matrix, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if result.SchemaVersion != matrix.SchemaVersion || result.MigrationDigest != matrix.MigrationDigest || result.MatrixDigest != matrix.Digest() ||
				result.WriterMembership != writerMembership || !result.FullCapabilityGrants || !result.EffectiveTablePrivileges ||
				!result.EffectiveSequencePrivileges || !result.EffectiveRoutinePrivileges || len(result.OwnershipViolations) != 0 || len(result.PrivilegeViolations) != 0 {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestVerifyRuntimeCatalogRejectsEveryStructuralDriftClass(t *testing.T) {
	matrix := authority.PlatformV15()
	base := validRuntimeCatalog(matrix, false)
	tests := []struct {
		name string
		want string
		edit func(*authority.CatalogSnapshot)
	}{
		{name: "schema version", want: "SCHEMA_VERSION_MISMATCH", edit: func(snapshot *authority.CatalogSnapshot) { snapshot.SchemaVersion-- }},
		{name: "migration digest", want: "MIGRATION_DIGEST_MISMATCH", edit: func(snapshot *authority.CatalogSnapshot) { snapshot.MigrationDigest = strings.Repeat("a", 64) }},
		{name: "runtime identity", want: "RUNTIME_IDENTITY_MISMATCH", edit: func(snapshot *authority.CatalogSnapshot) { snapshot.RuntimeRole = "other_test" }},
		{name: "runtime noinherit", want: "RUNTIME_IDENTITY_MISMATCH", edit: func(snapshot *authority.CatalogSnapshot) { snapshot.RuntimeInherits = false }},
		{name: "database owner", want: "AUTHORITY_OWNER_DRIFT", edit: func(snapshot *authority.CatalogSnapshot) { snapshot.DatabaseOwner = matrix.Roles.Runtime }},
		{name: "schema owner", want: "AUTHORITY_OWNER_DRIFT", edit: func(snapshot *authority.CatalogSnapshot) { snapshot.SchemaOwner = matrix.Roles.Runtime }},
		{name: "public connect", want: "PUBLIC_AUTHORITY_DRIFT", edit: func(snapshot *authority.CatalogSnapshot) { snapshot.PublicConnect = true }},
		{name: "runtime connect missing", want: "PRIVILEGE_AUTHORITY_DRIFT", edit: func(snapshot *authority.CatalogSnapshot) { snapshot.RuntimeConnect = false }},
		{name: "writer connect", want: "PRIVILEGE_AUTHORITY_DRIFT", edit: func(snapshot *authority.CatalogSnapshot) { snapshot.WriterConnect = true }},
		{name: "public schema", want: "PUBLIC_AUTHORITY_DRIFT", edit: func(snapshot *authority.CatalogSnapshot) { snapshot.PublicSchemaPrivileges = []string{"USAGE"} }},
		{name: "runtime schema", want: "PRIVILEGE_AUTHORITY_DRIFT", edit: func(snapshot *authority.CatalogSnapshot) {
			snapshot.RuntimeSchemaPrivileges = []string{"CREATE", "USAGE"}
		}},
		{name: "default public", want: "DEFAULT_AUTHORITY_DRIFT", edit: func(snapshot *authority.CatalogSnapshot) {
			snapshot.DefaultPublicPrivileges = []string{"FUNCTION:EXECUTE"}
		}},
		{name: "default runtime", want: "DEFAULT_AUTHORITY_DRIFT", edit: func(snapshot *authority.CatalogSnapshot) {
			snapshot.DefaultRuntimePrivileges = []string{"TABLE:SELECT"}
		}},
		{name: "missing object", want: "AUTHORITY_MATRIX_DRIFT", edit: func(snapshot *authority.CatalogSnapshot) { snapshot.Objects = snapshot.Objects[1:] }},
		{name: "unknown object", want: "AUTHORITY_MATRIX_DRIFT", edit: func(snapshot *authority.CatalogSnapshot) {
			snapshot.Objects = append(snapshot.Objects, authority.CatalogObject{Schema: "public", Name: "platform_unknown", Kind: authority.Table, Owner: matrix.Roles.Migrator})
		}},
		{name: "object owner", want: "RUNTIME_OWNS_CANONICAL_OBJECT", edit: func(snapshot *authority.CatalogSnapshot) { snapshot.Objects[0].Owner = matrix.Roles.Runtime }},
		{name: "public object privilege", want: "PUBLIC_AUTHORITY_DRIFT", edit: func(snapshot *authority.CatalogSnapshot) { snapshot.Objects[0].PublicPrivileges = []string{"SELECT"} }},
		{name: "runtime direct privilege", want: "PRIVILEGE_AUTHORITY_DRIFT", edit: func(snapshot *authority.CatalogSnapshot) {
			snapshot.Objects[0].RuntimePrivileges = append(snapshot.Objects[0].RuntimePrivileges, "INSERT")
		}},
		{name: "writer direct privilege", want: "PRIVILEGE_AUTHORITY_DRIFT", edit: func(snapshot *authority.CatalogSnapshot) { snapshot.Objects[2].WriterPrivileges = nil }},
		{name: "runtime effective privilege", want: "PRIVILEGE_AUTHORITY_DRIFT", edit: func(snapshot *authority.CatalogSnapshot) { snapshot.Objects[0].EffectiveRuntimePrivileges = nil }},
		{name: "writer effective privilege", want: "PRIVILEGE_AUTHORITY_DRIFT", edit: func(snapshot *authority.CatalogSnapshot) { snapshot.Objects[2].EffectiveWriterPrivileges = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := cloneCatalog(t, base)
			test.edit(&snapshot)
			if _, err := authority.VerifyRuntimeCatalog(matrix, snapshot); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want=%s", err, test.want)
			}
		})
	}
}

func validRuntimeCatalog(matrix authority.Matrix, writerMembership bool) authority.CatalogSnapshot {
	snapshot := authority.CatalogSnapshot{
		SchemaVersion: matrix.SchemaVersion, MigrationDigest: matrix.MigrationDigest,
		RuntimeRole: matrix.Roles.Runtime, RuntimeInherits: true, WriterMembership: writerMembership,
		DatabaseOwner: matrix.Roles.Migrator, SchemaOwner: matrix.Roles.Migrator,
		RuntimeConnect:          true,
		RuntimeSchemaPrivileges: []string{"USAGE"}, WriterSchemaPrivileges: []string{"USAGE"},
	}
	for _, object := range matrix.Objects {
		effectiveRuntime := append([]string(nil), object.Runtime...)
		if writerMembership {
			effectiveRuntime = append(effectiveRuntime, object.Writer...)
		}
		snapshot.Objects = append(snapshot.Objects, authority.CatalogObject{
			Schema: object.Schema, Name: object.Name, Kind: object.Kind, Owner: matrix.Roles.Migrator,
			RuntimePrivileges: append([]string(nil), object.Runtime...), WriterPrivileges: append([]string(nil), object.Writer...),
			EffectiveRuntimePrivileges: effectiveRuntime, EffectiveWriterPrivileges: append([]string(nil), object.Writer...),
		})
	}
	return snapshot
}

func cloneCatalog(t *testing.T, source authority.CatalogSnapshot) authority.CatalogSnapshot {
	t.Helper()
	data, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var result authority.CatalogSnapshot
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
