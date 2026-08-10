package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/QuantProcessing/boltertrader/migration/authority"
)

type SnapshotCell struct {
	Type  string `json:"type"`
	Value string `json:"value,omitempty"`
	Null  bool   `json:"null,omitempty"`
}

type SnapshotRow []SnapshotCell

type SnapshotSource interface {
	AuthorityRows(context.Context, authority.Matrix, authority.Object) ([]SnapshotRow, error)
	GatewayActiveRows(context.Context) ([]SnapshotRow, error)
	AuthSessionRows(context.Context) (map[string]SnapshotRow, error)
}

type ValidationSnapshot struct {
	MatrixSHA256            string    `json:"matrixSha256"`
	PlatformCanonicalSHA256 string    `json:"platformCanonicalSha256"`
	PlatformMigrationSHA256 string    `json:"platformMigrationSha256"`
	GatewayActiveSHA256     string    `json:"gatewayActiveSha256"`
	AuthSessionSHA256       string    `json:"authSessionSha256"`
	CapturedAt              time.Time `json:"capturedAt"`
	authSessions            map[string]SnapshotRow
}

type ValidationComparison struct {
	CanonicalUnchanged      bool `json:"canonicalUnchanged"`
	GatewayHeadsUnchanged   bool `json:"gatewayHeadsUnchanged"`
	MigrationUnchanged      bool `json:"migrationUnchanged"`
	SessionLifecycleAllowed bool `json:"sessionLifecycleAllowed"`
}

type snapshotObject struct {
	Matrix string          `json:"matrix"`
	Schema string          `json:"schema"`
	Name   string          `json:"name"`
	Kind   string          `json:"kind"`
	Rows   json.RawMessage `json:"rows"`
}

func CaptureValidationSnapshot(ctx context.Context, source SnapshotSource, platform, gateway authority.Matrix) (ValidationSnapshot, error) {
	if source == nil || platform.Validate() != nil || gateway.Validate() != nil {
		return ValidationSnapshot{}, fmt.Errorf("validation snapshot authority is invalid")
	}
	canonical, migration := make([]snapshotObject, 0), make([]snapshotObject, 0)
	for _, matrix := range []authority.Matrix{platform, gateway} {
		for _, object := range matrix.Objects {
			if object.Class == authority.ValidationAuthSession || (matrix.Name == gateway.Name && object.Class == authority.Canonical) {
				continue
			}
			rows, err := source.AuthorityRows(ctx, matrix, object)
			if err != nil {
				return ValidationSnapshot{}, fmt.Errorf("capture %s.%s: %w", matrix.Name, object.Name, err)
			}
			encoded, err := canonicalRows(rows)
			if err != nil {
				return ValidationSnapshot{}, fmt.Errorf("capture %s.%s: %w", matrix.Name, object.Name, err)
			}
			entry := snapshotObject{Matrix: matrix.Name, Schema: object.Schema, Name: object.Name, Kind: string(object.Kind), Rows: encoded}
			switch object.Class {
			case authority.Canonical:
				canonical = append(canonical, entry)
			case authority.MigrationOnly:
				migration = append(migration, entry)
			}
		}
	}
	gatewayRows, err := source.GatewayActiveRows(ctx)
	if err != nil {
		return ValidationSnapshot{}, fmt.Errorf("capture Gateway active heads: %w", err)
	}
	gatewayEncoded, err := canonicalRows(gatewayRows)
	if err != nil {
		return ValidationSnapshot{}, fmt.Errorf("capture Gateway active heads: %w", err)
	}
	sessions, err := source.AuthSessionRows(ctx)
	if err != nil {
		return ValidationSnapshot{}, fmt.Errorf("capture auth sessions: %w", err)
	}
	sessionEncoded, sessionCopy, err := canonicalSessions(sessions)
	if err != nil {
		return ValidationSnapshot{}, fmt.Errorf("capture auth sessions: %w", err)
	}
	sortSnapshotObjects(canonical)
	sortSnapshotObjects(migration)
	matrixEncoded, _ := json.Marshal([]string{platform.Digest(), gateway.Digest()})
	return ValidationSnapshot{
		MatrixSHA256:            bytesSHA256(matrixEncoded),
		PlatformCanonicalSHA256: jsonSHA256(canonical),
		PlatformMigrationSHA256: jsonSHA256(migration),
		GatewayActiveSHA256:     bytesSHA256(gatewayEncoded),
		AuthSessionSHA256:       bytesSHA256(sessionEncoded),
		CapturedAt:              time.Now().UTC(),
		authSessions:            sessionCopy,
	}, nil
}

func CompareValidationSnapshots(before, after ValidationSnapshot, allowedSessionID string) (ValidationComparison, error) {
	if before.MatrixSHA256 == "" || before.MatrixSHA256 != after.MatrixSHA256 {
		return ValidationComparison{}, fmt.Errorf("validation snapshot matrix changed")
	}
	return ValidationComparison{
		CanonicalUnchanged:      before.PlatformCanonicalSHA256 == after.PlatformCanonicalSHA256,
		GatewayHeadsUnchanged:   before.GatewayActiveSHA256 == after.GatewayActiveSHA256,
		MigrationUnchanged:      before.PlatformMigrationSHA256 == after.PlatformMigrationSHA256,
		SessionLifecycleAllowed: sessionsEqualExcept(before.authSessions, after.authSessions, allowedSessionID),
	}, nil
}

func canonicalRows(rows []SnapshotRow) ([]byte, error) {
	encoded := make([][]byte, 0, len(rows))
	for _, row := range rows {
		if len(row) == 0 {
			return nil, fmt.Errorf("empty typed row")
		}
		for _, cell := range row {
			if cell.Type == "" || (cell.Null && cell.Value != "") {
				return nil, fmt.Errorf("invalid typed cell")
			}
		}
		data, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		encoded = append(encoded, data)
	}
	sort.Slice(encoded, func(i, j int) bool { return bytes.Compare(encoded[i], encoded[j]) < 0 })
	return json.Marshal(encoded)
}

func canonicalSessions(sessions map[string]SnapshotRow) ([]byte, map[string]SnapshotRow, error) {
	ids := make([]string, 0, len(sessions))
	copyOfSessions := make(map[string]SnapshotRow, len(sessions))
	for id, row := range sessions {
		if id == "" {
			return nil, nil, fmt.Errorf("empty session id")
		}
		ids = append(ids, id)
		copyOfSessions[id] = append(SnapshotRow(nil), row...)
	}
	sort.Strings(ids)
	type entry struct {
		ID  string          `json:"id"`
		Row json.RawMessage `json:"row"`
	}
	entries := make([]entry, 0, len(ids))
	for _, id := range ids {
		rows, err := canonicalRows([]SnapshotRow{sessions[id]})
		if err != nil {
			return nil, nil, err
		}
		entries = append(entries, entry{ID: id, Row: rows})
	}
	encoded, err := json.Marshal(entries)
	return encoded, copyOfSessions, err
}

func sessionsEqualExcept(before, after map[string]SnapshotRow, allowed string) bool {
	beforeCanonical := sessionMapCanonical(before, allowed)
	afterCanonical := sessionMapCanonical(after, allowed)
	return bytes.Equal(beforeCanonical, afterCanonical)
}

func sessionMapCanonical(sessions map[string]SnapshotRow, excluded string) []byte {
	copyOfSessions := make(map[string]SnapshotRow, len(sessions))
	for id, row := range sessions {
		if id != excluded {
			copyOfSessions[id] = row
		}
	}
	encoded, _, _ := canonicalSessions(copyOfSessions)
	return encoded
}

func sortSnapshotObjects(objects []snapshotObject) {
	sort.Slice(objects, func(i, j int) bool {
		return objects[i].Matrix+"\x00"+objects[i].Schema+"\x00"+objects[i].Name < objects[j].Matrix+"\x00"+objects[j].Schema+"\x00"+objects[j].Name
	})
}

func jsonSHA256(value any) string {
	encoded, _ := json.Marshal(value)
	return bytesSHA256(encoded)
}

func bytesSHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
