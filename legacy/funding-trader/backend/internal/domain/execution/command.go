package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

type CommandPayload struct {
	ExecutionID string
	Preview     PreviewResult
	secret      string
}

type NormalizedCommand struct {
	ExecutionID          string       `json:"executionId"`
	PrincipalID          string       `json:"principalId"`
	Mode                 string       `json:"mode"`
	Operation            Operation    `json:"operation"`
	OpportunityID        string       `json:"opportunityId"`
	PositionID           string       `json:"positionId,omitempty"`
	ParentExecutionID    string       `json:"parentExecutionId,omitempty"`
	LineageID            string       `json:"lineageId,omitempty"`
	CapitalUSDT          string       `json:"capitalUsdt"`
	Leverage             int          `json:"leverage"`
	ExpectedNetAprPct    string       `json:"expectedNetAprPct"`
	InitialLongQuantity  string       `json:"initialLongQuantity"`
	InitialShortQuantity string       `json:"initialShortQuantity"`
	SingleLegNotional    string       `json:"singleLegNotional"`
	GrossExposure        string       `json:"grossExposure"`
	GrossProfit7d        string       `json:"grossProfit7d"`
	FeesUSDT             string       `json:"feesUsdt"`
	SlippageUSDT         string       `json:"slippageUsdt"`
	NetProfit7d          string       `json:"netProfit7d"`
	MaxSlippagePct       string       `json:"maxSlippagePct"`
	PlannedLegs          []CommandLeg `json:"plannedLegs"`
}

type NormalizedRequest struct {
	Method               string       `json:"method"`
	Path                 string       `json:"path"`
	PrincipalID          string       `json:"principalId"`
	Mode                 string       `json:"mode"`
	Operation            Operation    `json:"operation"`
	OpportunityID        string       `json:"opportunityId"`
	PositionID           string       `json:"positionId,omitempty"`
	ParentExecutionID    string       `json:"parentExecutionId,omitempty"`
	LineageID            string       `json:"lineageId,omitempty"`
	CapitalUSDT          string       `json:"capitalUsdt"`
	Leverage             int          `json:"leverage"`
	ExpectedNetAprPct    string       `json:"expectedNetAprPct"`
	InitialLongQuantity  string       `json:"initialLongQuantity"`
	InitialShortQuantity string       `json:"initialShortQuantity"`
	SnapshotID           string       `json:"snapshotId"`
	SingleLegNotional    string       `json:"singleLegNotional"`
	GrossExposure        string       `json:"grossExposure"`
	GrossProfit7d        string       `json:"grossProfit7d"`
	FeesUSDT             string       `json:"feesUsdt"`
	SlippageUSDT         string       `json:"slippageUsdt"`
	NetProfit7d          string       `json:"netProfit7d"`
	MaxSlippagePct       string       `json:"maxSlippagePct"`
	PlannedLegs          []CommandLeg `json:"plannedLegs"`
}

type CommandLeg struct {
	LegIdentity
	Role                LegRole `json:"role"`
	Side                Side    `json:"side"`
	Symbol              string  `json:"symbol"`
	GatewayInstrumentID string  `json:"gatewayInstrumentId"`
	MarkPrice           string  `json:"markPrice"`
	Notional            string  `json:"notional"`
	Quantity            string  `json:"quantity"`
	ReduceOnly          bool    `json:"reduceOnly"`
}

func NewCommandPayload(executionID string, preview PreviewResult, secret string) CommandPayload {
	return CommandPayload{ExecutionID: executionID, Preview: preview, secret: secret}
}

func (payload CommandPayload) Normalized() NormalizedCommand {
	legs := make([]CommandLeg, 0, len(payload.Preview.PlannedLegs))
	for _, leg := range payload.Preview.PlannedLegs {
		legs = append(legs, CommandLeg{
			LegIdentity:         leg.LegIdentity,
			Role:                leg.Role,
			Side:                leg.Side,
			Symbol:              leg.Symbol,
			GatewayInstrumentID: leg.GatewayInstrumentID,
			MarkPrice:           leg.MarkPrice.String(),
			Notional:            leg.Notional.String(),
			Quantity:            leg.Quantity.String(),
			ReduceOnly:          leg.ReduceOnly,
		})
	}
	return NormalizedCommand{
		ExecutionID:          payload.ExecutionID,
		PrincipalID:          payload.Preview.PrincipalID,
		Mode:                 payload.Preview.Mode,
		Operation:            payload.Preview.Operation,
		OpportunityID:        payload.Preview.OpportunityID,
		PositionID:           payload.Preview.PositionID,
		ParentExecutionID:    payload.Preview.ParentExecutionID,
		LineageID:            payload.Preview.LineageID,
		CapitalUSDT:          payload.Preview.CapitalUSDT.String(),
		Leverage:             payload.Preview.Leverage,
		ExpectedNetAprPct:    payload.Preview.ExpectedNetAprPct.String(),
		InitialLongQuantity:  payload.Preview.InitialLongQuantity.String(),
		InitialShortQuantity: payload.Preview.InitialShortQuantity.String(),
		SingleLegNotional:    payload.Preview.SingleLegNotional.String(),
		GrossExposure:        payload.Preview.GrossExposure.String(),
		GrossProfit7d:        payload.Preview.GrossProfit7d.String(),
		FeesUSDT:             payload.Preview.FeesUSDT.String(),
		SlippageUSDT:         payload.Preview.SlippageUSDT.String(),
		NetProfit7d:          payload.Preview.NetProfit7d.String(),
		MaxSlippagePct:       payload.Preview.MaxSlippagePct.String(),
		PlannedLegs:          legs,
	}
}

func (payload CommandPayload) Hash() (string, error) {
	encoded, err := json.Marshal(payload.Normalized())
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func (payload CommandPayload) RequestHash(method string, path string) (string, error) {
	normalized := payload.Normalized()
	request := NormalizedRequest{
		Method:               method,
		Path:                 path,
		PrincipalID:          normalized.PrincipalID,
		Mode:                 normalized.Mode,
		Operation:            normalized.Operation,
		OpportunityID:        normalized.OpportunityID,
		PositionID:           normalized.PositionID,
		ParentExecutionID:    normalized.ParentExecutionID,
		LineageID:            normalized.LineageID,
		CapitalUSDT:          normalized.CapitalUSDT,
		Leverage:             normalized.Leverage,
		ExpectedNetAprPct:    normalized.ExpectedNetAprPct,
		InitialLongQuantity:  normalized.InitialLongQuantity,
		InitialShortQuantity: normalized.InitialShortQuantity,
		SnapshotID:           payload.Preview.SnapshotID,
		SingleLegNotional:    normalized.SingleLegNotional,
		GrossExposure:        normalized.GrossExposure,
		GrossProfit7d:        normalized.GrossProfit7d,
		FeesUSDT:             normalized.FeesUSDT,
		SlippageUSDT:         normalized.SlippageUSDT,
		NetProfit7d:          normalized.NetProfit7d,
		MaxSlippagePct:       normalized.MaxSlippagePct,
		PlannedLegs:          normalized.PlannedLegs,
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
