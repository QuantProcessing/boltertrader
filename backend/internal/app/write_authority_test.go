package app

import (
	"context"
	"testing"
)

func TestCanonicalWriteAuthorityRequiresEveryDatabaseFact(t *testing.T) {
	complete := WriteAuthorityFacts{
		RuntimeInherits: true, WriterUsage: true, FullCapabilityGrants: true,
		EffectiveTablePrivileges: true, EffectiveSequencePrivileges: true,
	}
	authority := NewWriteAuthority(complete)
	if ready, reason := authority.Ready(context.Background()); !ready || reason != "ready" {
		t.Fatalf("Ready()=(%v,%q)", ready, reason)
	}

	for name, mutate := range map[string]func(*WriteAuthorityFacts){
		"runtime inherit":     func(value *WriteAuthorityFacts) { value.RuntimeInherits = false },
		"writer usage":        func(value *WriteAuthorityFacts) { value.WriterUsage = false },
		"capability grants":   func(value *WriteAuthorityFacts) { value.FullCapabilityGrants = false },
		"table privileges":    func(value *WriteAuthorityFacts) { value.EffectiveTablePrivileges = false },
		"sequence privileges": func(value *WriteAuthorityFacts) { value.EffectiveSequencePrivileges = false },
		"canonical ownership": func(value *WriteAuthorityFacts) { value.CanonicalOwnershipViolations = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			facts := complete
			mutate(&facts)
			if ready, reason := NewWriteAuthority(facts).Ready(context.Background()); ready || reason == "" || reason == "ready" {
				t.Fatalf("Ready()=(%v,%q)", ready, reason)
			}
		})
	}
}
