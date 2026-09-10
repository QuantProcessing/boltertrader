package auth_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/gateway/auth"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
)

func TestBearerMiddlewareRequiresStrictAuthorizationSyntax(t *testing.T) {
	tokenRef, err := secret.ParseRef("env://GATEWAY_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	middleware, err := auth.NewBearerMiddleware(context.Background(), secret.StaticProvider{
		tokenRef.RawForProvider(): "correct-token",
	}, tokenRef)
	if err != nil {
		t.Fatalf("NewBearerMiddleware: %v", err)
	}
	if principal := middleware.PrincipalID(); principal == "" || principal == domain.PrincipalID("correct-token") {
		t.Fatalf("safe principal=%q", principal)
	}

	tests := map[string]struct {
		header string
		want   int
	}{
		"missing":        {"", http.StatusUnauthorized},
		"wrong token":    {"Bearer wrong-token", http.StatusUnauthorized},
		"extra space":    {"Bearer  correct-token", http.StatusUnauthorized},
		"tab separator":  {"Bearer\tcorrect-token", http.StatusUnauthorized},
		"newline before": {"Bearer\ncorrect-token", http.StatusUnauthorized},
		"trailing text":  {"Bearer correct-token trailing", http.StatusUnauthorized},
		"valid":          {"Bearer correct-token", http.StatusNoContent},
		"lower scheme":   {"bearer correct-token", http.StatusNoContent},
		"upper scheme":   {"BEARER correct-token", http.StatusNoContent},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/v1/health/live", nil)
			if test.header != "" {
				request.Header.Set("Authorization", test.header)
			}
			recorder := httptest.NewRecorder()

			middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.want == http.StatusNoContent {
					if principal, ok := auth.Principal(r.Context()); !ok || principal == "" {
						t.Fatalf("authenticated request principal=%q ok=%v", principal, ok)
					}
				}
				w.WriteHeader(http.StatusNoContent)
			})).ServeHTTP(recorder, request)

			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, test.want, recorder.Body.String())
			}
		})
	}
}

func TestBearerMiddlewareUnauthorizedResponseNeverLeaksTokenCanary(t *testing.T) {
	canary := "tok-{json:\"value\"}\\n%E2%98%83 dG9rZW4="
	tokenRef, err := secret.ParseRef("env://GATEWAY_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	middleware, err := auth.NewBearerMiddleware(context.Background(), secret.StaticProvider{
		tokenRef.RawForProvider(): canary,
	}, tokenRef)
	if err != nil {
		t.Fatalf("NewBearerMiddleware: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/accounts", nil)
	request.Header.Set("Authorization", "Bearer wrong")
	recorder := httptest.NewRecorder()

	middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not run for unauthorized requests")
	})).ServeHTTP(recorder, request)

	body := recorder.Body.String()
	formatted := fmt.Sprintf("%v %#v", middleware, middleware)
	payload, _ := json.Marshal(map[string]string{"body": body, "formatted": formatted})
	for _, haystack := range []string{body, formatted, string(payload)} {
		if strings.Contains(haystack, canary) || strings.Contains(haystack, "\\n") && strings.Contains(haystack, "tok-") {
			t.Fatalf("unauthorized output leaked canary: %q", haystack)
		}
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if got := recorder.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("WWW-Authenticate = %q, want Bearer", got)
	}
	var envelope map[string]map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	errorBody := envelope["error"]
	if errorBody["code"] != "unauthorized" || errorBody["message"] != "unauthorized" ||
		errorBody["retryable"] != false {
		t.Fatalf("unauthorized envelope=%v", envelope)
	}
	correlationID, _ := errorBody["correlation_id"].(string)
	if err := domain.EventID(correlationID).Validate(); err != nil {
		t.Fatalf("unauthorized correlation_id=%q: %v", correlationID, err)
	}
}

func TestOwnerIdentitySurvivesServiceTokenReplacement(t *testing.T) {
	ref, err := secret.ParseRef("env://GATEWAY_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	first, err := auth.NewBearerMiddleware(t.Context(), secret.StaticProvider{ref.RawForProvider(): "old-offline-token"}, ref)
	if err != nil {
		t.Fatal(err)
	}
	next, err := auth.NewBearerMiddleware(t.Context(), secret.StaticProvider{ref.RawForProvider(): "new-offline-token"}, ref)
	if err != nil {
		t.Fatal(err)
	}
	if first.PrincipalID() != next.PrincipalID() || first.PrincipalID() != "gateway-owner" {
		t.Fatal("service secret changed durable ownership")
	}
	handler := next.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, headers := range [][]string{{"Bearer old-offline-token"}, {"Bearer new-offline-token", "Bearer new-offline-token"}, {"Bearer new-offline-token"}} {
		request := httptest.NewRequest("GET", "/v1/accounts", nil)
		for _, header := range headers {
			request.Header.Add("Authorization", header)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := 401
		if len(headers) == 1 && headers[0] == "Bearer new-offline-token" {
			want = 204
		}
		if response.Code != want {
			t.Fatalf("status=%d want=%d", response.Code, want)
		}
	}
}
