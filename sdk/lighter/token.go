package lighter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// GetTokens lists all active tokens for the account
// Notice: This api not return api_token filed, what a shit......
func (c *Client) GetTokens(ctx context.Context) ([]Token, error) {
	path := fmt.Sprintf("/api/v1/tokens?account_index=%d", c.AccountIndex)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}

	// Add Auth
	token, err := c.CreateAuthToken(time.Now().Add(time.Minute * 10))
	if err != nil {
		return nil, fmt.Errorf("failed to create auth token: %w", err)
	}
	req.Header.Set("Authorization", token)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var res TokenListResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	if res.Code != 200 {
		return nil, fmt.Errorf("get tokens failed: %s", res.Msg)
	}
	return res.ApiTokens, nil
}

// CreateToken creates a new API token
func (c *Client) CreateToken(ctx context.Context, name, permission string, expiration int64) (string, error) {
	params := map[string]string{
		"name":               name,
		"account_index":      fmt.Sprintf("%d", c.AccountIndex),
		"expiry":             fmt.Sprintf("%d", expiration),
		"sub_account_access": "true",
		"scopes":             permission,
	}

	data, err := c.PostForm(ctx, "/api/v1/tokens/create", params, true)
	if err != nil {
		return "", err
	}
	var res CreateTokenResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return "", err
	}
	if res.Code != 200 {
		return "", fmt.Errorf("create token failed: %s", res.Msg)
	}
	return res.ApiToken, nil
}

// RevokeToken revokes an API token
func (c *Client) RevokeToken(ctx context.Context, tokenId int64) error {
	req := RevokeTokenRequest{
		TokenId:      tokenId,
		AccountIndex: c.AccountIndex,
	}
	data, err := c.Post(ctx, "/api/v1/tokens/revoke", req, true)
	if err != nil {
		return err
	}
	var res RevokeTokenResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return err
	}
	if res.Code != 200 {
		return fmt.Errorf("revoke token failed: %s", res.Msg)
	}
	return nil
}
