package spot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	hyperliquid "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
)

func (c *Client) SubmitSplitOutcome(ctx context.Context, outcome int, amount string) (string, error) {
	if outcome < 0 {
		return "", fmt.Errorf("submit split outcome failed: outcome must be non-negative")
	}
	if strings.TrimSpace(amount) == "" {
		return "", fmt.Errorf("submit split outcome failed: amount is required")
	}
	action := hyperliquid.UserOutcomeAction{
		Type:         "userOutcome",
		SplitOutcome: &hyperliquid.OutcomeSplitAction{Outcome: outcome, Amount: amount},
	}
	return c.submitOutcomeAction(ctx, action, "submit split outcome")
}

func (c *Client) SubmitMergeOutcome(ctx context.Context, outcome int, amount *string) (string, error) {
	if outcome < 0 {
		return "", fmt.Errorf("submit merge outcome failed: outcome must be non-negative")
	}
	if amount != nil && strings.TrimSpace(*amount) == "" {
		return "", fmt.Errorf("submit merge outcome failed: amount cannot be empty")
	}
	action := hyperliquid.UserOutcomeAction{
		Type:         "userOutcome",
		MergeOutcome: &hyperliquid.OutcomeMergeAction{Outcome: outcome, Amount: amount},
	}
	return c.submitOutcomeAction(ctx, action, "submit merge outcome")
}

func (c *Client) SubmitMergeQuestion(ctx context.Context, question int, amount *string) (string, error) {
	if question < 0 {
		return "", fmt.Errorf("submit merge question failed: question must be non-negative")
	}
	if amount != nil && strings.TrimSpace(*amount) == "" {
		return "", fmt.Errorf("submit merge question failed: amount cannot be empty")
	}
	action := hyperliquid.UserOutcomeAction{
		Type:          "userOutcome",
		MergeQuestion: &hyperliquid.OutcomeMergeQuestionAction{Question: question, Amount: amount},
	}
	return c.submitOutcomeAction(ctx, action, "submit merge question")
}

func (c *Client) SubmitNegateOutcome(ctx context.Context, question int, outcome int, amount string) (string, error) {
	if question < 0 {
		return "", fmt.Errorf("submit negate outcome failed: question must be non-negative")
	}
	if outcome < 0 {
		return "", fmt.Errorf("submit negate outcome failed: outcome must be non-negative")
	}
	if strings.TrimSpace(amount) == "" {
		return "", fmt.Errorf("submit negate outcome failed: amount is required")
	}
	action := hyperliquid.UserOutcomeAction{
		Type:          "userOutcome",
		NegateOutcome: &hyperliquid.OutcomeNegateAction{Question: question, Outcome: outcome, Amount: amount},
	}
	return c.submitOutcomeAction(ctx, action, "submit negate outcome")
}

func (c *Client) submitOutcomeAction(ctx context.Context, action hyperliquid.UserOutcomeAction, op string) (string, error) {
	if c.PrivateKey == nil {
		return "", hyperliquid.ErrCredentialsRequired
	}
	nonce := c.GetNextNonce()
	sig, err := c.SignL1Action(action, nonce)
	if err != nil {
		return "", err
	}
	data, err := c.PostAction(ctx, action, sig, nonce)
	if err != nil {
		return "", err
	}
	return parseOutcomeActionResult(data, op)
}

func parseOutcomeActionResult(data []byte, op string) (string, error) {
	var res struct {
		Status   string `json:"status"`
		Response *struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return "", err
	}
	if res.Status != "ok" {
		return "", fmt.Errorf("%s failed: %s", op, res.Status)
	}
	if res.Response == nil {
		return res.Status, nil
	}
	if len(res.Response.Data) > 0 && string(res.Response.Data) != "null" {
		var fields map[string]any
		if err := json.Unmarshal(res.Response.Data, &fields); err == nil {
			if message, ok := fields["error"].(string); ok && message != "" {
				return "", fmt.Errorf("%s failed: %s", op, message)
			}
			for _, key := range []string{"hash", "txHash", "status"} {
				if value, ok := fields[key].(string); ok && value != "" {
					return value, nil
				}
			}
		}
		var value string
		if err := json.Unmarshal(res.Response.Data, &value); err == nil && value != "" {
			return value, nil
		}
	}
	if res.Response.Type != "" {
		return res.Response.Type, nil
	}
	return res.Status, nil
}
