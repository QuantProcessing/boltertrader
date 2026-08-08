package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/accounts"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/catalog"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/marketdata"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/portfolio"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/reservations"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/work"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/accounting"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/planning"
	fundingpolicy "github.com/QuantProcessing/boltertrader/backend/internal/products/funding/policy"
)

type catalogFixture struct{ snapshot catalog.Snapshot }

func (source *catalogFixture) Snapshot(context.Context) (catalog.Snapshot, error) {
	return source.snapshot, nil
}
func (source *catalogFixture) Status() catalog.Status {
	return catalog.Status{Ready: true, Revision: source.snapshot.Revision}
}

type marketFixture map[marketdata.Query]marketdata.Fact

func (source marketFixture) Get(_ context.Context, query marketdata.Query) (marketdata.Fact, error) {
	item, ok := source[query]
	if !ok {
		return marketdata.Fact{}, errors.New("market fact not found")
	}
	return item, nil
}

type accountFixture map[string]accounts.Account

func (source accountFixture) Get(_ context.Context, id string) (accounts.Account, error) {
	item, ok := source[id]
	if !ok {
		return accounts.Account{}, accounts.ErrNotFound
	}
	return item, nil
}
func (source accountFixture) List(context.Context) ([]accounts.Account, error) {
	items := make([]accounts.Account, 0, len(source))
	for _, item := range source {
		items = append(items, item)
	}
	return items, nil
}

type reservationFixture map[string]reservations.Reservation

func (source reservationFixture) Get(_ context.Context, id string) (reservations.Reservation, error) {
	item, ok := source[id]
	if !ok {
		return reservations.Reservation{}, reservations.ErrNotFound
	}
	return item, nil
}
func (source reservationFixture) List(_ context.Context, principal string, mode reservations.Mode) ([]reservations.Reservation, error) {
	items := []reservations.Reservation{}
	for _, item := range source {
		if item.PrincipalID == principal && item.Mode == mode {
			items = append(items, item)
		}
	}
	return items, nil
}

type mismatchedFundingExecutionStore struct {
	work.FundingExecutionStore
	execution work.FundingExecution
}

func (store mismatchedFundingExecutionStore) GetFundingExecution(context.Context, string, orders.Mode, string) (work.FundingExecution, error) {
	return store.execution, nil
}

type fundingPositionFixture []portfolio.Position

func (fixture fundingPositionFixture) ListContext(_ context.Context, principalID string, _ portfolio.Mode, _, _ string) ([]portfolio.Position, error) {
	result := make([]portfolio.Position, 0, len(fixture))
	for _, item := range fixture {
		if item.PrincipalID == "" || item.PrincipalID == principalID {
			result = append(result, item)
		}
	}
	return result, nil
}

func TestSharedFundingProjectionBindsCompositeFactsAndRevalidatesAuthority(t *testing.T) {
	now := time.Date(2026, 8, 6, 2, 30, 0, 0, time.UTC)
	catalogSource := &catalogFixture{snapshot: fundingCatalog(now)}
	marketSource := fundingMarkets(now)
	accountSource := accountFixture{
		"account-long":  {ID: "account-long", Venue: "venue-a", VenueEnvironment: accounts.EnvironmentTestnet, ProductIDs: []string{"funding"}, DesiredStateRevision: 11, RuntimeGeneration: 4, CredentialVersion: 3, State: accounts.StateActive},
		"account-short": {ID: "account-short", Venue: "venue-b", VenueEnvironment: accounts.EnvironmentTestnet, ProductIDs: []string{"funding"}, DesiredStateRevision: 13, RuntimeGeneration: 7, CredentialVersion: 5, State: accounts.StateActive},
	}
	reservationSource := reservationFixture{
		"reservation-long":        exactFundingReservation("reservation-long", "browser-a", reservations.KindOpen, "account-long", "venue-a", "btc-a", "0.5", "50", "USDT", now),
		"reservation-short":       exactFundingReservation("reservation-short", "browser-a", reservations.KindOpen, "account-short", "venue-b", "btc-b", "0.5", "50", "USDT", now),
		"reservation-close-long":  fundingReservation("reservation-close-long", "browser-a", reservations.KindClose, "account-long", "venue-a", "btc-a", "50", now),
		"reservation-close-short": fundingReservation("reservation-close-short", "browser-a", reservations.KindClose, "account-short", "venue-b", "btc-b", "50", now),
		"reservation-b-long":      exactFundingReservation("reservation-b-long", "browser-b", reservations.KindOpen, "account-long", "venue-a", "btc-a", "0.5", "50", "USDT", now),
		"reservation-b-short":     exactFundingReservation("reservation-b-short", "browser-b", reservations.KindOpen, "account-short", "venue-b", "btc-b", "0.5", "50", "USDT", now),
	}
	projection, err := NewSharedFundingProjection(SharedFundingProjectionOptions{Catalog: catalogSource, MarketData: marketSource, Accounts: accountSource, Reservations: reservationSource, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	opportunities, err := projection.ListOpportunities(context.Background(), "browser-a", "test")
	if err != nil || len(opportunities) != 1 {
		t.Fatalf("ListOpportunities()=%+v err=%v", opportunities, err)
	}
	opportunity := opportunities[0]
	preview, err := projection.CreatePreview(context.Background(), "browser-a", FundingPreviewRequest{OpportunityID: opportunity.OpportunityID, LongAccountID: "account-long", ShortAccountID: "account-short", CanonicalMarketID: opportunity.CanonicalMarketID, NotionalPerLeg: "50", MaxSlippageBps: 10, HoldThroughSettlement: true, Mode: "test"})
	if err != nil || !preview.CanSubmit || preview.PrincipalID != "browser-a" || preview.Mode != "test" || preview.CatalogRevision != "catalog-rev-1" || preview.FactsRevision == "" || len(preview.Facts) != 0 || len(preview.ReservationRequirements) != 2 || !preview.ExpiresAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("CreatePreview()=%+v err=%v", preview, err)
	}
	finalized, err := projection.GetPreview(context.Background(), "browser-a", preview.PreviewID)
	if err != nil || len(finalized.Facts) != 1 || finalized.Facts[0].Cursor == "" || len(finalized.WorkPayload) == 0 {
		t.Fatalf("GetPreview()=%+v err=%v", finalized, err)
	}
	var frozen planning.PlanningFact
	if err := json.Unmarshal(finalized.Facts[0].Payload, &frozen); err != nil || len(frozen.ReservationAuthorities) != 2 || len(frozen.PolicyAssumptions) != 2 || frozen.ReservationAuthorities[0].Revision.IsZero() {
		t.Fatalf("frozen planning authority=%+v err=%v", frozen, err)
	}
	if _, err := projection.GetPreview(context.Background(), "browser-b", preview.PreviewID); !errors.Is(err, ErrFundingPreviewStale) {
		t.Fatalf("cross-principal GetPreview() err=%v", err)
	}
	if _, err := projection.GetPreview(context.Background(), "browser-a", preview.PreviewID); err != nil {
		t.Fatalf("GetPreview() err=%v", err)
	}
	browserBPreview, err := projection.CreatePreview(context.Background(), "browser-b", FundingPreviewRequest{OpportunityID: opportunity.OpportunityID, LongAccountID: "account-long", ShortAccountID: "account-short", CanonicalMarketID: opportunity.CanonicalMarketID, NotionalPerLeg: "50", MaxSlippageBps: 10, Mode: "test"})
	if err != nil || browserBPreview.PreviewID == preview.PreviewID {
		t.Fatalf("principal authority identity collided: a=%s b=%s err=%v", preview.PreviewID, browserBPreview.PreviewID, err)
	}
	closeAuthority, err := projection.CreateCloseAuthority(context.Background(), "browser-a", "test", preview.PreviewID, "lineage-a")
	if err != nil || len(closeAuthority.Facts) != 2 || closeAuthority.Facts[0].Kind != "funding.unwind-reservation" || closeAuthority.Facts[0].EffectiveAt != now || !bytes.Contains(closeAuthority.WorkPayload, []byte(`"requestedAt":"2026-08-06T02:30:00Z"`)) {
		t.Fatalf("CreateCloseAuthority()=%+v err=%v", closeAuthority, err)
	}

	shortQuery := marketdata.Query{Venue: "venue-b", Environment: "testnet", Product: "perp", InstrumentID: "btc-b"}
	shortFact := marketSource[shortQuery]
	shortFact.FundingRate = "0.0012"
	marketSource[shortQuery] = shortFact
	if _, err := projection.GetPreview(context.Background(), "browser-a", preview.PreviewID); !errors.Is(err, ErrFundingPreviewStale) {
		t.Fatalf("changed facts GetPreview() err=%v", err)
	}
	shortFact.FundingRate = "0.0011"
	marketSource[shortQuery] = shortFact

	longAccount := accountSource["account-long"]
	longAccount.DesiredStateRevision++
	accountSource["account-long"] = longAccount
	if _, err := projection.GetPreview(context.Background(), "browser-a", preview.PreviewID); !errors.Is(err, ErrFundingPreviewStale) {
		t.Fatalf("changed account GetPreview() err=%v", err)
	}
	longAccount.DesiredStateRevision--
	accountSource["account-long"] = longAccount

	longReservation := reservationSource["reservation-long"]
	longReservation.Status = reservations.StatusReleased
	reservationSource["reservation-long"] = longReservation
	if _, err := projection.GetPreview(context.Background(), "browser-a", preview.PreviewID); !errors.Is(err, ErrFundingPreviewStale) {
		t.Fatalf("released reservation GetPreview() err=%v", err)
	}
	longReservation.Status = reservations.StatusHeld
	reservationSource["reservation-long"] = longReservation
	longReservation.UpdatedAt = longReservation.UpdatedAt.Add(time.Second)
	reservationSource["reservation-long"] = longReservation
	if _, err := projection.GetPreview(context.Background(), "browser-a", preview.PreviewID); !errors.Is(err, ErrFundingPreviewStale) {
		t.Fatalf("changed reservation revision GetPreview() err=%v", err)
	}
	longReservation.UpdatedAt = longReservation.UpdatedAt.Add(-time.Second)
	reservationSource["reservation-long"] = longReservation

	catalogSource.snapshot.Revision = "catalog-rev-2"
	if _, err := projection.GetPreview(context.Background(), "browser-a", preview.PreviewID); !errors.Is(err, ErrFundingPreviewStale) {
		t.Fatalf("changed catalog GetPreview() err=%v", err)
	}
	catalogSource.snapshot.Revision = "catalog-rev-1"
	now = now.Add(30 * time.Second)
	if _, err := projection.GetPreview(context.Background(), "browser-a", preview.PreviewID); !errors.Is(err, ErrFundingPreviewStale) {
		t.Fatalf("expired GetPreview() err=%v", err)
	}
	if _, err := projection.CreatePreview(context.Background(), "browser-a", FundingPreviewRequest{OpportunityID: opportunity.OpportunityID, LongAccountID: "account-long", ShortAccountID: "account-short", CanonicalMarketID: opportunity.CanonicalMarketID, NotionalPerLeg: "50", Mode: "live"}); err == nil {
		t.Fatal("CreatePreview() accepted cross-mode accounts and facts")
	}
}

func TestSharedFundingProjectionEvictsExpiredAndStalePreviewAuthority(t *testing.T) {
	now := time.Date(2026, 8, 8, 8, 0, 0, 0, time.UTC)
	catalogSource := &catalogFixture{snapshot: fundingCatalog(now)}
	marketSource := fundingMarkets(now)
	accountSource := accountFixture{
		"account-long":  {ID: "account-long", Venue: "venue-a", VenueEnvironment: accounts.EnvironmentTestnet, ProductIDs: []string{"funding"}, State: accounts.StateActive, DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1},
		"account-short": {ID: "account-short", Venue: "venue-b", VenueEnvironment: accounts.EnvironmentTestnet, ProductIDs: []string{"funding"}, State: accounts.StateActive, DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1},
	}
	reservationSource := reservationFixture{
		"reservation-long":  fundingReservation("reservation-long", "browser-a", reservations.KindOpen, "account-long", "venue-a", "btc-a", "50", now),
		"reservation-short": fundingReservation("reservation-short", "browser-a", reservations.KindOpen, "account-short", "venue-b", "btc-b", "50", now),
	}
	projectionAPI, err := NewSharedFundingProjection(SharedFundingProjectionOptions{Catalog: catalogSource, MarketData: marketSource, Accounts: accountSource, Reservations: reservationSource, Admission: fundingpolicy.DefaultAdmission(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	projection := projectionAPI.(*sharedFundingProjection)
	opportunities, err := projection.ListOpportunities(context.Background(), "browser-a", "test")
	if err != nil || len(opportunities) == 0 {
		t.Fatalf("ListOpportunities()=%+v error=%v", opportunities, err)
	}
	request := FundingPreviewRequest{OpportunityID: opportunities[0].OpportunityID, LongAccountID: "account-long", ShortAccountID: "account-short", CanonicalMarketID: opportunities[0].CanonicalMarketID, NotionalPerLeg: "50", MaxSlippageBps: 10, HoldThroughSettlement: true, Mode: "test"}
	preview, err := projection.CreatePreview(context.Background(), "browser-a", request)
	if err != nil {
		t.Fatal(err)
	}
	now = preview.ExpiresAt
	if _, err := projection.GetPreview(context.Background(), "browser-a", preview.PreviewID); !errors.Is(err, ErrFundingPreviewStale) {
		t.Fatalf("expired GetPreview() error=%v", err)
	}
	if len(projection.previews) != 0 {
		t.Fatalf("expired preview retained: %d", len(projection.previews))
	}

	now = now.Add(time.Second)
	for query, fact := range fundingMarkets(now) {
		marketSource[query] = fact
	}
	preview, err = projection.CreatePreview(context.Background(), "browser-a", request)
	if err != nil {
		t.Fatal(err)
	}
	changed := accountSource["account-long"]
	changed.DesiredStateRevision++
	accountSource["account-long"] = changed
	if _, err := projection.GetPreview(context.Background(), "browser-a", preview.PreviewID); !errors.Is(err, ErrFundingPreviewStale) {
		t.Fatalf("stale GetPreview() error=%v", err)
	}
	if len(projection.previews) != 0 {
		t.Fatalf("stale preview retained: %d", len(projection.previews))
	}
}

func TestSharedFundingProjectionPublishesReservationRequirementsAndClosesFromDurableState(t *testing.T) {
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	catalogSource := &catalogFixture{snapshot: fundingCatalog(now)}
	marketSource := fundingMarkets(now)
	accountSource := accountFixture{
		"account-long":  {ID: "account-long", Venue: "venue-a", VenueEnvironment: accounts.EnvironmentTestnet, ProductIDs: []string{"funding"}, State: accounts.StateActive, DesiredStateRevision: 11, RuntimeGeneration: 4, CredentialVersion: 3},
		"account-short": {ID: "account-short", Venue: "venue-b", VenueEnvironment: accounts.EnvironmentTestnet, ProductIDs: []string{"funding"}, State: accounts.StateActive, DesiredStateRevision: 13, RuntimeGeneration: 7, CredentialVersion: 5},
	}
	reservationSource := reservationFixture{
		"open-long":  fundingReservation("open-long", "browser-a", reservations.KindOpen, "account-long", "venue-a", "btc-a", "50", now),
		"open-short": fundingReservation("open-short", "browser-a", reservations.KindOpen, "account-short", "venue-b", "btc-b", "50", now),
	}
	executions := work.NewMemoryFundingExecutionStore()
	positions := portfolio.NewProjection()
	accountingSink := &fundingAccountingFixture{positions: positions}
	projectionAPI, err := NewSharedFundingProjection(SharedFundingProjectionOptions{
		Catalog: catalogSource, MarketData: marketSource, Accounts: accountSource, Reservations: reservationSource,
		Executions: executions, Positions: positions, Accounting: accountingSink, Admission: fundingpolicy.DefaultAdmission(), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	sharedProjection := projectionAPI.(*sharedFundingProjection)
	opportunities, err := projectionAPI.ListOpportunities(context.Background(), "browser-a", "test")
	if err != nil || len(opportunities) != 1 {
		t.Fatalf("ListOpportunities()=%+v err=%v", opportunities, err)
	}
	opportunity := opportunities[0]
	if opportunity.LongAccountID != "account-long" || opportunity.ShortAccountID != "account-short" || opportunity.LongInstrumentID != "btc-a" || opportunity.ShortInstrumentID != "btc-b" || len(opportunity.ReservationRequirements) != 2 {
		t.Fatalf("opportunity lacks authoritative reservation scopes: %+v", opportunity)
	}
	for _, requirement := range opportunity.ReservationRequirements {
		if requirement.ProductID != "funding" || requirement.ReservationKind != reservations.KindOpen || requirement.Mode != "test" || requirement.NettingScope.AccountID == "" || requirement.NettingScope.InstrumentID == "" {
			t.Fatalf("reservation requirement=%+v", requirement)
		}
	}

	preview, err := projectionAPI.CreatePreview(context.Background(), "browser-a", FundingPreviewRequest{OpportunityID: opportunity.OpportunityID, LongAccountID: opportunity.LongAccountID, ShortAccountID: opportunity.ShortAccountID, CanonicalMarketID: opportunity.CanonicalMarketID, NotionalPerLeg: "50", MaxSlippageBps: 10, Mode: "test"})
	if err != nil || len(preview.ReservationRequirements) != 2 {
		t.Fatalf("CreatePreview()=%+v err=%v", preview, err)
	}
	openDrafts := []work.FundingOrderDraft{
		{LegID: "long", AccountID: "account-long", DesiredStateRevision: 11, RuntimeGeneration: 4, CredentialVersion: 3, Venue: "venue-a", VenueEnvironment: "testnet", ReservationID: "open-long", InstrumentID: "btc-a", Side: "buy", Quantity: "0.5", Notional: "50", OrderType: "market"},
		{LegID: "short", AccountID: "account-short", DesiredStateRevision: 13, RuntimeGeneration: 7, CredentialVersion: 5, Venue: "venue-b", VenueEnvironment: "testnet", ReservationID: "open-short", InstrumentID: "btc-b", Side: "sell", Quantity: "0.5", Notional: "50", OrderType: "market"},
	}
	_, _, err = executions.ReserveFundingExecution(context.Background(), work.FundingExecution{ExecutionID: "execution-lineage-a", PrincipalID: "browser-a", Mode: orders.ModeTest, LineageID: "lineage-a", ParentCommandID: "parent-open", PlanKey: "open:" + preview.PreviewID, PreviewHash: preview.FactsRevision, IntentKind: "funding.open", State: work.FundingExecutionHedged, Legs: []work.FundingExecutionLeg{{Role: "long", Draft: openDrafts[0], CommandID: "long-command", CommandState: orders.StateFilled, FilledQuantity: "0.5"}, {Role: "short", Draft: openDrafts[1], CommandID: "short-command", CommandState: orders.StateFilled, FilledQuantity: "0.5"}}, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	for _, fill := range []portfolio.Fill{
		{PrincipalID: "browser-a", FillID: "fill-long", CommandID: "long-command", LineageID: "lineage-a", ProductID: "funding", AccountID: "account-long", InstrumentID: "btc-a", Mode: portfolio.ModeTest, Side: portfolio.SideBuy, Quantity: "0.5", Price: "100", Fee: "0.01", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: now},
		{PrincipalID: "browser-a", FillID: "fill-short", CommandID: "short-command", LineageID: "lineage-a", ProductID: "funding", AccountID: "account-short", InstrumentID: "btc-b", Mode: portfolio.ModeTest, Side: portfolio.SideSell, Quantity: "0.5", Price: "100", Fee: "0.01", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: now},
	} {
		if _, _, err := positions.ApplyFill(context.Background(), fill); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sharedProjection.RefreshAccounting(context.Background(), "browser-a", "test"); err != nil {
		t.Fatalf("normal accounting refresh error=%v", err)
	}
	if len(accountingSink.requests) != 2 {
		t.Fatalf("normal accounting refresh requests=%+v", accountingSink.requests)
	}
	accountingSink.requests = nil

	now = preview.ExpiresAt.Add(time.Second)
	for query, fact := range fundingMarkets(now) {
		marketSource[query] = fact
	}
	closeRequirements, err := projectionAPI.GetCloseReservationRequirements(context.Background(), "browser-a", "test", "lineage-a")
	if err != nil || len(closeRequirements) != 2 {
		t.Fatalf("GetCloseReservationRequirements()=%+v err=%v", closeRequirements, err)
	}
	if len(accountingSink.requests) != 2 {
		t.Fatalf("authoritative accounting requests=%+v", accountingSink.requests)
	}
	for _, request := range accountingSink.requests {
		if request.PrincipalID != "browser-a" || request.RequestID != "funding-accounting:execution-lineage-a" || request.ProductID != "funding" || request.Mode != "test" || request.LineageID != "lineage-a" || request.PositionID != "execution-lineage-a" || request.SettlementAsset != "USDT" || !request.Start.Equal(preview.ExpiresAt.Add(-30*time.Second)) || !request.End.Equal(now) {
			t.Fatalf("accounting request=%+v", request)
		}
	}
	for _, requirement := range closeRequirements {
		if requirement.AccountID == "" || requirement.AccountID != requirement.NettingScope.AccountID || requirement.ProductID != "funding" || requirement.ReservationKind != reservations.KindClose || requirement.Mode != "test" || requirement.Quantity != "0.5" || requirement.Notional != "50" || requirement.Currency != "USDT" {
			t.Fatalf("close reservation requirement=%+v", requirement)
		}
	}
	reservationSource["close-long"] = exactFundingReservation("close-long", "browser-a", reservations.KindClose, "account-long", "venue-a", "btc-a", "0.5", "50", "USDT", now)
	reservationSource["close-short"] = exactFundingReservation("close-short", "browser-a", reservations.KindClose, "account-short", "venue-b", "btc-b", "0.5", "50", "USDT", now)
	closeAuthority, err := projectionAPI.CreateCloseAuthority(context.Background(), "browser-a", "test", "", "lineage-a")
	if err != nil || len(closeAuthority.Facts) != 2 || len(closeAuthority.AccountFences) == 0 || !bytes.Contains(closeAuthority.WorkPayload, []byte(`"lineageId":"lineage-a"`)) {
		t.Fatalf("CreateCloseAuthority()=%+v err=%v", closeAuthority, err)
	}
	if closeAuthority.PreviewID == preview.PreviewID || !closeAuthority.ExpiresAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("close authority reused expired open preview: %+v", closeAuthority)
	}
	storedExecution, err := executions.GetFundingExecution(context.Background(), "browser-a", orders.ModeTest, "lineage-a")
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*work.FundingExecution){
		"principal": func(item *work.FundingExecution) { item.PrincipalID = "browser-b" },
		"mode":      func(item *work.FundingExecution) { item.Mode = orders.ModeLive },
		"lineage":   func(item *work.FundingExecution) { item.LineageID = "lineage-b" },
	} {
		t.Run("returned execution "+name, func(t *testing.T) {
			changed := storedExecution
			mutate(&changed)
			sharedProjection.options.Executions = mismatchedFundingExecutionStore{FundingExecutionStore: executions, execution: changed}
			t.Cleanup(func() { sharedProjection.options.Executions = executions })
			if _, err := projectionAPI.GetCloseReservationRequirements(context.Background(), "browser-a", "test", "lineage-a"); !errors.Is(err, ErrFundingPreviewStale) {
				t.Fatalf("GetCloseReservationRequirements() error=%v, want ErrFundingPreviewStale", err)
			}
		})
	}
	currentPositions, err := positions.ListContext(context.Background(), "browser-a", portfolio.ModeTest, "", "funding")
	if err != nil || len(currentPositions) != 2 {
		t.Fatalf("positions=%+v err=%v", currentPositions, err)
	}
	reversed := append([]portfolio.Position(nil), currentPositions...)
	for index := range reversed {
		if reversed[index].AccountID == "account-long" {
			reversed[index].Quantity = "-0.5"
		}
	}
	sharedProjection.options.Positions = fundingPositionFixture(reversed)
	if _, err := projectionAPI.GetCloseReservationRequirements(context.Background(), "browser-a", "test", "lineage-a"); !errors.Is(err, ErrFundingPreviewStale) {
		t.Fatalf("reversed position GetCloseReservationRequirements() error=%v, want ErrFundingPreviewStale", err)
	}
	sharedProjection.options.Positions = positions

	positionMode := "one-way"
	for name, mutate := range map[string]func(*reservations.Reservation){
		"missing quantity": func(item *reservations.Reservation) { item.Quantity = nil },
		"larger quantity":  func(item *reservations.Reservation) { value := "0.6"; item.Quantity = &value },
		"larger notional":  func(item *reservations.Reservation) { value := "60"; item.Notional = &value },
		"wrong currency":   func(item *reservations.Reservation) { value := "USD"; item.Currency = &value },
		"extra scope":      func(item *reservations.Reservation) { item.Scope.PositionMode = &positionMode },
	} {
		t.Run(name, func(t *testing.T) {
			original := reservationSource["close-long"]
			changed := original
			mutate(&changed)
			reservationSource["close-long"] = changed
			t.Cleanup(func() { reservationSource["close-long"] = original })
			if _, err := projectionAPI.CreateCloseAuthority(context.Background(), "browser-a", "test", "", "lineage-a"); !errors.Is(err, ErrFundingPreviewStale) {
				t.Fatalf("CreateCloseAuthority() error=%v, want ErrFundingPreviewStale", err)
			}
		})
	}
}

type fundingAccountingFixture struct {
	positions *portfolio.Projection
	requests  []accounting.IngestionRequest
	err       error
}

func (fixture *fundingAccountingFixture) Ingest(ctx context.Context, request accounting.IngestionRequest) error {
	fixture.requests = append(fixture.requests, request)
	if fixture.err != nil {
		return fixture.err
	}
	_, err := fixture.positions.ApplyMarkContext(ctx, request.PrincipalID, portfolio.Mode(request.Mode), request.AccountID, request.ProductID, request.LineageID, request.InstrumentID, "100", request.End)
	return err
}

func fundingReservation(id, principal string, kind reservations.Kind, account, venue, instrument, notional string, now time.Time) reservations.Reservation {
	currency := "USDT"
	return reservations.Reservation{ReservationID: id, PrincipalID: principal, Mode: reservations.ModeTest, ProductID: "funding", Kind: kind, Scope: reservations.NettingScope{AccountID: account, Venue: venue, InstrumentID: instrument, VenueEnvironment: "testnet"}, Notional: &notional, Currency: &currency, Status: reservations.StatusHeld, CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute)}
}

func exactFundingReservation(id, principal string, kind reservations.Kind, account, venue, instrument, quantity, notional, currency string, now time.Time) reservations.Reservation {
	return reservations.Reservation{ReservationID: id, PrincipalID: principal, Mode: reservations.ModeTest, ProductID: "funding", Kind: kind, Scope: reservations.NettingScope{AccountID: account, Venue: venue, InstrumentID: instrument, VenueEnvironment: "testnet"}, Quantity: &quantity, Notional: &notional, Currency: &currency, Status: reservations.StatusHeld, CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute)}
}

func fundingCatalog(now time.Time) catalog.Snapshot {
	rules := catalog.TradeRules{PriceIncrement: "0.01", QuantityIncrement: "0.001", MinQuantity: "0.001", MinNotional: "5"}
	return catalog.Snapshot{Revision: "catalog-rev-1", ObservedAt: now, Venues: []catalog.Venue{{Venue: "venue-a", Environment: "testnet", Ready: true}, {Venue: "venue-b", Environment: "testnet", Ready: true}}, Instruments: []catalog.Instrument{
		{InstrumentID: "btc-a", Venue: "venue-a", Environment: "testnet", Product: "perp", Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Rules: rules, Ready: true},
		{InstrumentID: "btc-b", Venue: "venue-b", Environment: "testnet", Product: "perp", Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Rules: rules, Ready: true},
	}}
}

func fundingMarkets(now time.Time) marketFixture {
	makeFact := func(venue, instrument, rate string) marketdata.Fact {
		return marketdata.Fact{Venue: venue, Environment: "testnet", Product: "perp", InstrumentID: instrument, BestBid: "99.9", BestBidSize: "10", BestAsk: "100.1", BestAskSize: "10", Bids: []marketdata.Level{{Price: "99.9", Quantity: "10"}}, Asks: []marketdata.Level{{Price: "100.1", Quantity: "10"}}, MarkPrice: "100", FundingRate: rate, FundingObservedAt: now.Add(-time.Second), FundingTime: now, NextFundingTime: now.Add(8 * time.Hour), FundingHistory: []marketdata.FundingPoint{{Rate: rate, ObservedAt: now.Add(-8 * time.Hour), FundingTime: now.Add(-8 * time.Hour)}, {Rate: rate, ObservedAt: now, FundingTime: now}}, ObservedAt: now.Add(-time.Second)}
	}
	return marketFixture{
		{Venue: "venue-a", Environment: "testnet", Product: "perp", InstrumentID: "btc-a"}: makeFact("venue-a", "btc-a", "0.0001"),
		{Venue: "venue-b", Environment: "testnet", Product: "perp", InstrumentID: "btc-b"}: makeFact("venue-b", "btc-b", "0.0011"),
	}
}
