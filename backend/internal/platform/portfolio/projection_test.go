package portfolio

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestProjectionMaintainsProductAttributedPositionAndRealizedPnL(t *testing.T) {
	projection := NewProjection()
	base := Fill{
		FillID: "fill-1", CommandID: "command-1", LineageID: "lineage-a", ProductID: "manual",
		Mode: ModeTest, AccountID: "account-a", InstrumentID: "BTC-USDT", Side: SideBuy,
		Quantity: "2", Price: "100", Fee: "1", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: time.Now(),
	}
	position, result, err := projection.ApplyFill(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if position.Quantity != "2" || position.AverageEntryPrice != "100" || result.RealizedPnL != "0" {
		t.Fatalf("open position=%+v result=%+v", position, result)
	}
	closeFill := base
	closeFill.FillID = "fill-2"
	closeFill.Side = SideSell
	closeFill.Quantity = "1"
	closeFill.Price = "110"
	closeFill.Fee = "0.5"
	position, result, err = projection.ApplyFill(context.Background(), closeFill)
	if err != nil {
		t.Fatal(err)
	}
	if position.Quantity != "1" || position.RealizedPnL != "10" || result.RealizedPnL != "10" {
		t.Fatalf("closed position=%+v result=%+v", position, result)
	}
	mismatched := closeFill
	mismatched.FillID = "fill-3"
	mismatched.SettlementCurrency = "USDC"
	if _, _, err := projection.ApplyFill(context.Background(), mismatched); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("settlement mismatch error=%v, want ErrCurrencyMismatch", err)
	}
	marked, err := projection.ApplyMark(ModeTest, "account-a", "manual", "lineage-a", "BTC-USDT", "105", time.Now())
	if err != nil || marked.UnrealizedPnL != "5" {
		t.Fatalf("marked=%+v err=%v", marked, err)
	}
	if _, _, err := projection.ApplyFill(context.Background(), Fill{FillID: "unattributed"}); err == nil {
		t.Fatal("unattributed fill accepted")
	}
}

func TestProjectionSeparatesIdenticalOwnershipAcrossModes(t *testing.T) {
	projection := NewProjection()
	fill := Fill{
		FillID: "test-fill", CommandID: "test-command", LineageID: "lineage-a", ProductID: "manual", Mode: ModeTest,
		AccountID: "account-a", InstrumentID: "BTC-USDT", Side: SideBuy, Quantity: "1", Price: "100", Fee: "0", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: time.Now(),
	}
	if _, _, err := projection.ApplyFill(context.Background(), fill); err != nil {
		t.Fatal(err)
	}
	fill.FillID, fill.CommandID, fill.Mode, fill.Price = "live-fill", "live-command", ModeLive, "200"
	if _, _, err := projection.ApplyFill(context.Background(), fill); err != nil {
		t.Fatal(err)
	}
	testPositions := projection.List(ModeTest, "account-a", "manual")
	livePositions := projection.List(ModeLive, "account-a", "manual")
	if len(testPositions) != 1 || len(livePositions) != 1 || testPositions[0].AverageEntryPrice != "100" || livePositions[0].AverageEntryPrice != "200" {
		t.Fatalf("test=%+v live=%+v", testPositions, livePositions)
	}
}

func TestProjectionRejectsConflictingFillReplay(t *testing.T) {
	projection := NewProjection()
	fill := Fill{
		FillID: "fill-1", CommandID: "command-1", LineageID: "lineage-a", ProductID: "manual", Mode: ModeTest,
		AccountID: "account-a", InstrumentID: "BTC-USDT", Side: SideBuy, Quantity: "1", Price: "100",
		Fee: "0", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: time.Now().UTC(),
	}
	if _, _, err := projection.ApplyFill(context.Background(), fill); err != nil {
		t.Fatal(err)
	}
	fill.Price = "101"
	if _, _, err := projection.ApplyFill(context.Background(), fill); !errors.Is(err, ErrFillConflict) {
		t.Fatalf("conflicting fill replay error=%v, want ErrFillConflict", err)
	}
}

func TestPersistentProjectionAccumulatesAfterRestartWithoutReapplyingFills(t *testing.T) {
	store := newRestartStore()
	base := Fill{
		FillID: "fill-1", CommandID: "command-1", LineageID: "lineage-a", ProductID: "manual", Mode: ModeTest,
		AccountID: "account-a", InstrumentID: "BTC-USDT", Side: SideBuy, Quantity: "2", Price: "100",
		Fee: "1", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: time.Now().UTC(),
	}
	first := NewPersistentProjection(store)
	if _, _, err := first.ApplyFill(context.Background(), base); err != nil {
		t.Fatal(err)
	}

	restarted := NewPersistentProjection(store)
	next := base
	next.FillID, next.CommandID, next.Quantity, next.Price = "fill-2", "command-2", "1", "200"
	position, _, err := restarted.ApplyFill(context.Background(), next)
	if err != nil {
		t.Fatal(err)
	}
	if position.Quantity != "3" || position.AverageEntryPrice != "133.3333333333333333" {
		t.Fatalf("restart accumulation position=%+v", position)
	}

	replayed := NewPersistentProjection(store)
	position, _, err = replayed.ApplyFill(context.Background(), next)
	if err != nil {
		t.Fatal(err)
	}
	if position.Quantity != "3" || position.AverageEntryPrice != "133.3333333333333333" {
		t.Fatalf("replayed fill changed position=%+v", position)
	}
	positions := replayed.List(ModeTest, "account-a", "manual")
	if len(positions) != 1 || positions[0].Quantity != "3" {
		t.Fatalf("restart positions=%+v", positions)
	}
	marked, err := replayed.ApplyMark(ModeTest, "account-a", "manual", "lineage-a", "BTC-USDT", "133.3333333333333333", base.OccurredAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if marked.Quantity != "3" || marked.MarkPrice != "133.3333333333333333" || marked.UnrealizedPnL != "0" {
		t.Fatalf("restart mark=%+v", marked)
	}
	conflicting := next
	conflicting.Price = "201"
	if _, _, err := NewPersistentProjection(store).ApplyFill(context.Background(), conflicting); !errors.Is(err, ErrFillConflict) {
		t.Fatalf("conflicting fill replay error=%v, want ErrFillConflict", err)
	}
}

type restartStore struct {
	mu        sync.Mutex
	positions map[string]Position
	fills     map[string]restartFill
}

type restartFill struct {
	hash   string
	result FillResult
}

func newRestartStore() *restartStore {
	return &restartStore{positions: make(map[string]Position), fills: make(map[string]restartFill)}
}

func (store *restartStore) ApplyFill(_ context.Context, fill Fill, reduce func(Position) (Position, FillResult, error)) (Position, FillResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := positionKey(string(fill.Mode), fill.AccountID, fill.ProductID, fill.LineageID, fill.InstrumentID)
	if recorded, ok := store.fills[fill.FillID]; ok {
		if recorded.hash != FillFingerprint(fill) {
			return Position{}, FillResult{}, ErrFillConflict
		}
		return store.positions[key], recorded.result, nil
	}
	position, result, err := reduce(store.positions[key])
	if err != nil {
		return Position{}, FillResult{}, err
	}
	store.positions[key] = position
	store.fills[fill.FillID] = restartFill{hash: FillFingerprint(fill), result: result}
	return position, result, nil
}

func (store *restartStore) ApplyMark(_ context.Context, mode Mode, accountID, productID, lineageID, instrumentID string, reduce func(Position) (Position, error)) (Position, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := positionKey(string(mode), accountID, productID, lineageID, instrumentID)
	position, ok := store.positions[key]
	if !ok {
		return Position{}, errors.New("position not found")
	}
	position, err := reduce(position)
	if err != nil {
		return Position{}, err
	}
	store.positions[key] = position
	return position, nil
}

func (store *restartStore) Upsert(_ context.Context, item Position) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.positions[positionKey(string(item.Mode), item.AccountID, item.ProductID, item.LineageID, item.InstrumentID)] = item
	return nil
}

func (store *restartStore) List(_ context.Context, mode Mode, accountID, productID string) ([]Position, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	items := make([]Position, 0)
	for _, item := range store.positions {
		if item.Mode == mode && (accountID == "" || item.AccountID == accountID) && (productID == "" || item.ProductID == productID) {
			items = append(items, item)
		}
	}
	return items, nil
}

type failingListStore struct {
	err     error
	context context.Context
}

func (store *failingListStore) ApplyFill(context.Context, Fill, func(Position) (Position, FillResult, error)) (Position, FillResult, error) {
	return Position{}, FillResult{}, nil
}
func (store *failingListStore) ApplyMark(context.Context, Mode, string, string, string, string, func(Position) (Position, error)) (Position, error) {
	return Position{}, nil
}
func (store *failingListStore) Upsert(context.Context, Position) error { return nil }
func (store *failingListStore) List(ctx context.Context, _ Mode, _, _ string) ([]Position, error) {
	store.context = ctx
	return nil, store.err
}

func TestPersistentProjectionListContextPreservesRepositoryFailureAndContext(t *testing.T) {
	cause := errors.New("portfolio repository unavailable")
	store := &failingListStore{err: cause}
	projection := NewPersistentProjection(store)
	type contextKey string
	ctx := context.WithValue(context.Background(), contextKey("request"), "request-42")

	items, err := projection.ListContext(ctx, ModeTest, "account-a", "manual")
	if !errors.Is(err, cause) || items != nil {
		t.Fatalf("ListContext() items=%v err=%v, want repository cause", items, err)
	}
	if got := store.context.Value(contextKey("request")); got != "request-42" {
		t.Fatalf("repository context value=%v, want request-42", got)
	}
}
