package boltertrader

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/marketdata"
	"github.com/QuantProcessing/funding-trader/backend/internal/readiness"
)

const publicDataProbeTimeout = 8 * time.Second

func GatewayComponent(client *GatewayClient) readiness.ComponentCheck {
	return func(ctx context.Context) readiness.Component {
		ready, err := client.Ready(ctx)
		if err != nil {
			return readiness.Component{Ready: false, Reason: "gateway-unreachable"}
		}
		if !ready.Ready {
			reason := "gateway-not-ready"
			if len(ready.Reasons) > 0 {
				reason = stableReason(ready.Reasons[0])
			}
			return readiness.Component{Ready: false, Reason: reason}
		}
		return readiness.Component{Ready: true}
	}
}

func AccountsComponent(client *GatewayClient) readiness.ComponentCheck {
	return func(ctx context.Context) readiness.Component {
		discovery, err := client.DiscoverAccounts(ctx)
		if err != nil {
			return readiness.Component{Ready: false, Reason: "accounts-unreachable"}
		}
		details := map[string]string{"count": stringInt(len(discovery.Accounts))}
		if discovery.Mixed {
			details["mixedVenueEnvironments"] = "true"
		}
		if !discovery.Ready {
			reason := "accounts-not-ready"
			if len(discovery.Reasons) > 0 {
				reason = stableReason(discovery.Reasons[0])
			}
			return readiness.Component{Ready: false, Reason: reason, Details: details}
		}
		details["mode"] = string(discovery.Mode)
		return readiness.Component{Ready: true, Details: details}
	}
}

func PublicDataNotConfiguredComponent() readiness.ComponentCheck {
	return func(context.Context) readiness.Component {
		return readiness.Component{Ready: false, Reason: "public-data-client-not-configured"}
	}
}

type PublicDataProber interface {
	Probe(context.Context) (marketdata.Readiness, error)
}

func PublicDataComponent(probe PublicDataProber) readiness.ComponentCheck {
	return func(ctx context.Context) readiness.Component {
		if probe == nil {
			return readiness.Component{Ready: false, Reason: "public-data-unreachable"}
		}
		probeContext, cancel := context.WithTimeout(ctx, publicDataProbeTimeout)
		defer cancel()
		status, err := probe.Probe(probeContext)
		if err != nil {
			reason := "public-data-unreachable"
			switch {
			case errors.Is(err, marketdata.ErrNoConfiguredInstruments):
				reason = "public-data-no-instruments"
			case errors.Is(err, marketdata.ErrInvalidMarketData):
				reason = "public-data-invalid"
			}
			return readiness.Component{Ready: false, Reason: reason}
		}
		if status.Mode == "" || status.Accounts <= 0 || status.Instruments <= 0 {
			return readiness.Component{Ready: false, Reason: "public-data-invalid"}
		}
		return readiness.Component{
			Ready: true,
			Details: map[string]string{
				"mode":        status.Mode,
				"accounts":    stringInt(status.Accounts),
				"instruments": stringInt(status.Instruments),
			},
		}
	}
}

func stableReason(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "not-ready"
	}
	value = strings.ReplaceAll(value, "_", "-")
	value = strings.ToLower(value)
	if strings.ContainsAny(value, "\n\r\t /:") {
		return "not-ready"
	}
	return value
}

func stringInt(value int) string {
	if value == 0 {
		return "0"
	}
	var buf [20]byte
	index := len(buf)
	for value > 0 {
		index--
		buf[index] = byte('0' + value%10)
		value /= 10
	}
	return string(buf[index:])
}
