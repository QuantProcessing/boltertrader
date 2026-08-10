package migration

import (
	"context"
	"slices"
	"testing"

	"github.com/QuantProcessing/boltertrader/migration/authority"
)

func TestValidationSnapshotIsDeterministicAndSeparatesClasses(t *testing.T) {
	platform, gateway := authority.PlatformV15(), authority.GatewayV6()
	source := &snapshotSourceStub{
		rows: map[string][]SnapshotRow{
			platform.Name + "/platform_accounts": {
				{{Type: "text", Value: "account-b"}, {Type: "bigint", Value: "2"}},
				{{Type: "text", Value: "account-a"}, {Type: "bigint", Value: "1"}},
			},
			platform.Name + "/platform_schema_migrations": {{{Type: "bigint", Value: "15"}}},
			gateway.Name + "/schema_migrations":           {{{Type: "integer", Value: "6"}}},
		},
		gatewayActive: []SnapshotRow{{{Type: "text", Value: "account-a"}, {Type: "uuid", Value: "018f47a8-2578-7d3c-9d7d-5e4f4a445e11"}}},
		sessions:      map[string]SnapshotRow{"session-existing": {{Type: "text", Value: "principal-a"}}},
	}
	first, err := CaptureValidationSnapshot(context.Background(), source, platform, gateway)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(source.rows[platform.Name+"/platform_accounts"])
	second, err := CaptureValidationSnapshot(context.Background(), source, platform, gateway)
	if err != nil {
		t.Fatal(err)
	}
	if first.MatrixSHA256 == "" || first.PlatformCanonicalSHA256 != second.PlatformCanonicalSHA256 || first.PlatformMigrationSHA256 != second.PlatformMigrationSHA256 || first.GatewayActiveSHA256 != second.GatewayActiveSHA256 || first.AuthSessionSHA256 != second.AuthSessionSHA256 {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}

func TestValidationSnapshotComparisonAllowsOnlyNamedSessionLifecycle(t *testing.T) {
	platform, gateway := authority.PlatformV15(), authority.GatewayV6()
	beforeSource := &snapshotSourceStub{
		rows: map[string][]SnapshotRow{
			platform.Name + "/platform_accounts":          {{{Type: "text", Value: "account-a"}}},
			platform.Name + "/platform_schema_migrations": {{{Type: "bigint", Value: "15"}}},
			gateway.Name + "/schema_migrations":           {{{Type: "integer", Value: "6"}}},
		},
		sessions: map[string]SnapshotRow{"existing": {{Type: "text", Value: "principal-a"}}},
	}
	afterSource := beforeSource.clone()
	afterSource.sessions["browser-session"] = SnapshotRow{{Type: "text", Value: "principal-browser"}}
	before, err := CaptureValidationSnapshot(context.Background(), beforeSource, platform, gateway)
	if err != nil {
		t.Fatal(err)
	}
	after, err := CaptureValidationSnapshot(context.Background(), afterSource, platform, gateway)
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := CompareValidationSnapshots(before, after, "browser-session")
	if err != nil || !comparison.CanonicalUnchanged || !comparison.GatewayHeadsUnchanged || !comparison.MigrationUnchanged || !comparison.SessionLifecycleAllowed {
		t.Fatalf("comparison=%+v err=%v", comparison, err)
	}
	comparison, err = CompareValidationSnapshots(before, after, "other-session")
	if err != nil || comparison.SessionLifecycleAllowed {
		t.Fatalf("unnamed session comparison=%+v err=%v", comparison, err)
	}
}

func TestValidationSnapshotComparisonDetectsCanonicalAndMigrationDrift(t *testing.T) {
	platform, gateway := authority.PlatformV15(), authority.GatewayV6()
	source := &snapshotSourceStub{rows: map[string][]SnapshotRow{
		platform.Name + "/platform_accounts":          {{{Type: "text", Value: "account-a"}}},
		platform.Name + "/platform_schema_migrations": {{{Type: "bigint", Value: "15"}}},
		gateway.Name + "/schema_migrations":           {{{Type: "integer", Value: "6"}}},
	}}
	before, err := CaptureValidationSnapshot(context.Background(), source, platform, gateway)
	if err != nil {
		t.Fatal(err)
	}
	afterSource := source.clone()
	afterSource.rows[platform.Name+"/platform_accounts"] = []SnapshotRow{{{Type: "text", Value: "account-b"}}}
	afterSource.rows[gateway.Name+"/schema_migrations"] = []SnapshotRow{{{Type: "integer", Value: "7"}}}
	after, err := CaptureValidationSnapshot(context.Background(), afterSource, platform, gateway)
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := CompareValidationSnapshots(before, after, "")
	if err != nil || comparison.CanonicalUnchanged || comparison.MigrationUnchanged {
		t.Fatalf("comparison=%+v err=%v", comparison, err)
	}
}

type snapshotSourceStub struct {
	rows          map[string][]SnapshotRow
	gatewayActive []SnapshotRow
	sessions      map[string]SnapshotRow
}

func (source *snapshotSourceStub) AuthorityRows(_ context.Context, matrix authority.Matrix, object authority.Object) ([]SnapshotRow, error) {
	return append([]SnapshotRow(nil), source.rows[matrix.Name+"/"+object.Name]...), nil
}

func (source *snapshotSourceStub) GatewayActiveRows(context.Context) ([]SnapshotRow, error) {
	return append([]SnapshotRow(nil), source.gatewayActive...), nil
}

func (source *snapshotSourceStub) AuthSessionRows(context.Context) (map[string]SnapshotRow, error) {
	rows := make(map[string]SnapshotRow, len(source.sessions))
	for id, row := range source.sessions {
		rows[id] = append(SnapshotRow(nil), row...)
	}
	return rows, nil
}

func (source *snapshotSourceStub) clone() *snapshotSourceStub {
	clone := &snapshotSourceStub{rows: make(map[string][]SnapshotRow, len(source.rows)), gatewayActive: append([]SnapshotRow(nil), source.gatewayActive...), sessions: make(map[string]SnapshotRow, len(source.sessions))}
	for key, rows := range source.rows {
		clone.rows[key] = append([]SnapshotRow(nil), rows...)
	}
	for id, row := range source.sessions {
		clone.sessions[id] = append(SnapshotRow(nil), row...)
	}
	return clone
}
