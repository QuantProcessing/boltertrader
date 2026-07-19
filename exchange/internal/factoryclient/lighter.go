package factoryclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/sdk/lighter"
)

type lighterSpotClient struct {
	*spotClient
	sdk   *lighter.Client
	ws    exchange.SpotWebSocket
	state *lighterRESTState
}

func NewLighterSpot(privateKey string, accountIndex int64, keyIndex uint8, settings Settings) exchange.SpotClient {
	sdkClient := lighter.NewClient().WithCredentials(privateKey, accountIndex, keyIndex)
	if settings.Environment == "testnet" {
		sdkClient.WithEnvironment(lighter.EnvironmentTestnet)
	} else {
		sdkClient.WithEnvironment(lighter.EnvironmentMainnet)
	}
	if settings.Endpoint != "" {
		sdkClient.WithBaseURL(settings.Endpoint)
	}
	sdkClient.HTTPClient = lighterTrackedHTTPClient(settings.HTTPClient)
	client := &lighterSpotClient{
		spotClient: &spotClient{meta: clientMeta{venue: exchange.VenueLighter, product: exchange.ProductSpot}},
		sdk:        sdkClient,
		state:      newLighterRESTState(),
	}
	client.ws = newSpotWebSocket(
		newPublicWebSocket(client.meta, newLighterSpotWSBackend(client.sdk, client.state, settings)),
		newLighterSpotPrivateWSBackend(client.sdk, client.state, settings),
	)
	return client
}

type lighterPerpClient struct {
	*perpClient
	sdk   *lighter.Client
	ws    exchange.PerpWebSocket
	state *lighterRESTState
}

func NewLighterPerp(privateKey string, accountIndex int64, keyIndex uint8, settings Settings) exchange.PerpClient {
	sdkClient := lighter.NewClient().WithCredentials(privateKey, accountIndex, keyIndex)
	if settings.Environment == "testnet" {
		sdkClient.WithEnvironment(lighter.EnvironmentTestnet)
	} else {
		sdkClient.WithEnvironment(lighter.EnvironmentMainnet)
	}
	if settings.Endpoint != "" {
		sdkClient.WithBaseURL(settings.Endpoint)
	}
	sdkClient.HTTPClient = lighterTrackedHTTPClient(settings.HTTPClient)
	client := &lighterPerpClient{
		perpClient: &perpClient{meta: clientMeta{venue: exchange.VenueLighter, product: exchange.ProductPerp}},
		sdk:        sdkClient,
		state:      newLighterRESTState(),
	}
	client.ws = newPerpWebSocket(
		client.meta,
		newLighterPerpWSBackend(client.sdk, client.state, settings),
		newLighterPerpPrivateWSBackend(client.sdk, client.state, settings),
	)
	return client
}

type lighterRESTState struct {
	cacheMu sync.Mutex
	metas   map[string]lighterMarketMeta
	byID    map[int]lighterMarketMeta
	loading chan struct{}
	cmdGate chan struct{}
}

func newLighterRESTState() *lighterRESTState {
	return &lighterRESTState{cmdGate: make(chan struct{}, 1)}
}

type lighterSendTracker struct {
	mu              sync.Mutex
	began           bool
	status          int
	accountIdentity lighterAccountIdentity
}

type lighterAccountIdentityField struct {
	present bool
	valid   bool
	value   int64
}

type lighterAccountIdentity struct {
	observed     bool
	index        lighterAccountIdentityField
	accountIndex lighterAccountIdentityField
}

func (tracker *lighterSendTracker) begin() {
	tracker.mu.Lock()
	tracker.began = true
	tracker.mu.Unlock()
}

func (tracker *lighterSendTracker) setStatus(status int) {
	tracker.mu.Lock()
	tracker.status = status
	tracker.mu.Unlock()
}

func (tracker *lighterSendTracker) snapshot() (bool, int) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return tracker.began, tracker.status
}

func (tracker *lighterSendTracker) setAccountIdentity(identity lighterAccountIdentity) {
	tracker.mu.Lock()
	tracker.accountIdentity = identity
	tracker.mu.Unlock()
}

func (tracker *lighterSendTracker) accountIdentitySnapshot() lighterAccountIdentity {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return tracker.accountIdentity
}

type lighterSendTrackerKey struct{}

type lighterTrackingTransport struct {
	base http.RoundTripper
}

type lighterHTTPStatusError struct {
	status int
}

func (err *lighterHTTPStatusError) Error() string {
	return "lighter HTTP status " + strconv.Itoa(err.status)
}

func lighterTrackedHTTPClient(input *http.Client) *http.Client {
	source := input
	if source == nil {
		source = http.DefaultClient
	}
	clone := *source
	base := clone.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	clone.Transport = lighterTrackingTransport{base: base}
	return &clone
}

func (transport lighterTrackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tracker, _ := req.Context().Value(lighterSendTrackerKey{}).(*lighterSendTracker)
	path := ""
	if req.URL != nil {
		path = req.URL.Path
	}
	if tracker != nil && path == "/api/v1/sendTx" {
		tracker.begin()
	}
	resp, err := transport.base.RoundTrip(req)
	if tracker != nil && resp != nil {
		tracker.setStatus(resp.StatusCode)
	}
	if err != nil || resp == nil {
		return resp, err
	}
	if lighterInterceptHTTPStatus(resp.StatusCode) {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, &lighterHTTPStatusError{status: resp.StatusCode}
	}
	if tracker != nil && path == "/api/v1/account" && resp.Body != nil {
		resp.Body = &lighterAccountIdentityBody{body: resp.Body, tracker: tracker}
	}
	return resp, err
}

func lighterInterceptHTTPStatus(status int) bool {
	return status >= http.StatusBadRequest
}

type lighterAccountIdentityBody struct {
	body      io.ReadCloser
	tracker   *lighterSendTracker
	captured  bytes.Buffer
	finalized bool
}

func (body *lighterAccountIdentityBody) Read(p []byte) (int, error) {
	n, err := body.body.Read(p)
	if n > 0 {
		_, _ = body.captured.Write(p[:n])
	}
	if err == io.EOF {
		body.finalize()
	}
	return n, err
}

func (body *lighterAccountIdentityBody) Close() error {
	return body.body.Close()
}

func (body *lighterAccountIdentityBody) finalize() {
	if body.finalized {
		return
	}
	body.finalized = true
	body.tracker.setAccountIdentity(lighterParseAccountIdentity(body.captured.Bytes()))
}

func lighterParseAccountIdentity(data []byte) lighterAccountIdentity {
	var envelope struct {
		Accounts []json.RawMessage `json:"accounts"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || len(envelope.Accounts) != 1 {
		return lighterAccountIdentity{}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Accounts[0], &fields); err != nil || fields == nil {
		return lighterAccountIdentity{}
	}
	return lighterAccountIdentity{
		observed:     true,
		index:        lighterParseAccountIdentityField(fields, "index"),
		accountIndex: lighterParseAccountIdentityField(fields, "account_index"),
	}
}

func lighterParseAccountIdentityField(fields map[string]json.RawMessage, name string) lighterAccountIdentityField {
	raw, present := fields[name]
	field := lighterAccountIdentityField{present: present}
	if !present || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return field
	}
	if err := json.Unmarshal(raw, &field.value); err == nil {
		field.valid = true
	}
	return field
}

func lighterWithSendTracker(ctx context.Context) (context.Context, *lighterSendTracker) {
	tracker := &lighterSendTracker{}
	return context.WithValue(ctx, lighterSendTrackerKey{}, tracker), tracker
}
