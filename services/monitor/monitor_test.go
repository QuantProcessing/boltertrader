package monitor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

var epoch = time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)

func testConfig() Config {
	return Config{Source: "http://localhost:18080", Instruments: []string{"btc"}, Window: 10 * time.Second, ChangePercent: decimal.NewFromInt(5), MinSamples: 3, MaxGap: 3 * time.Second, Cooldown: 10 * time.Second}
}
func testEngine(t *testing.T) *engine {
	t.Helper()
	e, err := newEngine(testConfig(), epoch)
	if err != nil {
		t.Fatal(err)
	}
	e.Pending = nil
	return e
}
func quote(t *testing.T, e *engine, seconds int, value string) {
	t.Helper()
	at := epoch.Add(time.Duration(seconds) * time.Second)
	if err := e.advance(at); err != nil {
		t.Fatal(err)
	}
	e.observe("btc", at, value, value)
}
func kinds(events []Event) []string {
	result := []string{}
	for _, event := range events {
		result = append(result, event.Kind)
	}
	return result
}

func TestReplayDropRiseAndCooldown(t *testing.T) {
	e := testEngine(t)
	quote(t, e, 0, "100")
	quote(t, e, 1, "100")
	quote(t, e, 2, "100")
	quote(t, e, 3, "95")
	if got := kinds(e.Pending); !reflect.DeepEqual(got, []string{"state", "price_change"}) {
		t.Fatal(got)
	}
	alert := e.Pending[1]
	if alert.State != Triggered || alert.PriceBasis != PriceBasis || alert.ReferencePrice != "100" || alert.ChangePercent != "-5" || alert.Samples != 4 || !alert.ReferenceAt.Equal(epoch) {
		t.Fatalf("wrong drop evidence: %+v", alert)
	}
	e.Pending = nil
	// Re-entry and opposite direction share the instrument's notification cooldown.
	quote(t, e, 4, "100")
	quote(t, e, 5, "106")
	quote(t, e, 6, "107")
	if got := kinds(e.Pending); !reflect.DeepEqual(got, []string{"state"}) || e.Instruments["btc"].State != Triggered {
		t.Fatal(got)
	}
	for n := 7; n <= 13; n++ {
		quote(t, e, n, "107")
	}
	last := e.Pending[len(e.Pending)-1]
	if last.Kind != "price_change" || last.ChangePercent != "12.63157895" || last.ReferencePrice != "95" || !last.At.Equal(epoch.Add(13*time.Second)) {
		t.Fatalf("wrong rolling reference/cooldown: %+v", last)
	}
}

func TestEveryQuoteIsEvaluatedWithoutCountingBurstAsSamples(t *testing.T) {
	e := testEngine(t)
	quote(t, e, 0, "100")
	for n := 1; n < 100; n++ {
		at := epoch.Add(time.Duration(n) * time.Millisecond)
		if err := e.advance(at); err != nil {
			t.Fatal(err)
		}
		e.observe("btc", at, "100", "100")
	}
	if len(e.Instruments["btc"].Samples) != 1 || len(e.Pending) != 0 {
		t.Fatal("message burst made rule ready")
	}
	quote(t, e, 1, "100")
	quote(t, e, 2, "100")
	if err := e.advance(epoch.Add(2100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	e.observe("btc", e.now, "90", "92")
	last := e.Pending[len(e.Pending)-1]
	if last.Kind != "price_change" || last.Price != "91" || last.ChangePercent != "-9" || last.Samples != 3 {
		t.Fatalf("intra-second crash lost: %+v", last)
	}
}

func TestBadQuotesCannotPreventTimeoutOrRecoverIt(t *testing.T) {
	e := testEngine(t)
	quote(t, e, 0, "100")
	quote(t, e, 1, "100")
	quote(t, e, 2, "100")
	e.Pending = nil
	if err := e.advance(epoch.Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct {
		at       time.Time
		bid, ask string
	}{
		{epoch.Add(time.Second), "1", "1"},         // out of order
		{epoch.Add(2 * time.Second), "1", "1"},     // duplicate
		{epoch.Add(5 * time.Second), "1", "1"},     // future
		{epoch.Add(4 * time.Second), "101", "100"}, // crossed
		{epoch.Add(4 * time.Second), "0", "100"},
		{epoch.Add(4 * time.Second), "NaN", "100"},
		{epoch.Add(4 * time.Second), "1e101", "1e101"},
	} {
		e.observe("btc", input.at, input.bid, input.ask)
	}
	if !e.Instruments["btc"].LastObserved.Equal(epoch.Add(2*time.Second)) || len(e.Pending) != 0 {
		t.Fatal("bad quote changed the watermark or triggered an alert")
	}
	if err := e.advance(epoch.Add(6 * time.Second)); err != nil {
		t.Fatal(err)
	}
	e.observe("btc", epoch.Add(2500*time.Millisecond), "1", "1") // increasing but stale
	if e.Instruments["btc"].State != Interrupted || len(e.Instruments["btc"].Samples) != 0 || !reflect.DeepEqual(kinds(e.Pending), []string{"interrupted"}) {
		t.Fatal("stale quote recovered interruption")
	}
	e.interrupt("btc", "stream_resync")
	quote(t, e, 7, "50")
	quote(t, e, 8, "50")
	quote(t, e, 9, "50")
	if !reflect.DeepEqual(kinds(e.Pending), []string{"interrupted", "recovered", "state"}) || e.Instruments["btc"].State != Normal {
		t.Fatal("recovery compared prices across a data gap", e.Pending)
	}
}

func TestTimeoutWithoutAnyDataAndWindowBound(t *testing.T) {
	e := testEngine(t)
	if err := e.advance(epoch.Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if e.Pending[0].Kind != "interrupted" {
		t.Fatal("startup silence was not detected")
	}
	for n := 5; n < 200; n++ {
		quote(t, e, n, "100")
		if len(e.Instruments["btc"].Samples) > 11 {
			t.Fatal("window grew without bound")
		}
	}
	if err := e.advance(epoch); err == nil {
		t.Fatal("clock regression accepted")
	}
}

func TestThresholdUsesUnroundedDecimal(t *testing.T) {
	e := testEngine(t)
	quote(t, e, 0, "100")
	quote(t, e, 1, "100")
	quote(t, e, 2, "95.000000000001")
	if e.Instruments["btc"].State != Normal {
		t.Fatal("rounded percentage caused a false alert")
	}
	quote(t, e, 3, "95")
	if e.Instruments["btc"].State != Triggered {
		t.Fatal("exact threshold missed")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("offline failure") }

func TestRestartReplaysPendingIDsAndPreservesCooldown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	f, e, err := openState(path, testConfig(), epoch)
	if err != nil {
		t.Fatal(err)
	}
	var initial bytes.Buffer
	if err := f.flush(e, &initial); err != nil {
		t.Fatal(err)
	}
	quote(t, e, 0, "100")
	quote(t, e, 1, "100")
	quote(t, e, 2, "90")
	id := e.Pending[0].ID
	if err := f.flush(e, failWriter{}); err == nil {
		t.Fatal("output failure ignored")
	}
	if _, _, err := openState(path, testConfig(), e.now); err == nil {
		t.Fatal("second process acquired state")
	}
	f.close()
	f, restored, err := openState(path, testConfig(), epoch.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer f.close()
	if len(restored.Pending) != 1 || restored.Pending[0].ID != id {
		t.Fatal("pending event identity lost")
	}
	var output bytes.Buffer
	if err := f.flush(restored, &output); err != nil {
		t.Fatal(err)
	}
	var event Event
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event); err != nil || event.ID != id {
		t.Fatal("replay changed identity", err)
	}
	quote(t, restored, 3, "90")
	if len(restored.Pending) != 0 || restored.Instruments["btc"].State != Triggered || len(restored.Instruments["btc"].Samples) != 4 {
		t.Fatal("short restart lost window or cooldown")
	}
	if err := restored.advance(epoch.Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}
	quote(t, restored, 20, "45")
	if !reflect.DeepEqual(kinds(restored.Pending), []string{"interrupted", "recovered"}) || restored.Instruments["btc"].State != Insufficient {
		t.Fatal("long restart compared missing window")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("checkpoint permissions", err)
	}
}

func TestCrashAfterOutputReplaysSameBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	f, e, err := openState(path, testConfig(), epoch)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.save(e); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	for _, event := range e.Pending {
		if err := json.NewEncoder(&output).Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate process death between successful output and checkpoint acknowledgement.
	f.close()
	f, restored, err := openState(path, testConfig(), epoch.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer f.close()
	var replay bytes.Buffer
	if err := f.flush(restored, &replay); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), replay.Bytes()) {
		t.Fatal("crash replay changed event")
	}
}

func TestStateRejectsChangedRulesCorruptionAndFutureClock(t *testing.T) {
	for _, change := range []string{"source", "threshold", "instruments", "corruption", "future"} {
		t.Run(change, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			c := testConfig()
			f, e, err := openState(path, c, epoch)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.save(e); err != nil {
				t.Fatal(err)
			}
			f.close()
			now := epoch.Add(time.Second)
			switch change {
			case "source":
				c.Source = "http://other-gateway"
			case "threshold":
				c.ChangePercent = decimal.NewFromInt(6)
			case "instruments":
				c.Instruments = []string{"eth"}
			case "corruption":
				if err := os.WriteFile(path, []byte(`{"version":1`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "future":
				now = epoch.Add(-time.Second)
			}
			if f, _, err := openState(path, c, now); err == nil {
				f.close()
				t.Fatal("incompatible checkpoint accepted")
			}
		})
	}
}

func TestKilledProcessReleasesLockAndRetainsPendingEvent(t *testing.T) {
	if path := os.Getenv("BT_MONITOR_CRASH_CHILD"); path != "" {
		f, e, err := openState(path, testConfig(), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		defer f.close()
		if err := f.save(e); err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(os.Stdout).Encode(e.Pending[0]); err != nil {
			t.Fatal(err)
		}
		// Parent kills this process after reading the event, before acknowledgement.
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	path := filepath.Join(t.TempDir(), "crash.json")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestKilledProcessReleasesLockAndRetainsPendingEvent$")
	cmd.Env = append(os.Environ(), "BT_MONITOR_CRASH_CHILD="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	line, err := bufio.NewReader(stdout).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var written Event
	if err := json.Unmarshal(line, &written); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("child did not crash")
	}
	f, restored, err := openState(path, testConfig(), time.Now())
	if err != nil {
		t.Fatal("killed process left state locked", err)
	}
	defer f.close()
	if len(restored.Pending) != 1 || restored.Pending[0].ID != written.ID {
		t.Fatal("pending event was lost or reassigned")
	}
}

func TestRulesAndGapsAreIsolatedByInstrument(t *testing.T) {
	c := testConfig()
	c.Instruments = []string{"btc", "eth"}
	e, err := newEngine(c, epoch)
	if err != nil {
		t.Fatal(err)
	}
	e.Pending = nil
	for n := 0; n <= 2; n++ {
		quote(t, e, n, "100")
		e.observe("eth", e.now, "100", "100")
	}
	e.Pending = nil
	quote(t, e, 3, "90")
	e.interrupt("eth", "stream_resync")
	quote(t, e, 4, "90")
	e.observe("eth", e.now, "40", "40")
	if e.Instruments["btc"].State != Triggered || e.Instruments["eth"].State != Insufficient || len(e.Instruments["btc"].Samples) != 5 || len(e.Instruments["eth"].Samples) != 1 {
		t.Fatal("one instrument changed another's state")
	}
	if !reflect.DeepEqual(kinds(e.Pending), []string{"price_change", "interrupted", "recovered"}) {
		t.Fatal(e.Pending)
	}
}
