package migration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

type Manifest struct {
	SchemaVersion           int               `json:"schemaVersion"`
	MigrationID             string            `json:"migrationId"`
	ContractRevision        int               `json:"contractRevision"`
	RecordedAt              string            `json:"recordedAt"`
	SourceRepo              string            `json:"sourceRepo"`
	SourceRemoteName        string            `json:"sourceRemoteName"`
	SourceRemote            string            `json:"sourceRemote"`
	SourceBranch            string            `json:"sourceBranch"`
	SourceCommit            string            `json:"sourceCommit"`
	SourceTreeHash          string            `json:"sourceTreeHash"`
	SourceQuiesceAt         string            `json:"sourceQuiesceAt"`
	TargetRepo              string            `json:"targetRepo"`
	TargetRemoteName        string            `json:"targetRemoteName"`
	TargetRemote            string            `json:"targetRemote"`
	TargetBranch            string            `json:"targetBranch"`
	TargetCommit            string            `json:"targetCommit"`
	TargetTreeHash          string            `json:"targetTreeHash"`
	SubtreePrefix           string            `json:"subtreePrefix"`
	ImportCommit            *string           `json:"importCommit"`
	BackfillScripts         []string          `json:"backfillScripts"`
	BackupID                *string           `json:"backupId"`
	BackupSHA256            *string           `json:"backupSha256"`
	RestoreVerifiedAt       *string           `json:"restoreVerifiedAt"`
	FinalDeltaAt            *string           `json:"finalDeltaAt"`
	FinalDeltaSHA256        *string           `json:"finalDeltaSha256"`
	OneWriterFenceAt        *string           `json:"oneWriterFenceAt"`
	ValidationAt            *string           `json:"validationAt"`
	TargetActivationAt      *string           `json:"targetActivationAt"`
	CutoverAt               *string           `json:"cutoverAt"`
	RollbackBoundary        string            `json:"rollbackBoundary"`
	ForwardRecoveryBoundary string            `json:"forwardRecoveryBoundary"`
	SourceRetirementAt      *string           `json:"sourceRetirementAt"`
	Quiescence              Quiescence        `json:"quiescence"`
	OneWriter               OneWriter         `json:"oneWriter"`
	ApprovedContractHashes  map[string]string `json:"approvedContractHashes"`
	Workflow                Workflow          `json:"workflow,omitempty"`
}

type Quiescence struct {
	Status         string `json:"status"`
	WorktreeClean  bool   `json:"worktreeClean"`
	FrozenCommit   string `json:"frozenCommit"`
	FrozenTreeHash string `json:"frozenTreeHash"`
	AllowedChanges string `json:"allowedChanges"`
}

type OneWriter struct {
	Owner                        string   `json:"owner"`
	FenceRaised                  bool     `json:"fenceRaised"`
	TargetCanonicalWritesEnabled bool     `json:"targetCanonicalWritesEnabled"`
	NextOwner                    string   `json:"nextOwner"`
	TransitionRequires           []string `json:"transitionRequires"`
}

func DecodeManifest(data []byte) (Manifest, error) {
	if err := rejectDuplicateKeys(data); err != nil {
		return Manifest{}, fmt.Errorf("decode migration manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode migration manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return Manifest{}, fmt.Errorf("decode migration manifest: %w", err)
	}
	return manifest, nil
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate field %q", key)
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	_, err = decoder.Token()
	return err
}
