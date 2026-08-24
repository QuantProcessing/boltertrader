package devsetup

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"
)

func TestProbeTradingUntilWaitsThroughStartupUnready(t *testing.T) {
	calls := 0
	status, _, err := probeTradingUntil(context.Background(), time.Second, time.Millisecond, func(context.Context) (int, []byte, error) {
		calls++
		if calls < 3 {
			return http.StatusServiceUnavailable, []byte(`{"data":{"components":{"gateway":{"ready":false,"reason":"catalog_projection_empty"}}}}`), nil
		}
		return http.StatusOK, nil, nil
	})
	if err != nil || status != http.StatusOK || calls != 3 {
		t.Fatalf("status=%d calls=%d err=%v, want 200 after three probes", status, calls, err)
	}
}

func TestProbeTradingUntilDoesNotRetryConfigurationFailure(t *testing.T) {
	calls := 0
	status, _, err := probeTradingUntil(context.Background(), time.Second, time.Millisecond, func(context.Context) (int, []byte, error) {
		calls++
		return http.StatusUnauthorized, nil, nil
	})
	if err != nil || status != http.StatusUnauthorized || calls != 1 {
		t.Fatalf("status=%d calls=%d err=%v, want one 401 probe", status, calls, err)
	}
}

func TestProbeTradingUntilPreservesLastReadinessResponseAtTimeout(t *testing.T) {
	responseBody := []byte(`{"data":{"components":{"publicData":{"ready":false,"reason":"market_data_route_unavailable"}}}}`)
	calls := 0
	status, body, err := probeTradingUntil(context.Background(), 20*time.Millisecond, time.Millisecond, func(ctx context.Context) (int, []byte, error) {
		calls++
		if calls == 1 {
			return http.StatusServiceUnavailable, responseBody, nil
		}
		<-ctx.Done()
		return 0, nil, ctx.Err()
	})
	if err != nil || status != http.StatusServiceUnavailable || !reflect.DeepEqual(body, responseBody) {
		t.Fatalf("status=%d body=%s err=%v, want the last complete 503 response", status, body, err)
	}
}

func TestProbeTradingUntilReturnsInitialConnectionError(t *testing.T) {
	want := errors.New("connection refused")
	_, _, err := probeTradingUntil(context.Background(), 5*time.Millisecond, time.Millisecond, func(context.Context) (int, []byte, error) {
		return 0, nil, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("err=%v, want %v", err, want)
	}
}

func TestTradingBlockingReasonsAreSortedAndNameMissingReasons(t *testing.T) {
	body := []byte(`{"data":{"components":{"publicData":{"ready":false,"reason":"market_data_route_unavailable"},"process":{"ready":true,"reason":"ready"},"accounts":{"ready":false}}}}`)
	want := []string{"accounts:not_ready", "publicData:market_data_route_unavailable"}
	if got := tradingBlockingReasons(body); !reflect.DeepEqual(got, want) {
		t.Fatalf("reasons=%v, want %v", got, want)
	}
}
