package authority

import (
	"fmt"
	"sort"
)

type CatalogObject struct {
	Schema                     string
	Name                       string
	Kind                       ObjectKind
	Owner                      string
	RuntimePrivileges          []string
	WriterPrivileges           []string
	PublicPrivileges           []string
	EffectiveRuntimePrivileges []string
	EffectiveWriterPrivileges  []string
}

type CatalogSnapshot struct {
	SchemaVersion            int
	MigrationDigest          string
	RuntimeRole              string
	RuntimeInherits          bool
	WriterMembership         bool
	DatabaseOwner            string
	SchemaOwner              string
	PublicConnect            bool
	RuntimeConnect           bool
	WriterConnect            bool
	PublicSchemaPrivileges   []string
	RuntimeSchemaPrivileges  []string
	WriterSchemaPrivileges   []string
	DefaultPublicPrivileges  []string
	DefaultRuntimePrivileges []string
	DefaultWriterPrivileges  []string
	Objects                  []CatalogObject
}

type RuntimeAuthorityResult struct {
	SchemaVersion               int
	MigrationDigest             string
	MatrixDigest                string
	RuntimeRole                 string
	RuntimeInherits             bool
	WriterMembership            bool
	FullCapabilityGrants        bool
	EffectiveTablePrivileges    bool
	EffectiveSequencePrivileges bool
	EffectiveRoutinePrivileges  bool
	OwnershipViolations         []string
	PrivilegeViolations         []string
}

func VerifyRuntimeCatalog(matrix Matrix, snapshot CatalogSnapshot) (RuntimeAuthorityResult, error) {
	result := RuntimeAuthorityResult{
		SchemaVersion: snapshot.SchemaVersion, MigrationDigest: snapshot.MigrationDigest, MatrixDigest: matrix.Digest(),
		RuntimeRole: snapshot.RuntimeRole, RuntimeInherits: snapshot.RuntimeInherits, WriterMembership: snapshot.WriterMembership,
		FullCapabilityGrants: true, EffectiveTablePrivileges: true, EffectiveSequencePrivileges: true, EffectiveRoutinePrivileges: true,
	}
	if err := matrix.Validate(); err != nil {
		return result, err
	}
	if snapshot.SchemaVersion != matrix.SchemaVersion {
		return result, fmt.Errorf("SCHEMA_VERSION_MISMATCH")
	}
	if snapshot.MigrationDigest != matrix.MigrationDigest {
		return result, fmt.Errorf("MIGRATION_DIGEST_MISMATCH")
	}
	if snapshot.RuntimeRole != matrix.Roles.Runtime || !snapshot.RuntimeInherits {
		return result, fmt.Errorf("RUNTIME_IDENTITY_MISMATCH")
	}
	if snapshot.DatabaseOwner != matrix.Roles.Migrator {
		result.OwnershipViolations = append(result.OwnershipViolations, "database")
	}
	if snapshot.SchemaOwner != matrix.Roles.Migrator {
		result.OwnershipViolations = append(result.OwnershipViolations, "schema:public")
	}
	if len(result.OwnershipViolations) != 0 {
		return result, fmt.Errorf("AUTHORITY_OWNER_DRIFT")
	}
	if snapshot.PublicConnect || len(snapshot.PublicSchemaPrivileges) != 0 {
		result.PrivilegeViolations = append(result.PrivilegeViolations, "PUBLIC")
		return result, fmt.Errorf("PUBLIC_AUTHORITY_DRIFT")
	}
	if len(snapshot.DefaultPublicPrivileges) != 0 || len(snapshot.DefaultRuntimePrivileges) != 0 || len(snapshot.DefaultWriterPrivileges) != 0 {
		result.PrivilegeViolations = append(result.PrivilegeViolations, "default-privileges")
		return result, fmt.Errorf("DEFAULT_AUTHORITY_DRIFT")
	}
	if !snapshot.RuntimeConnect || snapshot.WriterConnect || !samePrivileges(snapshot.RuntimeSchemaPrivileges, []string{"USAGE"}) || !samePrivileges(snapshot.WriterSchemaPrivileges, []string{"USAGE"}) {
		result.PrivilegeViolations = append(result.PrivilegeViolations, "database-or-schema")
		return result, fmt.Errorf("PRIVILEGE_AUTHORITY_DRIFT")
	}

	expected := make(map[string]Object, len(matrix.Objects))
	for _, object := range matrix.Objects {
		expected[catalogObjectKey(object.Schema, object.Name, object.Kind)] = object
	}
	actual := make(map[string]CatalogObject, len(snapshot.Objects))
	for _, object := range snapshot.Objects {
		key := catalogObjectKey(object.Schema, object.Name, object.Kind)
		if _, duplicate := actual[key]; duplicate {
			return result, fmt.Errorf("AUTHORITY_MATRIX_DRIFT")
		}
		actual[key] = object
	}
	if len(actual) != len(expected) {
		return result, fmt.Errorf("AUTHORITY_MATRIX_DRIFT")
	}
	for key := range expected {
		if _, ok := actual[key]; !ok {
			return result, fmt.Errorf("AUTHORITY_MATRIX_DRIFT")
		}
	}
	for key := range actual {
		if _, ok := expected[key]; !ok {
			return result, fmt.Errorf("AUTHORITY_MATRIX_DRIFT")
		}
	}

	keys := make([]string, 0, len(expected))
	for key := range expected {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		want, got := expected[key], actual[key]
		if got.Owner != matrix.Roles.Migrator {
			result.OwnershipViolations = append(result.OwnershipViolations, key)
			if got.Owner == matrix.Roles.Runtime {
				return result, fmt.Errorf("RUNTIME_OWNS_CANONICAL_OBJECT")
			}
			return result, fmt.Errorf("AUTHORITY_OWNER_DRIFT")
		}
		if len(got.PublicPrivileges) != 0 {
			result.PrivilegeViolations = append(result.PrivilegeViolations, key+":PUBLIC")
			return result, fmt.Errorf("PUBLIC_AUTHORITY_DRIFT")
		}
		effectiveRuntime := append([]string(nil), want.Runtime...)
		if snapshot.WriterMembership {
			effectiveRuntime = append(effectiveRuntime, want.Writer...)
		}
		if !samePrivileges(got.RuntimePrivileges, want.Runtime) || !samePrivileges(got.WriterPrivileges, want.Writer) {
			result.FullCapabilityGrants = false
			result.PrivilegeViolations = append(result.PrivilegeViolations, key+":direct")
			return result, fmt.Errorf("PRIVILEGE_AUTHORITY_DRIFT")
		}
		if !samePrivileges(got.EffectiveRuntimePrivileges, effectiveRuntime) || !samePrivileges(got.EffectiveWriterPrivileges, want.Writer) {
			setEffectivePrivilegeResult(&result, want.Kind, false)
			result.PrivilegeViolations = append(result.PrivilegeViolations, key+":effective")
			return result, fmt.Errorf("PRIVILEGE_AUTHORITY_DRIFT")
		}
	}
	return result, nil
}

func catalogObjectKey(schema, name string, kind ObjectKind) string {
	return string(kind) + ":" + schema + "." + name
}

func samePrivileges(left, right []string) bool {
	left = sortedUnique(left)
	right = sortedUnique(right)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		seen[value] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func setEffectivePrivilegeResult(result *RuntimeAuthorityResult, kind ObjectKind, value bool) {
	switch kind {
	case Table:
		result.EffectiveTablePrivileges = value
	case Sequence:
		result.EffectiveSequencePrivileges = value
	case Routine:
		result.EffectiveRoutinePrivileges = value
	}
}

func PrivilegeUniverse(kind ObjectKind) []string {
	switch kind {
	case Table:
		return []string{"DELETE", "INSERT", "REFERENCES", "SELECT", "TRIGGER", "TRUNCATE", "UPDATE"}
	case Sequence:
		return []string{"SELECT", "UPDATE", "USAGE"}
	case Routine:
		return []string{"EXECUTE"}
	default:
		return nil
	}
}
