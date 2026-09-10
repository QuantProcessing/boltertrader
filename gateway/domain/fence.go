package domain

type RouteID string

func (id RouteID) Validate() error { return validateSafeToken("route id", string(id)) }

type AccountFence struct {
	AccountID AccountID `json:"accountId"`
	RuntimeID string    `json:"runtimeId"`
}

func (fence AccountFence) Validate() error {
	if err := fence.AccountID.Validate(); err != nil {
		return err
	}
	return validateSafeToken("runtime id", fence.RuntimeID)
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
