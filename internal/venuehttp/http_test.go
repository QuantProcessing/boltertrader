package venuehttp

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHTTPFailureRetainsStatusAndDelayWithoutRenderingBody(t *testing.T) {
	native := errors.New("SECRET-BODY-AND-SIGNATURE")
	err := WithHTTPResponse(native, &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"7"}}})
	var failure *HTTPError
	if !errors.As(err, &failure) || failure.RetryAfter != 7*time.Second || !errors.Is(err, ErrRateLimited) || !errors.Is(err, native) {
		t.Fatalf("failure=%+v", failure)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatal("response error leaked body")
	}
}
func TestRetryAfterFormsAndOverflow(t *testing.T) {
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		value string
		want  time.Duration
	}{{"3", 3 * time.Second}, {"-2", 0}, {"garbage", 0}, {now.Add(4 * time.Second).Format(http.TimeFormat), 4 * time.Second}, {now.Add(-time.Second).Format(http.TimeFormat), 0}, {"9223372036854775807", time.Duration(1<<63 - 1)}} {
		if got := RetryAfter(http.Header{"Retry-After": []string{test.value}}, now); got != test.want {
			t.Fatalf("%s: %s != %s", test.value, got, test.want)
		}
	}
}
