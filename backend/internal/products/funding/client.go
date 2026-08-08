package funding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	fundingpolicy "github.com/QuantProcessing/boltertrader/backend/internal/products/funding/policy"
	"github.com/shopspring/decimal"
)

var forbiddenEnvironment = []string{
	"BT_GATEWAY_URL", "BT_GATEWAY_TOKEN", "BT_GATEWAY_TOKEN_FILE", "GATEWAY_AUTH_TOKEN_FILE", "BT_LIVE_PRIVATE_WRITES_ENABLED",
	"DATABASE_URL", "DB_DSN", "BT_SECRET_MASTER_KEY_FILE", "TRADING_SECRET", "TRADING_SECRET_REF",
}

type Config struct {
	BaseURL         string
	Token           string
	WorkerID        string
	InstanceID      string
	Mode            string
	ContractVersion string
	StateFile       string
	LiveEnabled     bool
	LiveMaxNotional string
	HTTPClient      *http.Client
}

type Client struct {
	baseURL         string
	token           string
	workerID        string
	instanceID      string
	mode            string
	contractVersion string
	httpClient      *http.Client
}

// HTTPError is the typed representation of the frozen Platform work error
// envelope. It intentionally excludes request bodies, tokens, and response
// bodies so callers can log it without leaking worker credentials.
type HTTPError struct {
	StatusCode int
	Code       string
	Message    string
	Retryable  bool
	RequestID  string
	Details    map[string]any
}

func (err *HTTPError) Error() string {
	if err == nil {
		return "<nil>"
	}
	if err.Code != "" {
		return fmt.Sprintf("platform work returned %d %s (request %s)", err.StatusCode, err.Code, err.RequestID)
	}
	return fmt.Sprintf("platform work returned %d", err.StatusCode)
}

type Claim struct {
	WorkItemID      string          `json:"workItemId"`
	WorkKind        string          `json:"workKind"`
	LineageID       string          `json:"lineageId"`
	ProductID       string          `json:"productId"`
	Mode            string          `json:"mode"`
	WorkRevision    int             `json:"workRevision"`
	LeaseID         string          `json:"leaseId"`
	LeaseGeneration int             `json:"leaseGeneration"`
	ExpiresAt       time.Time       `json:"expiresAt"`
	FactsCursor     string          `json:"factsCursor"`
	AccountFences   json.RawMessage `json:"accountFences,omitempty"`
	Payload         json.RawMessage `json:"payload,omitempty"`
	Attempt         int             `json:"attempt"`
}

type ClaimPage struct {
	WorkItem   *WorkItem `json:"workItem"`
	Lease      *Lease    `json:"lease"`
	NextCursor string    `json:"nextCursor"`
}

type WorkItem struct {
	WorkItemID    string          `json:"workItemId"`
	WorkKind      string          `json:"workKind"`
	LineageID     string          `json:"lineageId"`
	ProductID     string          `json:"productId"`
	Mode          string          `json:"mode"`
	WorkRevision  int             `json:"workRevision"`
	FactsCursor   string          `json:"factsCursor"`
	AccountFences json.RawMessage `json:"accountFences"`
	Payload       json.RawMessage `json:"payload"`
	Attempt       int             `json:"attempt"`
}

type Lease struct {
	LeaseID         string    `json:"leaseId"`
	LeaseGeneration int       `json:"leaseGeneration"`
	ExpiresAt       time.Time `json:"expiresAt"`
}

func (page ClaimPage) Claim() (Claim, bool) {
	if page.WorkItem == nil || page.Lease == nil {
		return Claim{}, false
	}
	return Claim{
		WorkItemID: page.WorkItem.WorkItemID, WorkKind: page.WorkItem.WorkKind, LineageID: page.WorkItem.LineageID,
		ProductID: page.WorkItem.ProductID, Mode: page.WorkItem.Mode, WorkRevision: page.WorkItem.WorkRevision,
		LeaseID: page.Lease.LeaseID, LeaseGeneration: page.Lease.LeaseGeneration,
		ExpiresAt:   page.Lease.ExpiresAt,
		FactsCursor: page.WorkItem.FactsCursor, AccountFences: page.WorkItem.AccountFences,
		Payload: page.WorkItem.Payload, Attempt: page.WorkItem.Attempt,
	}, true
}

type Registration struct {
	WorkerGeneration       int      `json:"workerGeneration"`
	Generation             int      `json:"generation"`
	State                  string   `json:"state"`
	Draining               bool     `json:"draining"`
	ActiveLeaseIDs         []string `json:"activeLeaseIds,omitempty"`
	LastAppliedFactsCursor string   `json:"lastAppliedFactsCursor,omitempty"`
}

func (registration Registration) generation() int {
	if registration.WorkerGeneration != 0 {
		return registration.WorkerGeneration
	}
	return registration.Generation
}

type Readiness struct {
	WorkerGeneration int    `json:"workerGeneration"`
	ContractVersion  string `json:"contractVersion"`
	CanClaim         bool   `json:"canClaim"`
	FreshHeartbeat   bool   `json:"freshHeartbeat"`
	Draining         bool   `json:"draining"`
	State            string `json:"state"`
}

type HeartbeatOptions struct {
	WorkerGeneration       int
	State                  string
	ActiveLeaseIDs         []string
	LastAppliedFactsCursor string
}

type LeaseRef struct {
	WorkerGeneration int
	WorkItemID       string
	WorkRevision     int
	LeaseID          string
	LeaseGeneration  int
}

type Fact struct {
	FactID      string          `json:"factId"`
	Cursor      string          `json:"cursor"`
	AccountID   string          `json:"accountId"`
	ProductID   string          `json:"productId"`
	Mode        string          `json:"mode"`
	Kind        string          `json:"kind"`
	Payload     json.RawMessage `json:"payload"`
	EffectiveAt time.Time       `json:"effectiveAt"`
}

type FactsPage struct {
	Facts              []Fact    `json:"facts"`
	NextCursor         string    `json:"nextCursor"`
	ResnapshotRequired bool      `json:"resnapshotRequired"`
	AsOf               time.Time `json:"asOf"`
}

type LeaseResult struct {
	LeaseID         string    `json:"leaseId"`
	WorkItemID      string    `json:"workItemId"`
	LeaseGeneration int       `json:"leaseGeneration"`
	ExpiresAt       time.Time `json:"expiresAt"`
	Accepted        bool      `json:"accepted,omitempty"`
	Completed       bool      `json:"completed,omitempty"`
	Failed          bool      `json:"failed,omitempty"`
}

type IntentOptions struct {
	LeaseRef
	IdempotencyKey string
	IntentKind     string
	FactsCursor    string
	AccountFences  json.RawMessage
	Payload        json.RawMessage
}

type CompleteOptions struct {
	LeaseRef
	IdempotencyKey string
	FactsCursor    string
	Result         json.RawMessage
}

type FailOptions struct {
	LeaseRef
	IdempotencyKey string
	FactsCursor    string
	ErrorCode      string
	Retryable      bool
	Details        json.RawMessage
}

type ClaimOptions struct {
	WorkerGeneration int
	IdempotencyKey   string
	AfterCursor      string
	WaitSeconds      int
	MaxItems         int
}

type DrainOptions struct {
	WorkerGeneration int
	IdempotencyKey   string
	Reason           string
}

func ConfigFromEnvironment() (Config, error) {
	tokenFile := strings.TrimSpace(os.Getenv("BT_FUNDING_WORKER_TOKEN_FILE"))
	if tokenFile == "" {
		return Config{}, fmt.Errorf("BT_FUNDING_WORKER_TOKEN_FILE is required")
	}
	token, err := os.ReadFile(tokenFile)
	if err != nil {
		return Config{}, fmt.Errorf("read worker token: %w", err)
	}
	stateFile := strings.TrimSpace(os.Getenv("BT_FUNDING_WORKER_STATE_FILE"))
	if stateFile == "" {
		stateFile = tokenFile + ".state"
	}
	mode := strings.TrimSpace(os.Getenv("FT_MODE"))
	liveEnabled, err := parseFundingLiveEnabled(os.Getenv("BT_FUNDING_LIVE_ENABLED"))
	if err != nil {
		return Config{}, err
	}
	workerID := strings.TrimSpace(os.Getenv("BT_FUNDING_WORKER_ID"))
	if workerID == "" && (mode == "test" || mode == "live") {
		workerID = "funding-default-" + mode
	}
	return Config{
		BaseURL:         strings.TrimSpace(os.Getenv("BT_PLATFORM_WORK_URL")),
		Token:           strings.TrimSpace(string(token)),
		WorkerID:        workerID,
		InstanceID:      strings.TrimSpace(os.Getenv("BT_FUNDING_WORKER_INSTANCE_ID")),
		Mode:            mode,
		ContractVersion: strings.TrimSpace(os.Getenv("BT_FUNDING_WORK_CONTRACT_VERSION")),
		StateFile:       stateFile,
		LiveEnabled:     liveEnabled,
		LiveMaxNotional: strings.TrimSpace(os.Getenv("BT_FUNDING_LIVE_MAX_NOTIONAL")),
	}, nil
}

func parseFundingLiveEnabled(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, fmt.Errorf("BT_FUNDING_LIVE_ENABLED must be true or false")
	}
}

func (config Config) Admission() (fundingpolicy.Admission, error) {
	maximum := decimal.Zero
	if config.LiveEnabled {
		if config.Mode != "live" {
			return fundingpolicy.Admission{}, fmt.Errorf("BT_FUNDING_LIVE_ENABLED is valid only when FT_MODE=live")
		}
		var err error
		maximum, err = decimal.NewFromString(config.LiveMaxNotional)
		if err != nil || !maximum.IsPositive() {
			return fundingpolicy.Admission{}, fmt.Errorf("BT_FUNDING_LIVE_MAX_NOTIONAL must be a positive decimal when Funding Live is enabled")
		}
	}
	return fundingpolicy.NewAdmission(config.LiveEnabled, maximum)
}

func NewClient(config Config) (*Client, error) {
	for _, key := range forbiddenEnvironment {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return nil, fmt.Errorf("forbidden worker environment %s is set", key)
		}
	}
	base, err := url.Parse(strings.TrimSpace(config.BaseURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("platform work URL is required")
	}
	if path := strings.TrimRight(base.Path, "/"); path != "" && path != frozenWorkBasePath {
		return nil, fmt.Errorf("platform work URL must be an origin without path, query, fragment, or user info")
	}
	if base.RawQuery != "" || base.Fragment != "" || base.User != nil {
		return nil, fmt.Errorf("platform work URL must be an origin without path, query, fragment, or user info")
	}
	base.Path = ""
	if strings.TrimSpace(config.Token) == "" || strings.TrimSpace(config.WorkerID) == "" || strings.TrimSpace(config.InstanceID) == "" {
		return nil, fmt.Errorf("worker identity and token are required")
	}
	if config.ContractVersion != workContractVersion {
		return nil, fmt.Errorf("contract version mismatch")
	}
	if config.Mode != "test" && config.Mode != "live" {
		return nil, fmt.Errorf("invalid mode")
	}
	hc := config.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{baseURL: strings.TrimRight(base.String(), "/"), token: config.Token, workerID: config.WorkerID, instanceID: config.InstanceID, mode: config.Mode, contractVersion: config.ContractVersion, httpClient: hc}, nil
}

func (client *Client) Register(ctx context.Context) (int, error) {
	var response struct {
		Data Registration `json:"data"`
	}
	idempotencyKey := "register:" + client.workerID + ":" + client.instanceID
	err := client.post(ctx, "/internal/v1/funding-work/workers/registrations", map[string]any{
		"workerId": client.workerID, "instanceId": client.instanceID, "productId": "funding", "mode": client.mode, "contractVersion": client.contractVersion,
	}, http.StatusCreated, &response, requestOptions{idempotencyKey: idempotencyKey})
	return response.Data.generation(), err
}

func (client *Client) Heartbeat(ctx context.Context, options HeartbeatOptions) (Registration, error) {
	if options.WorkerGeneration <= 0 {
		return Registration{}, fmt.Errorf("worker generation is required")
	}
	if options.State == "" {
		return Registration{}, fmt.Errorf("worker state is required")
	}
	var response struct {
		Data Registration `json:"data"`
	}
	err := client.requestJSON(ctx, http.MethodPut, "/internal/v1/funding-work/workers/"+url.PathEscape(client.workerID)+"/heartbeat", map[string]any{
		"workerGeneration": options.WorkerGeneration, "instanceId": client.instanceID, "state": options.State,
		"activeLeaseIds": options.ActiveLeaseIDs, "lastAppliedFactsCursor": options.LastAppliedFactsCursor,
	}, http.StatusOK, &response, requestOptions{workerGeneration: options.WorkerGeneration})
	return response.Data, err
}

func (client *Client) Readiness(ctx context.Context, workerGeneration int) (Readiness, error) {
	if workerGeneration <= 0 {
		return Readiness{}, fmt.Errorf("worker generation is required")
	}
	var response struct {
		Data Readiness `json:"data"`
	}
	err := client.requestJSON(ctx, http.MethodGet, "/internal/v1/funding-work/workers/"+url.PathEscape(client.workerID)+"/readiness", nil, http.StatusOK, &response, requestOptions{workerGeneration: workerGeneration})
	return response.Data, err
}

func (client *Client) Claim(ctx context.Context, options ClaimOptions) ([]Claim, error) {
	page, err := client.ClaimPage(ctx, options)
	if err != nil {
		return nil, err
	}
	claim, ok := page.Claim()
	if !ok {
		return []Claim{}, nil
	}
	return []Claim{claim}, nil
}

func (client *Client) ClaimPage(ctx context.Context, options ClaimOptions) (ClaimPage, error) {
	if options.WorkerGeneration <= 0 {
		return ClaimPage{}, fmt.Errorf("worker generation is required")
	}
	if strings.TrimSpace(options.IdempotencyKey) == "" {
		return ClaimPage{}, fmt.Errorf("idempotency key is required")
	}
	if options.MaxItems != 1 {
		return ClaimPage{}, fmt.Errorf("max items must equal 1")
	}
	if options.WaitSeconds < 0 || options.WaitSeconds > 20 {
		return ClaimPage{}, fmt.Errorf("wait seconds must be between 0 and 20")
	}
	var response struct {
		Data ClaimPage `json:"data"`
	}
	err := client.post(ctx, "/internal/v1/funding-work/claims", map[string]any{
		"workerId": client.workerID, "workerGeneration": options.WorkerGeneration, "instanceId": client.instanceID,
		"idempotencyKey": options.IdempotencyKey, "afterCursor": options.AfterCursor, "waitSeconds": options.WaitSeconds, "maxItems": options.MaxItems,
	}, http.StatusOK, &response, requestOptions{idempotencyKey: options.IdempotencyKey, workerGeneration: options.WorkerGeneration})
	if err == nil && (response.Data.WorkItem == nil) != (response.Data.Lease == nil) {
		return ClaimPage{}, fmt.Errorf("platform work returned an incomplete claim page")
	}
	if err == nil && response.Data.Lease != nil && response.Data.Lease.ExpiresAt.IsZero() {
		return ClaimPage{}, fmt.Errorf("platform work returned a claim without lease expiry")
	}
	return response.Data, err
}

func (client *Client) Drain(ctx context.Context, options DrainOptions) error {
	if options.WorkerGeneration <= 0 {
		return fmt.Errorf("worker generation is required")
	}
	if strings.TrimSpace(options.IdempotencyKey) == "" {
		return fmt.Errorf("idempotency key is required")
	}
	return client.post(ctx, "/internal/v1/funding-work/workers/"+url.PathEscape(client.workerID)+"/drain", map[string]any{
		"workerGeneration": options.WorkerGeneration, "instanceId": client.instanceID, "idempotencyKey": options.IdempotencyKey, "reason": options.Reason,
	}, http.StatusAccepted, nil, requestOptions{idempotencyKey: options.IdempotencyKey, workerGeneration: options.WorkerGeneration})
}

func (client *Client) Facts(ctx context.Context, lease LeaseRef, afterCursor string, limit int) (FactsPage, error) {
	if err := validateLeaseRef(lease); err != nil {
		return FactsPage{}, err
	}
	if limit <= 0 || limit > 500 {
		return FactsPage{}, fmt.Errorf("facts limit must be between 1 and 500")
	}
	query := leaseQuery(lease)
	query.Set("afterCursor", afterCursor)
	query.Set("limit", fmt.Sprintf("%d", limit))
	var response struct {
		Data FactsPage `json:"data"`
	}
	err := client.requestJSON(ctx, http.MethodGet, "/internal/v1/funding-work/facts?"+query.Encode(), nil, http.StatusOK, &response, requestOptions{workerGeneration: lease.WorkerGeneration})
	return response.Data, err
}

func (client *Client) Snapshot(ctx context.Context, lease LeaseRef) (FactsPage, error) {
	if err := validateLeaseRef(lease); err != nil {
		return FactsPage{}, err
	}
	var response struct {
		Data FactsPage `json:"data"`
	}
	err := client.requestJSON(ctx, http.MethodGet, "/internal/v1/funding-work/facts/snapshot?"+leaseQuery(lease).Encode(), nil, http.StatusOK, &response, requestOptions{workerGeneration: lease.WorkerGeneration})
	return response.Data, err
}

func (client *Client) Renew(ctx context.Context, lease LeaseRef, idempotencyKey string) (LeaseResult, error) {
	return client.leasePost(ctx, lease, "renew", idempotencyKey, nil)
}

func (client *Client) CommandIntent(ctx context.Context, options IntentOptions) (LeaseResult, error) {
	if options.IntentKind == "" || len(options.AccountFences) == 0 || len(options.Payload) == 0 {
		return LeaseResult{}, fmt.Errorf("intent kind, account fences, and payload are required")
	}
	extra := map[string]any{"intentKind": options.IntentKind, "factsCursor": options.FactsCursor, "accountFences": options.AccountFences, "payload": options.Payload}
	return client.leasePost(ctx, options.LeaseRef, "command-intents", options.IdempotencyKey, extra)
}

func (client *Client) Complete(ctx context.Context, options CompleteOptions) (LeaseResult, error) {
	if len(options.Result) == 0 {
		return LeaseResult{}, fmt.Errorf("completion result is required")
	}
	return client.leasePost(ctx, options.LeaseRef, "complete", options.IdempotencyKey, map[string]any{"factsCursor": options.FactsCursor, "result": options.Result})
}

func (client *Client) Fail(ctx context.Context, options FailOptions) (LeaseResult, error) {
	if options.ErrorCode == "" || len(options.Details) == 0 {
		return LeaseResult{}, fmt.Errorf("failure code and details are required")
	}
	return client.leasePost(ctx, options.LeaseRef, "fail", options.IdempotencyKey, map[string]any{"factsCursor": options.FactsCursor, "errorCode": options.ErrorCode, "retryable": options.Retryable, "details": options.Details})
}

func (client *Client) leasePost(ctx context.Context, lease LeaseRef, action, idempotencyKey string, extra map[string]any) (LeaseResult, error) {
	if err := validateLeaseRef(lease); err != nil {
		return LeaseResult{}, err
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return LeaseResult{}, fmt.Errorf("idempotency key is required")
	}
	payload := map[string]any{"workerId": client.workerID, "workerGeneration": lease.WorkerGeneration, "workItemId": lease.WorkItemID, "workRevision": lease.WorkRevision, "leaseGeneration": lease.LeaseGeneration, "idempotencyKey": idempotencyKey}
	for key, value := range extra {
		payload[key] = value
	}
	var response struct {
		Data LeaseResult `json:"data"`
	}
	err := client.post(ctx, "/internal/v1/funding-work/leases/"+url.PathEscape(lease.LeaseID)+"/"+action, payload, http.StatusOK, &response, requestOptions{idempotencyKey: idempotencyKey, workerGeneration: lease.WorkerGeneration})
	return response.Data, err
}

func validateLeaseRef(lease LeaseRef) error {
	if lease.WorkerGeneration <= 0 || lease.WorkRevision <= 0 || lease.LeaseGeneration <= 0 || lease.WorkItemID == "" || lease.LeaseID == "" {
		return fmt.Errorf("complete lease fence is required")
	}
	return nil
}

func leaseQuery(lease LeaseRef) url.Values {
	query := make(url.Values)
	query.Set("leaseId", lease.LeaseID)
	query.Set("leaseGeneration", fmt.Sprintf("%d", lease.LeaseGeneration))
	query.Set("workItemId", lease.WorkItemID)
	query.Set("workRevision", fmt.Sprintf("%d", lease.WorkRevision))
	return query
}

const workContractVersion = "boltertrader-product-platform-upgrade/v4"
const frozenWorkBasePath = "/internal/v1/funding-work"

type requestOptions struct {
	idempotencyKey   string
	workerGeneration int
}

func (client *Client) post(ctx context.Context, path string, payload any, want int, target any, options requestOptions) error {
	return client.requestJSON(ctx, http.MethodPost, path, payload, want, target, options)
}

func (client *Client) requestJSON(ctx context.Context, method, path string, payload any, want int, target any, options requestOptions) error {
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if options.idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", options.idempotencyKey)
	}
	if options.workerGeneration > 0 {
		request.Header.Set("X-Worker-Id", client.workerID)
		request.Header.Set("X-Worker-Generation", fmt.Sprintf("%d", options.workerGeneration))
		request.Header.Set("X-Worker-Instance-Id", client.instanceID)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		return decodeHTTPError(response)
	}
	if target != nil {
		return json.NewDecoder(response.Body).Decode(target)
	}
	return nil
}

func decodeHTTPError(response *http.Response) error {
	var envelope struct {
		Error struct {
			Code      string         `json:"code"`
			Message   string         `json:"message"`
			Details   map[string]any `json:"details"`
			Retryable bool           `json:"retryable"`
			RequestID string         `json:"requestId"`
		} `json:"error"`
	}
	_ = json.NewDecoder(response.Body).Decode(&envelope)
	return &HTTPError{
		StatusCode: response.StatusCode,
		Code:       envelope.Error.Code,
		Message:    envelope.Error.Message,
		Retryable:  envelope.Error.Retryable,
		RequestID:  envelope.Error.RequestID,
		Details:    envelope.Error.Details,
	}
}
