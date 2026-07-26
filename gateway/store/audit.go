package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
)

type AuditAction string

const (
	AuditPlaceOrder  AuditAction = "place_order"
	AuditCancelOrder AuditAction = "cancel_order"
	AuditAmendOrder  AuditAction = "amend_order"
	AuditSetLeverage AuditAction = "set_leverage"
)

func (action AuditAction) Validate() error {
	switch action {
	case AuditPlaceOrder, AuditCancelOrder, AuditAmendOrder, AuditSetLeverage:
		return nil
	default:
		return domainErr("audit action is invalid")
	}
}

type AuditOutcome string

const (
	AuditRequested   AuditOutcome = "requested"
	AuditPrepared    AuditOutcome = "prepared"
	AuditUnsupported AuditOutcome = "unsupported"
)

func (outcome AuditOutcome) Validate() error {
	switch outcome {
	case AuditRequested, AuditPrepared, AuditUnsupported:
		return nil
	default:
		return domainErr("audit outcome is invalid")
	}
}

type AuditRecord struct {
	PrincipalID   domain.PrincipalID
	Action        AuditAction
	ResourceID    string
	Outcome       AuditOutcome
	CorrelationID domain.EventID
	AccountID     domain.AccountID
	InstrumentID  domain.InstrumentID
}

func (record AuditRecord) Validate() error {
	if err := record.PrincipalID.Validate(); err != nil {
		return err
	}
	if err := record.Action.Validate(); err != nil {
		return err
	}
	if record.ResourceID == "" || strings.TrimSpace(record.ResourceID) != record.ResourceID ||
		len(record.ResourceID) > 256 {
		return fmt.Errorf("audit resource id is invalid")
	}
	if err := record.Outcome.Validate(); err != nil {
		return err
	}
	if err := record.CorrelationID.Validate(); err != nil {
		return err
	}
	if record.AccountID != "" {
		if err := record.AccountID.Validate(); err != nil {
			return err
		}
	}
	if record.InstrumentID != "" {
		if err := record.InstrumentID.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type AuditStore interface {
	AppendAudit(context.Context, AuditRecord) error
}
