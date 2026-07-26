package reference

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/capability"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
)

const ReasonRiskLimitsNotNormalized = "risk_limits_not_normalized"

type Registry interface {
	Accounts() []account.Resource
	Capability(domain.AccountID) (capability.Row, error)
	Instruments(domain.AccountID) (catalog.Snapshot, error)
}

type Service struct{ registry Registry }

type AccountCapability struct {
	AccountID domain.AccountID `json:"account_id"`
	Row       capability.Row   `json:"capability"`
}

type InstrumentRules struct {
	InstrumentID domain.InstrumentID `json:"instrument_id"`
	Rules        catalog.Rules       `json:"rules"`
}

type RiskLimit struct{}

type RiskLimitReference struct {
	Available bool        `json:"available"`
	Reason    string      `json:"reason"`
	Limits    []RiskLimit `json:"limits"`
}

func NewService(registry Registry) (*Service, error) {
	if registry == nil {
		return nil, fmt.Errorf("reference registry is required")
	}
	return &Service{registry: registry}, nil
}

func (service *Service) Accounts() []account.Resource {
	resources := service.registry.Accounts()
	sort.Slice(resources, func(i, j int) bool { return resources[i].AccountID < resources[j].AccountID })
	return resources
}

func (service *Service) Capabilities(accountID domain.AccountID) ([]AccountCapability, error) {
	accountIDs, err := service.accountIDs(accountID)
	if err != nil {
		return nil, err
	}
	result := make([]AccountCapability, 0, len(accountIDs))
	for _, current := range accountIDs {
		row, err := service.registry.Capability(current)
		if err != nil {
			return nil, err
		}
		result = append(result, AccountCapability{AccountID: current, Row: row})
	}
	return result, nil
}

func (service *Service) Instruments(accountID domain.AccountID) ([]catalog.Instrument, error) {
	accountIDs, err := service.accountIDs(accountID)
	if err != nil {
		return nil, err
	}
	byID := make(map[domain.InstrumentID]catalog.Instrument)
	var firstErr error
	for _, current := range accountIDs {
		snapshot, err := service.registry.Instruments(current)
		if err != nil {
			if accountID != "" {
				return nil, err
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, instrument := range snapshot.Instruments {
			if existing, ok := byID[instrument.ID]; ok && !reflect.DeepEqual(existing, instrument) {
				return nil, fmt.Errorf("conflicting public instrument projection")
			}
			byID[instrument.ID] = instrument
		}
	}
	if len(byID) == 0 && firstErr != nil {
		return nil, firstErr
	}
	result := make([]catalog.Instrument, 0, len(byID))
	for _, instrument := range byID {
		result = append(result, instrument)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (service *Service) Rules(accountID domain.AccountID) ([]InstrumentRules, error) {
	instruments, err := service.Instruments(accountID)
	if err != nil {
		return nil, err
	}
	result := make([]InstrumentRules, 0, len(instruments))
	for _, instrument := range instruments {
		result = append(result, InstrumentRules{InstrumentID: instrument.ID, Rules: instrument.Rules})
	}
	return result, nil
}

func (service *Service) RiskLimits(domain.AccountID) RiskLimitReference {
	return RiskLimitReference{
		Available: false,
		Reason:    ReasonRiskLimitsNotNormalized,
		Limits:    []RiskLimit{},
	}
}

func (service *Service) accountIDs(filter domain.AccountID) ([]domain.AccountID, error) {
	accounts := service.registry.Accounts()
	if filter != "" {
		for _, resource := range accounts {
			if resource.AccountID == filter {
				return []domain.AccountID{filter}, nil
			}
		}
		return nil, account.ErrUnknownAccount
	}
	accountIDs := make([]domain.AccountID, 0, len(accounts))
	for _, resource := range accounts {
		accountIDs = append(accountIDs, resource.AccountID)
	}
	sort.Slice(accountIDs, func(i, j int) bool { return accountIDs[i] < accountIDs[j] })
	return accountIDs, nil
}
