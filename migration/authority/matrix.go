package authority

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type ObjectClass string

const (
	Canonical             ObjectClass = "canonical"
	ValidationAuthSession ObjectClass = "validation-auth-session"
	MigrationOnly         ObjectClass = "migration-only"
)

type ObjectKind string

const (
	Table    ObjectKind = "table"
	Sequence ObjectKind = "sequence"
	Routine  ObjectKind = "routine"
)

type RoleTopology struct {
	Maintenance string
	Migrator    string
	Runtime     string
	Writer      string
}

type Object struct {
	Schema  string
	Name    string
	Kind    ObjectKind
	Class   ObjectClass
	Runtime []string
	Writer  []string
	Probe   string
}

type Matrix struct {
	Name            string
	SchemaVersion   int
	MigrationDigest string
	Roles           RoleTopology
	Objects         []Object
}

var (
	ErrInvalidMatrix      = errors.New("invalid authority matrix")
	ErrUnclassifiedObject = errors.New("unclassified authority object")
)

func (matrix Matrix) Validate() error {
	if matrix.Name == "" || matrix.SchemaVersion <= 0 || !isDigest(matrix.MigrationDigest) {
		return fmt.Errorf("%w: incomplete matrix identity", ErrInvalidMatrix)
	}
	roles := []string{matrix.Roles.Maintenance, matrix.Roles.Migrator, matrix.Roles.Runtime, matrix.Roles.Writer}
	seenRoles := make(map[string]struct{}, len(roles))
	for _, role := range roles {
		if role == "" {
			return fmt.Errorf("%w: empty role", ErrInvalidMatrix)
		}
		if _, exists := seenRoles[role]; exists {
			return fmt.Errorf("%w: duplicate role %s", ErrInvalidMatrix, role)
		}
		seenRoles[role] = struct{}{}
	}

	seenObjects := make(map[string]struct{}, len(matrix.Objects))
	for _, object := range matrix.Objects {
		if object.Class != Canonical && object.Class != ValidationAuthSession && object.Class != MigrationOnly {
			return fmt.Errorf("%w: %s", ErrUnclassifiedObject, object.Name)
		}
		if object.Schema == "" || object.Name == "" || (object.Kind != Table && object.Kind != Sequence && object.Kind != Routine) {
			return fmt.Errorf("%w: incomplete object %s", ErrInvalidMatrix, object.Name)
		}
		key := string(object.Kind) + ":" + object.Schema + "." + object.Name
		if _, exists := seenObjects[key]; exists {
			return fmt.Errorf("%w: duplicate object %s", ErrInvalidMatrix, key)
		}
		seenObjects[key] = struct{}{}
		if err := validateProbe(object); err != nil {
			return err
		}
		if err := validatePrivileges(object.Kind, object.Runtime); err != nil {
			return fmt.Errorf("%w: runtime privileges for %s: %v", ErrInvalidMatrix, object.Name, err)
		}
		if err := validatePrivileges(object.Kind, object.Writer); err != nil {
			return fmt.Errorf("%w: writer privileges for %s: %v", ErrInvalidMatrix, object.Name, err)
		}
	}
	return nil
}

func (matrix Matrix) Class(name string) ObjectClass {
	for _, object := range matrix.Objects {
		if object.Name == name {
			return object.Class
		}
	}
	return ""
}

func (matrix Matrix) Digest() string {
	canonical := matrix
	canonical.Objects = append([]Object(nil), matrix.Objects...)
	for index := range canonical.Objects {
		canonical.Objects[index].Runtime = sortedCopy(canonical.Objects[index].Runtime)
		canonical.Objects[index].Writer = sortedCopy(canonical.Objects[index].Writer)
	}
	sort.Slice(canonical.Objects, func(i, j int) bool {
		left, right := canonical.Objects[i], canonical.Objects[j]
		return string(left.Kind)+":"+left.Schema+"."+left.Name < string(right.Kind)+":"+right.Schema+"."+right.Name
	})
	payload, err := json.Marshal(canonical)
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func validateProbe(object Object) error {
	probe := strings.ToLower(strings.TrimSpace(object.Probe))
	if probe == "" || !strings.HasPrefix(probe, "select ") {
		return fmt.Errorf("%w: unsafe probe for %s", ErrInvalidMatrix, object.Name)
	}
	for _, unsafe := range []string{"nextval", "setval", "insert ", "update ", "delete ", "truncate ", "alter ", "drop ", "create ", ";"} {
		if strings.Contains(probe, unsafe) {
			return fmt.Errorf("%w: unsafe probe for %s", ErrInvalidMatrix, object.Name)
		}
	}
	return nil
}

func validatePrivileges(kind ObjectKind, privileges []string) error {
	allowed := map[ObjectKind]map[string]bool{
		Table:    {"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "TRUNCATE": true, "REFERENCES": true, "TRIGGER": true},
		Sequence: {"SELECT": true, "USAGE": true, "UPDATE": true},
		Routine:  {"EXECUTE": true},
	}
	seen := map[string]bool{}
	for _, privilege := range privileges {
		if !allowed[kind][privilege] || seen[privilege] {
			return fmt.Errorf("invalid or duplicate privilege %q", privilege)
		}
		seen[privilege] = true
	}
	return nil
}

func isDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func sortedCopy(values []string) []string {
	copyOfValues := append([]string(nil), values...)
	sort.Strings(copyOfValues)
	return copyOfValues
}
