package sdk

import (
	"context"
	"errors"
	"github.com/QuantProcessing/boltertrader/internal/venuehttp"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPublicRateLimitUsesNativeResetTimeAndPreservesCode(t *testing.T) {
	reset := time.Now().Add(10 * time.Second)
	client := NewClient().WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"X-Bapi-Limit-Reset-Timestamp": []string{strconv.FormatInt(reset.UnixMilli(), 10)}}, Body: io.NopCloser(strings.NewReader(`{"retCode":10006,"retMsg":"SECRET","result":{}}`))}, nil
	})})
	_, err := client.GetTickers(context.Background(), "linear")
	var failure *venuehttp.HTTPError
	var native *ResponseError
	if !errors.As(err, &failure) || !errors.As(err, &native) || failure.Code != "10006" || failure.RetryAfter < 9*time.Second || failure.RetryAfter > 10*time.Second || native.Code != 10006 {
		t.Fatalf("failure=%+v err=%v", failure, err)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatal("raw body leaked")
	}
}
