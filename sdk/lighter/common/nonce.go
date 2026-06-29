package common

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

type nonceManager struct {
	HttpClient   *http.Client
	baseURL      string
	accountIndex int64
	apiKeyIndex  uint8
}

// Fetch queries the next nonce from the Lighter API for the configured account/apiKey indices.
func (n *nonceManager) Fetch(ctx context.Context) (int64, error) {
	u, err := url.Parse(n.baseURL + "/api/v1/nextNonce")
	if err != nil {
		return 0, err
	}
	q := u.Query()
	q.Set("account_index", fmt.Sprintf("%d", n.accountIndex))
	q.Set("api_key_index", fmt.Sprintf("%d", n.apiKeyIndex))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")

	client := n.HttpClient
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	var body struct {
		Code  int    `json:"code"`
		Msg   string `json:"message"`
		Nonce int64  `json:"nonce"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return 0, err
	}
	if body.Code != 200 {
		return 0, fmt.Errorf("failed to get next nonce. code: %d, msg: %s", body.Code, body.Msg)
	}
	return body.Nonce, nil
}

type NonceManager interface {
	Fetch(ctx context.Context) (int64, error)
}

func NewNonceManager(baseURL string, accountIndex int64, apiKeyIndex uint8) (NonceManager, error) {
	return &nonceManager{
		HttpClient:   http.DefaultClient,
		baseURL:      baseURL,
		accountIndex: accountIndex,
		apiKeyIndex:  apiKeyIndex,
	}, nil
}
