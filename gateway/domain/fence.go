package domain

import "fmt"

type RouteID string

func (id RouteID) Validate() error { return validateSafeToken("route id", string(id)) }

type AccountFence struct {
	AccountID            AccountID `json:"accountId"`
	DesiredStateRevision uint64    `json:"desiredStateRevision"`
	RuntimeGeneration    uint64    `json:"runtimeGeneration"`
	CredentialVersion    uint64    `json:"credentialVersion"`
}

func (fence AccountFence) Validate() error {
	if err := fence.AccountID.Validate(); err != nil {
		return err
	}
	if fence.DesiredStateRevision == 0 {
		return fmt.Errorf("desired state revision must be positive")
	}
	if fence.RuntimeGeneration == 0 {
		return fmt.Errorf("runtime generation must be positive")
	}
	if fence.CredentialVersion == 0 {
		return fmt.Errorf("credential version must be positive")
	}
	return nil
}

type WriteFence struct {
	AccountFence
	RouteID RouteID `json:"routeId"`
}

func (fence WriteFence) Validate() error {
	if err := fence.AccountFence.Validate(); err != nil {
		return err
	}
	return fence.RouteID.Validate()
}
