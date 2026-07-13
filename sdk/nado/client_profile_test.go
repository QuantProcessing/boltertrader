package nado

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

type nadoRoundTripFunc func(*http.Request) (*http.Response, error)

func (f nadoRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestClientRoutesEveryRESTSurfaceThroughProfile(t *testing.T) {
	profile, err := NewProfile(EnvironmentTestnet)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(profile)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	seen := make(map[string]int)
	client.WithHTTPClient(&http.Client{Transport: nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		mu.Lock()
		seen[request.Method+" "+request.URL.String()]++
		mu.Unlock()
		payload := `{}`
		if strings.Contains(request.URL.Path, "/query") || strings.Contains(request.URL.Path, "/execute") {
			payload = `{"status":"success","data":{}}`
		} else if strings.HasSuffix(request.URL.Path, "/assets") {
			payload = `[]`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(payload)),
			Request:    request,
		}, nil
	})})

	if _, err := client.QueryGateWayV1(context.Background(), http.MethodGet, map[string]interface{}{"type": "contracts"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.QueryGateWayV1(context.Background(), http.MethodPost, map[string]interface{}{"type": "contracts"}); err != nil {
		t.Fatal(err)
	}
	if err := client.QueryGatewayV2(context.Background(), "/assets", nil, &[]AssetV2{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.QueryArchiveV1(context.Background(), map[string]interface{}{"candlesticks": map[string]int{"product_id": 1}}); err != nil {
		t.Fatal(err)
	}
	if err := client.QueryArchiveV2(context.Background(), "/contracts", nil, &ContractV2Map{}); err != nil {
		t.Fatal(err)
	}

	want := []string{
		http.MethodGet + " " + profile.GatewayV1URL() + "/query?type=contracts",
		http.MethodPost + " " + profile.GatewayV1URL() + "/query",
		http.MethodGet + " " + profile.GatewayV2URL() + "/assets",
		http.MethodPost + " " + profile.ArchiveV1URL(),
		http.MethodGet + " " + profile.ArchiveV2URL() + "/contracts",
	}
	mu.Lock()
	defer mu.Unlock()
	for _, target := range want {
		if seen[target] != 1 {
			t.Errorf("request %q count = %d", target, seen[target])
		}
	}
}

func TestClientRejectsRedirectWithoutFollowing(t *testing.T) {
	profile, _ := NewProfile(EnvironmentTestnet)
	client, err := NewClient(profile)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client.WithHTTPClient(&http.Client{Transport: nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"https://gateway.prod.nado.xyz/v1/query?type=contracts"}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    request,
		}, nil
	})})
	if _, err := client.QueryGateWayV1(context.Background(), http.MethodGet, map[string]interface{}{"type": "contracts"}); err == nil {
		t.Fatal("redirect unexpectedly succeeded")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}
