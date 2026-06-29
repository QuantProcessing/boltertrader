package okx

import (
	"net/http"
	"testing"
)

func TestSigner_SignRequest(t *testing.T) {
	apiKey := "test_api_key"
	secretKey := "test_secret_key"
	passphrase := "test_passphrase"
	signer := NewSigner(secretKey)

	req, _ := http.NewRequest("GET", "https://www.okx.com/api/v5/account/balance", nil)
	method := "GET"
	path := "/api/v5/account/balance"
	body := ""

	signer.SignRequest(req, method, path, body, apiKey, passphrase)

	if req.Header.Get("OK-ACCESS-KEY") != apiKey {
		t.Errorf("Expected OK-ACCESS-KEY %s, got %s", apiKey, req.Header.Get("OK-ACCESS-KEY"))
	}
	if req.Header.Get("OK-ACCESS-PASSPHRASE") != passphrase {
		t.Errorf("Expected OK-ACCESS-PASSPHRASE %s, got %s", passphrase, req.Header.Get("OK-ACCESS-PASSPHRASE"))
	}
	if req.Header.Get("OK-ACCESS-SIGN") == "" {
		t.Error("Expected OK-ACCESS-SIGN to be present")
	}
	if req.Header.Get("OK-ACCESS-TIMESTAMP") == "" {
		t.Error("Expected OK-ACCESS-TIMESTAMP to be present")
	}
}
