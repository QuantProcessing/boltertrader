package app

import "context"

type WriteAuthority interface {
	Ready(context.Context) (bool, string)
}

type WriteAuthorityFacts struct {
	RuntimeInherits              bool
	WriterUsage                  bool
	FullCapabilityGrants         bool
	EffectiveTablePrivileges     bool
	EffectiveSequencePrivileges  bool
	CanonicalOwnershipViolations int
}

type databaseWriteAuthority struct {
	facts WriteAuthorityFacts
}

func NewWriteAuthority(facts WriteAuthorityFacts) WriteAuthority {
	return databaseWriteAuthority{facts: facts}
}

func (authority databaseWriteAuthority) Ready(ctx context.Context) (bool, string) {
	if err := ctx.Err(); err != nil {
		return false, "authority_check_canceled"
	}
	switch {
	case !authority.facts.RuntimeInherits:
		return false, "runtime_noinherit"
	case !authority.facts.WriterUsage:
		return false, "writer_role_unavailable"
	case !authority.facts.FullCapabilityGrants:
		return false, "writer_capability_grants_incomplete"
	case !authority.facts.EffectiveTablePrivileges:
		return false, "writer_table_privileges_incomplete"
	case !authority.facts.EffectiveSequencePrivileges:
		return false, "writer_sequence_privileges_incomplete"
	case authority.facts.CanonicalOwnershipViolations != 0:
		return false, "runtime_owns_canonical_object"
	default:
		return true, "ready"
	}
}

type unavailableWriteAuthority struct{}

func (unavailableWriteAuthority) Ready(context.Context) (bool, string) {
	return false, "database_authority_not_verified"
}
