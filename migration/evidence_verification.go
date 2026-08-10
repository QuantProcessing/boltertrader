package migration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type EvidenceVerificationRequest struct {
	ManifestPath        string
	EvidenceDir         string
	ImportCommit        string
	ActivationProofPath string
}

// VerifyEvidence revalidates proof files that cannot be replayed through an
// executable migration operation. It does not mutate the manifest or evidence.
func VerifyEvidence(request EvidenceVerificationRequest) error {
	data, err := os.ReadFile(request.ManifestPath)
	if err != nil {
		return fmt.Errorf("read migration manifest: %w", err)
	}
	manifest, err := DecodeManifest(data)
	if err != nil {
		return err
	}
	if err := validateBaseline(manifest); err != nil {
		return err
	}
	if manifest.Workflow.Phase == "" {
		manifest.Workflow.Phase = inferPhase(manifest)
	}
	if err := validateWorkflow(manifest); err != nil {
		return err
	}

	importEvent := workflowEvent(manifest, RecordImport)
	if importEvent != nil {
		if manifest.ImportCommit == nil || request.ImportCommit != *manifest.ImportCommit || importEvent.Name != request.ImportCommit {
			return fmt.Errorf("record-import evidence does not match the durable import commit")
		}
		if err := validateGitProvenance(manifest, request.ImportCommit); err != nil {
			return err
		}
		proofPath := filepath.Join(request.EvidenceDir, fmt.Sprintf("%03d-%s.json", importEvent.Sequence, RecordImport))
		proofData, digest, err := readProof(proofPath)
		if err != nil || digest != importEvent.ProofSHA {
			return fmt.Errorf("record-import evidence changed")
		}
		var proof operationProof
		if err := json.Unmarshal(proofData, &proof); err != nil {
			return fmt.Errorf("record-import generated evidence is invalid")
		}
		proofRequest := Request{Operation: RecordImport, ID: request.ImportCommit, generatedEvidence: &proof}
		if err := validateTypedProof(proofData, manifest, proofRequest, ""); err != nil {
			return fmt.Errorf("record-import generated evidence is invalid: %w", err)
		}
		if proof.InputSHA != sha256Hex([]byte(request.ImportCommit)) || len(proof.Commands) != 1 ||
			proof.Commands[0].Executable != "git-provenance-verifier" ||
			proof.Commands[0].ArgvSHA256 != sha256Hex([]byte(request.ImportCommit)) ||
			proof.Commands[0].OutputSHA != sha256Hex([]byte("verified")) {
			return fmt.Errorf("record-import generated evidence is invalid")
		}
	}

	activationEvent := workflowEvent(manifest, RecordActivation)
	if activationEvent == nil {
		if request.ActivationProofPath != "" {
			return fmt.Errorf("activation proof supplied before durable activation evidence")
		}
		return nil
	}
	proofData, digest, err := readProof(request.ActivationProofPath)
	if err != nil || digest != activationEvent.ProofSHA {
		return fmt.Errorf("record-activation-evidence evidence changed")
	}
	preActivation := manifest
	preActivation.Workflow.Phase = PhaseActivationReady
	proofRequest := Request{
		Operation: RecordActivation, Verifier: activationEvent.Verifier,
		ActivationAt: activationEvent.ActivationAt, CutoverAt: activationEvent.CutoverAt,
	}
	if err := validateTypedProof(proofData, preActivation, proofRequest, ""); err != nil {
		return fmt.Errorf("record-activation-evidence is invalid: %w", err)
	}
	return nil
}

func workflowEvent(manifest Manifest, operation Operation) *Event {
	for index := range manifest.Workflow.Events {
		if manifest.Workflow.Events[index].Operation == operation {
			return &manifest.Workflow.Events[index]
		}
	}
	return nil
}
