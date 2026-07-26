package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func FuzzStrictJSONDecoder(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{"value":"ok"}`),
		[]byte(`{"value":"a","value":"b"}`),
		[]byte(`{"unknown":true}`),
		[]byte(`{"value":"ok"}{}`),
		{0xff, 0xfe},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 128<<10 {
			t.Skip()
		}
		request := httptest.NewRequest(http.MethodPost, "/v1/orders", bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		var destination struct {
			Value string `json:"value"`
		}
		_ = decodeStrictJSON(recorder, request, defaultBodyLimit, &destination)
	})
}
