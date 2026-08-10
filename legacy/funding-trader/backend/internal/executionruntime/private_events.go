package executionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/QuantProcessing/funding-trader/backend/internal/executionmonitor"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionservice"
	boltertrader "github.com/QuantProcessing/funding-trader/backend/internal/integrations/boltertrader"
	"github.com/QuantProcessing/funding-trader/backend/internal/uistream"
)

type privateEventClient interface {
	SubscribePrivateEvents(
		context.Context,
		boltertrader.PrivateEventSubscribeRequest,
	) (*boltertrader.PrivateEventStream, error)
}

type PrivateEventGateway struct {
	client privateEventClient
}

func NewPrivateEventGateway(client privateEventClient) *PrivateEventGateway {
	return &PrivateEventGateway{client: client}
}

func (gateway *PrivateEventGateway) SubscribePrivateEvents(
	ctx context.Context,
	request boltertrader.PrivateEventSubscribeRequest,
) (executionmonitor.PrivateStream, error) {
	if gateway == nil || gateway.client == nil {
		return nil, errors.New("private event gateway is unavailable")
	}
	return gateway.client.SubscribePrivateEvents(ctx, request)
}

type recoveryService interface {
	Recover(
		context.Context,
		executionservice.Scope,
		string,
	) (executionservice.Result, error)
}

type ExecutionReconciler struct {
	service recoveryService
	scope   executionservice.Scope
	prefix  string
	next    atomic.Uint64
}

func NewExecutionReconciler(
	service recoveryService,
	scope executionservice.Scope,
	runtimeID string,
) *ExecutionReconciler {
	runtimeID = strings.TrimSpace(runtimeID)
	if !safeRuntimeID.MatchString(runtimeID) {
		runtimeID = "runtime"
	}
	return &ExecutionReconciler{
		service: service,
		scope:   scope,
		prefix:  "private-event-" + runtimeID + "-",
	}
}

func (reconciler *ExecutionReconciler) Recover(ctx context.Context) error {
	if reconciler == nil ||
		reconciler.service == nil ||
		strings.TrimSpace(reconciler.scope.PrincipalID) == "" ||
		!isProductMode(reconciler.scope.Mode) {
		return errors.New("execution reconciler is unavailable")
	}
	requestID := reconciler.prefix + strconv.FormatUint(reconciler.next.Add(1), 10)
	_, err := reconciler.service.Recover(ctx, reconciler.scope, requestID)
	return err
}

var safeRuntimeID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,40}$`)

type privateEventStreamPublisher interface {
	Publish(context.Context, uistream.EventInput) (uistream.StoredEvent, error)
}

type PrivateEventResnapshotPublisher struct {
	stream privateEventStreamPublisher
}

func NewPrivateEventResnapshotPublisher(
	stream privateEventStreamPublisher,
) *PrivateEventResnapshotPublisher {
	return &PrivateEventResnapshotPublisher{stream: stream}
}

func (publisher *PrivateEventResnapshotPublisher) PublishResnapshotRequired(
	ctx context.Context,
	resnapshot executionmonitor.Resnapshot,
) error {
	if publisher == nil || publisher.stream == nil {
		return errors.New("private event resnapshot publisher is unavailable")
	}
	if !canonicalPrivateEventScopes(resnapshot.Scope) ||
		!safeResnapshotReason.MatchString(resnapshot.Reason) {
		return errors.New("private event resnapshot is invalid")
	}
	payload, err := json.Marshal(struct {
		Scope  []string `json:"scope"`
		Reason string   `json:"reason"`
	}{
		Scope:  []string{"executions", "positions", "activities"},
		Reason: resnapshot.Reason,
	})
	if err != nil {
		return err
	}
	_, err = publisher.stream.Publish(ctx, uistream.EventInput{
		Scope: "all",
		Kind:  uistream.EventResnapshotRequired,
		Data:  payload,
	})
	return err
}

func canonicalPrivateEventScopes(scopes []string) bool {
	if len(scopes) != 3 {
		return false
	}
	seen := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		switch scope {
		case "executions", "positions", "activities":
			seen[scope] = struct{}{}
		default:
			return false
		}
	}
	return len(seen) == 3
}

var safeResnapshotReason = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
