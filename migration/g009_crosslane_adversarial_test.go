package migration

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestCrossLaneRejectsTamperedFrozenProvenance(t *testing.T) {
	baseline := readManifest(t)
	if err := ValidateBaseline(baseline); err != nil {
		t.Fatalf("repository baseline must validate before tamper cases: %v", err)
	}

	tests := map[string]func(map[string]any){
		"migration id": func(manifest map[string]any) {
			manifest["migrationId"] = "attacker-controlled-migration"
		},
		"contract revision": func(manifest map[string]any) {
			manifest["contractRevision"] = float64(99)
		},
		"recorded timestamp": func(manifest map[string]any) {
			manifest["recordedAt"] = "2030-01-01T00:00:00Z"
		},
		"source repository": func(manifest map[string]any) {
			manifest["sourceRepo"] = "/tmp/attacker-source"
		},
		"source remote name": func(manifest map[string]any) {
			manifest["sourceRemoteName"] = "mirror"
		},
		"source remote": func(manifest map[string]any) {
			manifest["sourceRemote"] = "https://example.invalid/attacker/source.git"
		},
		"source branch": func(manifest map[string]any) {
			manifest["sourceBranch"] = "attacker-branch"
		},
		"source commit and quiescence": func(manifest map[string]any) {
			const altered = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			manifest["sourceCommit"] = altered
			manifest["quiescence"].(map[string]any)["frozenCommit"] = altered
		},
		"source tree and quiescence": func(manifest map[string]any) {
			const altered = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			manifest["sourceTreeHash"] = altered
			manifest["quiescence"].(map[string]any)["frozenTreeHash"] = altered
		},
		"target repository": func(manifest map[string]any) {
			manifest["targetRepo"] = "/tmp/attacker-target"
		},
		"target remote name": func(manifest map[string]any) {
			manifest["targetRemoteName"] = "mirror"
		},
		"target remote": func(manifest map[string]any) {
			manifest["targetRemote"] = "https://example.invalid/attacker/target.git"
		},
		"target branch": func(manifest map[string]any) {
			manifest["targetBranch"] = "attacker-branch"
		},
		"target commit": func(manifest map[string]any) {
			manifest["targetCommit"] = "cccccccccccccccccccccccccccccccccccccccc"
		},
		"target tree": func(manifest map[string]any) {
			manifest["targetTreeHash"] = "dddddddddddddddddddddddddddddddddddddddd"
		},
		"subtree prefix": func(manifest map[string]any) {
			manifest["subtreePrefix"] = "legacy/attacker-controlled"
		},
		"approved contract hash": func(manifest map[string]any) {
			hashes := manifest["approvedContractHashes"].(map[string]any)
			for name := range hashes {
				hashes[name] = strings.Repeat("e", 64)
				break
			}
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			var manifest map[string]any
			if err := json.Unmarshal(baseline, &manifest); err != nil {
				t.Fatal(err)
			}
			mutate(manifest)
			tampered, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			before := bytes.Clone(tampered)

			if err := ValidateBaseline(tampered); err == nil {
				t.Fatal("ValidateBaseline accepted tampered frozen provenance")
			}
			if !bytes.Equal(tampered, before) {
				t.Fatal("ValidateBaseline mutated the supplied manifest")
			}
		})
	}
}

func TestCrossLaneRejectsGitShapedImportWithFabricatedProof(t *testing.T) {
	manifestPath := copyManifest(t)
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	request := withProof(t, manifestPath, Request{
		Operation: RecordImport,
		ID:        "0123456789abcdef0123456789abcdef01234567",
	})

	if _, err := Apply(manifestPath, request, ApplyOptions{}); err == nil {
		t.Fatal("Apply accepted a Git-shaped import ID with caller-authored proof")
	}
	after, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("rejected fabricated import proof changed the manifest")
	}
}
