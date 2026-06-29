package sdk

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func buildSigningPayload(instruction string, params map[string]string, timestamp, window int64) string {
	parts := []string{fmt.Sprintf("instruction=%s", instruction)}
	params = filterEmptyParams(params)

	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", key, params[key]))
	}

	parts = append(parts, fmt.Sprintf("timestamp=%d", timestamp))
	parts = append(parts, fmt.Sprintf("window=%d", window))

	return strings.Join(parts, "&")
}

func buildBatchSigningPayload(instruction string, batchParams []map[string]string, timestamp, window int64) string {
	parts := make([]string, 0)
	for _, params := range batchParams {
		parts = append(parts, fmt.Sprintf("instruction=%s", instruction))
		params = filterEmptyParams(params)

		keys := make([]string, 0, len(params))
		for key := range params {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		for _, key := range keys {
			parts = append(parts, fmt.Sprintf("%s=%s", key, params[key]))
		}
	}

	parts = append(parts, fmt.Sprintf("timestamp=%d", timestamp))
	parts = append(parts, fmt.Sprintf("window=%d", window))

	return strings.Join(parts, "&")
}

func filterEmptyParams(params map[string]string) map[string]string {
	if len(params) == 0 {
		return nil
	}
	filtered := make(map[string]string, len(params))
	for key, value := range params {
		if value == "" {
			continue
		}
		filtered[key] = value
	}
	return filtered
}

func signPayload(seedBase64, payload string) (string, error) {
	seed, err := base64.StdEncoding.DecodeString(seedBase64)
	if err != nil {
		return "", err
	}
	if len(seed) != ed25519.SeedSize {
		return "", fmt.Errorf("invalid ed25519 seed length: %d", len(seed))
	}
	sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), []byte(payload))
	return base64.StdEncoding.EncodeToString(sig), nil
}

func buildSignedHeaders(apiKey, seedBase64, instruction string, params map[string]string, timestamp, window int64) (map[string]string, error) {
	payload := buildSigningPayload(instruction, params, timestamp, window)
	signature, err := signPayload(seedBase64, payload)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"X-API-Key":   apiKey,
		"X-Signature": signature,
		"X-Timestamp": strconv.FormatInt(timestamp, 10),
		"X-Window":    strconv.FormatInt(window, 10),
	}, nil
}

func buildBatchSignedHeaders(apiKey, seedBase64, instruction string, params []map[string]string, timestamp, window int64) (map[string]string, error) {
	payload := buildBatchSigningPayload(instruction, params, timestamp, window)
	signature, err := signPayload(seedBase64, payload)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"X-API-Key":   apiKey,
		"X-Signature": signature,
		"X-Timestamp": strconv.FormatInt(timestamp, 10),
		"X-Window":    strconv.FormatInt(window, 10),
	}, nil
}
