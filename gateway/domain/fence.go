package domain

import "fmt"

type RouteID string

func (id RouteID) Validate() error { return validateSafeToken("route id", string(id)) }

type AccountFence struct {
	AccountID AccountID `json:"accountId"`
	RuntimeID string    `json:"runtimeId"`

	// Deprecated compatibility fields. They are no longer persisted or emitted
	// by production APIs; RuntimeID is the sole runtime ownership fence. Keeping
	// these fields for one source-compatibility window lets old in-process test
	// fixtures fail closed while callers migrate to RuntimeID.
	DesiredStateRevision uint64 `json:"-"`
	RuntimeGeneration    uint64 `json:"-"`
	CredentialVersion    uint64 `json:"-"`
}

func (fence AccountFence) Validate() error {
	if err := fence.AccountID.Validate(); err != nil {
		return err
	}
	if fence.RuntimeID == "" {
		// Remove this branch after downstream packages have completed their source
		// migration. It does not cross the wire or reach the database.
		if fence.DesiredStateRevision == 0 || fence.RuntimeGeneration == 0 || fence.CredentialVersion == 0 {
			return fmt.Errorf("runtime id is required")
		}
	} else if err := validateSafeToken("runtime id", fence.RuntimeID); err != nil {
		return err
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
