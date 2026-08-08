package planning

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/calculation"
	fundingpolicy "github.com/QuantProcessing/boltertrader/backend/internal/products/funding/policy"
)

const FactKind = "funding.execution-input"

var (
	ErrInvalidWork = errors.New("invalid Funding work")
	ErrStaleFences = errors.New("stale Funding account fences")
)

type Fact struct {
	Kind    string
	Payload json.RawMessage
}

type PlanningFact struct {
	Input                  calculation.Input      `json:"calculationInput"`
	PrincipalID            string                 `json:"principalId"`
	LongReservation        string                 `json:"longReservationId"`
	ShortReservation       string                 `json:"shortReservationId"`
	ReservationAuthorities []ReservationAuthority `json:"reservationAuthorities"`
	PolicyAssumptions      []PolicyAssumption     `json:"policyAssumptions"`
}

type ReservationAuthority struct {
	ReservationID    string     `json:"reservationId"`
	Revision         time.Time  `json:"revision"`
	PrincipalID      string     `json:"principalId"`
	Mode             string     `json:"mode"`
	ProductID        string     `json:"productId"`
	Kind             string     `json:"kind"`
	AccountID        string     `json:"accountId"`
	Venue            string     `json:"venue"`
	VenueEnvironment string     `json:"venueEnvironment"`
	InstrumentID     string     `json:"instrumentId"`
	PositionMode     *string    `json:"positionMode"`
	SettlementScope  *string    `json:"settlementScope"`
	Quantity         *string    `json:"quantity"`
	Notional         *string    `json:"notional"`
	Currency         *string    `json:"currency"`
	Status           string     `json:"status"`
	ExpiresAt        *time.Time `json:"expiresAt"`
}

type PolicyAssumption struct {
	Code  string `json:"code"`
	Value string `json:"value"`
}

func NewPlanningFact(input calculation.Input, principal, longReservation, shortReservation string, authorities []ReservationAuthority, assumptions []PolicyAssumption) PlanningFact {
	input.AuthorityIdentity = authorityIdentity(principal, authorities)
	return PlanningFact{Input: input, PrincipalID: principal, LongReservation: longReservation, ShortReservation: shortReservation, ReservationAuthorities: authorities, PolicyAssumptions: assumptions}
}

type OpenIntent struct {
	PreviewID             string `json:"previewId"`
	OpportunityID         string `json:"opportunityId"`
	LongAccountID         string `json:"longAccountId"`
	ShortAccountID        string `json:"shortAccountId"`
	CanonicalMarketID     string `json:"canonicalMarketId"`
	NotionalPerLeg        string `json:"notionalPerLeg"`
	MaxSlippageBps        string `json:"maxSlippageBps"`
	HoldThroughSettlement bool   `json:"holdThroughSettlement"`
}

type OpenPlan struct {
	Plan               calculation.Plan
	Intent             OpenIntent
	IntentPayload      json.RawMessage
	LongReservationID  string
	ShortReservationID string
	MaxSlippageBps     int
	AccountFences      map[string]AccountFence
}

type AccountFence struct {
	AccountID            string `json:"accountId"`
	DesiredStateRevision int64  `json:"desiredStateRevision"`
	RuntimeGeneration    int    `json:"runtimeGeneration"`
	CredentialVersion    int64  `json:"credentialVersion"`
}

func DeriveOpen(workPayload, accountFences json.RawMessage, facts []Fact) (OpenPlan, error) {
	return DeriveOpenWithAdmission(workPayload, accountFences, facts, fundingpolicy.DefaultAdmission())
}

func DeriveOpenWithAdmission(workPayload, accountFences json.RawMessage, facts []Fact, admission fundingpolicy.Admission) (OpenPlan, error) {
	var request OpenIntent
	if err := decodeExact(workPayload, &request); err != nil {
		return OpenPlan{}, fmt.Errorf("%w: decode open request", ErrInvalidWork)
	}
	planning, err := planningFact(facts)
	if err != nil {
		return OpenPlan{}, err
	}
	fences, err := validateFences(accountFences, planning.Input)
	if err != nil {
		return OpenPlan{}, err
	}
	plan, err := calculation.ComputeWithAdmission(planning.Input, admission)
	if err != nil {
		return OpenPlan{}, err
	}
	if !plan.CanSubmit {
		return OpenPlan{}, ErrInvalidWork
	}
	maxSlippage, err := strconv.Atoi(request.MaxSlippageBps)
	if err != nil || maxSlippage < 0 || len(plan.Legs) != 2 || request.PreviewID != plan.PreviewID || request.OpportunityID != plan.OpportunityID ||
		request.LongAccountID != plan.Legs[0].AccountID || request.ShortAccountID != plan.Legs[1].AccountID || request.CanonicalMarketID != plan.RouteID ||
		request.NotionalPerLeg != planning.Input.RequestedNotionalPerLeg.String() {
		return OpenPlan{}, ErrInvalidWork
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return OpenPlan{}, err
	}
	return OpenPlan{Plan: plan, Intent: request, IntentPayload: payload, LongReservationID: planning.LongReservation, ShortReservationID: planning.ShortReservation, MaxSlippageBps: maxSlippage, AccountFences: fences}, nil
}

func planningFact(facts []Fact) (PlanningFact, error) {
	var result PlanningFact
	found := false
	for _, fact := range facts {
		if fact.Kind != FactKind {
			continue
		}
		if found || decodeExact(fact.Payload, &result) != nil {
			return PlanningFact{}, ErrInvalidWork
		}
		found = true
	}
	if !found || result.LongReservation == "" || result.ShortReservation == "" || result.PrincipalID == "" || len(result.ReservationAuthorities) != 2 || result.ReservationAuthorities[0].ReservationID != result.LongReservation || result.ReservationAuthorities[1].ReservationID != result.ShortReservation || result.Input.AuthorityIdentity != authorityIdentity(result.PrincipalID, result.ReservationAuthorities) {
		return PlanningFact{}, ErrInvalidWork
	}
	return result, nil
}

func authorityIdentity(principal string, authorities []ReservationAuthority) string {
	payload, _ := json.Marshal(struct {
		Principal    string
		Reservations []ReservationAuthority
	}{principal, authorities})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func validateFences(encoded json.RawMessage, input calculation.Input) (map[string]AccountFence, error) {
	var fences []AccountFence
	if err := decodeExact(encoded, &fences); err != nil || len(fences) != 2 {
		return nil, ErrStaleFences
	}
	want := map[string]int{
		input.Long.AccountFence.AccountID:  input.Long.AccountFence.RuntimeGeneration,
		input.Short.AccountFence.AccountID: input.Short.AccountFence.RuntimeGeneration,
	}
	if len(want) != 2 {
		return nil, ErrStaleFences
	}
	exact := make(map[string]AccountFence, len(fences))
	for _, fence := range fences {
		if want[fence.AccountID] != fence.RuntimeGeneration || fence.DesiredStateRevision < 1 || fence.RuntimeGeneration < 1 || fence.CredentialVersion < 1 {
			return nil, ErrStaleFences
		}
		exact[fence.AccountID] = fence
		delete(want, fence.AccountID)
	}
	if len(want) != 0 {
		return nil, ErrStaleFences
	}
	return exact, nil
}

func decodeExact(payload json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}
