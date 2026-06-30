package mbx

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	exchanges "github.com/QuantProcessing/boltertrader/internal/errs"
)

func TestUsedWeight_UpdateByHeader(t *testing.T) {
	tests := []struct {
		name     string
		headers  map[string]string
		wantUsed int64
		want1M   int64
	}{
		{
			name:     "both headers",
			headers:  map[string]string{"X-Mbx-Used-Weight": "100", "X-Mbx-Used-Weight-1m": "500"},
			wantUsed: 100,
			want1M:   500,
		},
		{
			name:     "only weight",
			headers:  map[string]string{"X-Mbx-Used-Weight": "42"},
			wantUsed: 42,
			want1M:   0,
		},
		{
			name:     "empty headers",
			headers:  map[string]string{},
			wantUsed: 0,
			want1M:   0,
		},
		{
			name:     "invalid value",
			headers:  map[string]string{"X-Mbx-Used-Weight": "notanumber"},
			wantUsed: 0,
			want1M:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := &UsedWeight{}
			h := http.Header{}
			for k, v := range tt.headers {
				h.Set(k, v)
			}
			u.UpdateByHeader(h)
			if got := atomic.LoadInt64(&u.Used); got != tt.wantUsed {
				t.Errorf("Used = %d, want %d", got, tt.wantUsed)
			}
			if got := atomic.LoadInt64(&u.Used1M); got != tt.want1M {
				t.Errorf("Used1M = %d, want %d", got, tt.want1M)
			}
		})
	}
}

func TestOrderCount_UpdateByHeader(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		want10s int64
		want1d  int64
	}{
		{
			name:    "both headers",
			headers: map[string]string{"X-Mbx-Order-Count-10s": "5", "X-Mbx-Order-Count-1d": "200"},
			want10s: 5,
			want1d:  200,
		},
		{
			name:    "empty headers",
			headers: map[string]string{},
			want10s: 0,
			want1d:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &OrderCount{}
			h := http.Header{}
			for k, v := range tt.headers {
				h.Set(k, v)
			}
			o.UpdateByHeader(h)
			if got := atomic.LoadInt64(&o.Count10s); got != tt.want10s {
				t.Errorf("Count10s = %d, want %d", got, tt.want10s)
			}
			if got := atomic.LoadInt64(&o.Count1d); got != tt.want1d {
				t.Errorf("Count1d = %d, want %d", got, tt.want1d)
			}
		})
	}
}

func TestIsRateLimitMessage(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"Way too many requests; IP banned until 1234567890", true},
		{"Request weight used: 1200/1200", true},
		{"Too many requests", true},
		{"Too Many Requests", true},
		{"Banned until 2024-01-01", true},
		{"insufficient balance", false},
		{"order not found", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.msg, func(t *testing.T) {
			if got := IsRateLimitMessage(tt.msg); got != tt.want {
				t.Errorf("IsRateLimitMessage(%q) = %v, want %v", tt.msg, got, tt.want)
			}
		})
	}
}

func TestMapAPIError_RateLimited(t *testing.T) {
	tests := []struct {
		name          string
		exchange      string
		status        int
		code          int
		message       string
		wantRateLimit bool
	}{
		{
			name: "HTTP 429", exchange: "TEST", status: 429,
			code: -1003, message: "Way too many requests",
			wantRateLimit: true,
		},
		{
			name: "HTTP 418 (Binance IP ban)", exchange: "TEST", status: 418,
			code: -1003, message: "banned until 1234567890",
			wantRateLimit: true,
		},
		{
			name: "code -1003", exchange: "TEST", status: 400,
			code: -1003, message: "Way too many requests",
			wantRateLimit: true,
		},
		{
			name: "code -1015", exchange: "TEST", status: 400,
			code: -1015, message: "Too many new orders",
			wantRateLimit: true,
		},
		{
			name: "rate limit message", exchange: "TEST", status: 400,
			code: -1000, message: "Request weight used: 1200/1200. Please slow down.",
			wantRateLimit: true,
		},
		{
			name: "regular error", exchange: "TEST", status: 400,
			code: -1001, message: "Internal error",
			wantRateLimit: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := MapAPIError(tt.exchange, tt.status, nil, func(d []byte) (int, string, error) {
				return tt.code, tt.message, nil
			})
			if tt.wantRateLimit {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !errors.Is(err, exchanges.ErrRateLimited) {
					t.Errorf("expected ErrRateLimited, got %v", err)
				}
			} else {
				if err != nil {
					t.Errorf("expected nil, got %v", err)
				}
			}
		})
	}
}

func TestUnmarshalAPIError(t *testing.T) {
	tests := []struct {
		name     string
		data     string
		wantCode int
		wantMsg  string
		wantErr  bool
	}{
		{
			name: "valid error", data: `{"code":-1003,"msg":"Way too many requests"}`,
			wantCode: -1003, wantMsg: "Way too many requests",
		},
		{
			name: "invalid json", data: `not json`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, msg, err := UnmarshalAPIError([]byte(tt.data))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if code != tt.wantCode {
				t.Errorf("code = %d, want %d", code, tt.wantCode)
			}
			if msg != tt.wantMsg {
				t.Errorf("msg = %q, want %q", msg, tt.wantMsg)
			}
		})
	}
}
