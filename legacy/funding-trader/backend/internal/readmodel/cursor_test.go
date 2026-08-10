package readmodel

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCursorCodecRoundTripsOnlyWithinTheOriginalQueryScope(t *testing.T) {
	codec, err := NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	position := CursorPosition{
		Resource:        Opportunities,
		PrincipalID:     "principal-default",
		Mode:            "test",
		ContractVersion: ContractVersion,
		Fingerprint:     "netApr_desc|okx|executable",
		SortValue:       "12.50000000",
		ID:              "opp-1",
		OccurredAt:      time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC),
	}
	cursor, err := codec.Encode(position)
	if err != nil {
		t.Fatal(err)
	}
	if cursor == "" || strings.Contains(cursor, "opp-1") || strings.Contains(cursor, "principal-default") {
		t.Fatalf("cursor is not opaque: %q", cursor)
	}
	payload := cursorPayload(t, cursor)
	if !strings.Contains(string(payload), `"version":2`) ||
		!strings.Contains(string(payload), `"mode":"test"`) ||
		!strings.Contains(string(payload), `"contractVersion":"`+ContractVersion+`"`) ||
		strings.Contains(string(payload), `"environment"`) {
		t.Fatalf("cursor payload has wrong contract fields: %s", payload)
	}
	decoded, err := codec.Decode(cursor, CursorExpectation{
		Resource:        Opportunities,
		PrincipalID:     "principal-default",
		Mode:            "test",
		ContractVersion: ContractVersion,
		Fingerprint:     "netApr_desc|okx|executable",
	})
	if err != nil {
		t.Fatal(err)
	}
	if decoded != position {
		t.Fatalf("decoded=%+v want=%+v", decoded, position)
	}

	for name, expectation := range map[string]CursorExpectation{
		"resource": {
			Resource: Positions, PrincipalID: position.PrincipalID,
			Mode: position.Mode, ContractVersion: position.ContractVersion, Fingerprint: position.Fingerprint,
		},
		"principal": {
			Resource: position.Resource, PrincipalID: "other",
			Mode: position.Mode, ContractVersion: position.ContractVersion, Fingerprint: position.Fingerprint,
		},
		"mode": {
			Resource: position.Resource, PrincipalID: position.PrincipalID,
			Mode: "live", ContractVersion: position.ContractVersion, Fingerprint: position.Fingerprint,
		},
		"contract version": {
			Resource: position.Resource, PrincipalID: position.PrincipalID,
			Mode: position.Mode, ContractVersion: "private-v1-mode-0", Fingerprint: position.Fingerprint,
		},
		"filters": {
			Resource: position.Resource, PrincipalID: position.PrincipalID,
			Mode: position.Mode, ContractVersion: position.ContractVersion, Fingerprint: "different",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := codec.Decode(cursor, expectation); !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestCursorCodecRejectsTamperingAndWeakKeys(t *testing.T) {
	if _, err := NewCursorCodec([]byte("short")); err == nil {
		t.Fatal("expected weak cursor key to be rejected")
	}
	codec, err := NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := codec.Encode(CursorPosition{
		Resource: Opportunities, PrincipalID: "principal-default", Mode: "test",
		Fingerprint: "netApr_desc", SortValue: "1", ID: "opp-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	last := cursor[len(cursor)-1]
	replacement := byte('A')
	if last == replacement {
		replacement = 'B'
	}
	tampered := cursor[:len(cursor)-1] + string(replacement)
	if _, err := codec.Decode(tampered, CursorExpectation{
		Resource: Opportunities, PrincipalID: "principal-default",
		Mode: "test", ContractVersion: ContractVersion, Fingerprint: "netApr_desc",
	}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("error=%v", err)
	}
}

func TestCursorCodecRejectsIncompleteOrNativeProductScope(t *testing.T) {
	codec, err := NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	valid := CursorPosition{
		Resource: Opportunities, PrincipalID: "principal-default", Mode: "test",
		Fingerprint: "netApr_desc", SortValue: "1", ID: "opp-1",
	}
	for name, mutate := range map[string]func(*CursorPosition){
		"missing principal":    func(position *CursorPosition) { position.PrincipalID = "" },
		"demo product mode":    func(position *CursorPosition) { position.Mode = "demo" },
		"testnet product mode": func(position *CursorPosition) { position.Mode = "testnet" },
		"mainnet product mode": func(position *CursorPosition) { position.Mode = "mainnet" },
	} {
		t.Run(name, func(t *testing.T) {
			position := valid
			mutate(&position)
			if _, err := codec.Encode(position); !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestCursorCodecRejectsSignedLegacyV1Cursor(t *testing.T) {
	codec, err := NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	cursor := legacyV1Cursor(t, codec, CursorPosition{
		Resource:    Opportunities,
		PrincipalID: "principal-default",
		Mode:        "test",
		Fingerprint: "netApr_desc",
		SortValue:   "1",
		ID:          "opp-1",
	})
	if _, err := codec.Decode(cursor, CursorExpectation{
		Resource:        Opportunities,
		PrincipalID:     "principal-default",
		Mode:            "test",
		ContractVersion: ContractVersion,
		Fingerprint:     "netApr_desc",
	}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("error=%v", err)
	}
}

func legacyV1Cursor(t *testing.T, codec *CursorCodec, position CursorPosition) string {
	t.Helper()
	payload, err := json.Marshal(struct {
		Version  int            `json:"version"`
		Position CursorPosition `json:"position"`
	}{Version: 1, Position: position})
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(codec.sign(payload))
}

func cursorPayload(t *testing.T, cursor string) []byte {
	t.Helper()
	parts := strings.Split(cursor, ".")
	if len(parts) != 2 {
		t.Fatalf("cursor parts=%d", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
