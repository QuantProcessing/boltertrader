package spot

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
)

func TestSpotClientErrorsRedactCallerEndpointQuery(t *testing.T) {
	const (
		safePath = "/api/v3/depth"
		secret   = "SENTINEL_ASTER_SPOT_ENDPOINT_TOKEN_2d91"
	)
	endpoint := safePath + "?accessToken=" + secret
	transportCause := errors.New("sentinel transport cause")
	tests := []struct {
		name      string
		transport roundTripFunc
		wantCause bool
	}{
		{
			name: "transport",
			transport: func(*http.Request) (*http.Response, error) {
				return nil, transportCause
			},
			wantCause: true,
		},
		{
			name: "venue status",
			transport: func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusUnauthorized,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"code":-1000,"msg":"invalid request"}`)),
					Request:    request,
				}, nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile, err := astercommon.NewProfile(astercommon.EnvironmentTestnet, astercommon.ProductSpot)
			if err != nil {
				t.Fatal(err)
			}
			client, err := NewClient(profile, nil)
			if err != nil {
				t.Fatal(err)
			}
			client.WithHTTPClient(&http.Client{Transport: tt.transport})

			err = client.Get(context.Background(), endpoint, nil, false, nil)
			if err == nil {
				t.Fatal("request returned nil error")
			}
			if tt.wantCause && !errors.Is(err, transportCause) {
				t.Fatalf("transport classification lost: %v", err)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "accessToken") {
				t.Fatalf("error leaked caller endpoint query: %v", err)
			}
			if !strings.Contains(err.Error(), safePath) {
				t.Fatalf("error lost safe path context: %v", err)
			}
		})
	}
}
