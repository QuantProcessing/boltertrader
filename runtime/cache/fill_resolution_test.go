package cache

import (
	"errors"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

func TestResolveOrderForFillUsesTypedAliasNamespaces(t *testing.T) {
	c := New()
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	clientCollision := fillResolutionOrder("acct", id, "shared", "venue-a", enums.SideBuy)
	venueMatch := fillResolutionOrder("acct", id, "client-b", "shared", enums.SideBuy)
	c.UpsertOrder(clientCollision)
	c.UpsertOrder(venueMatch)

	got, ok, err := c.ResolveOrderForFill("acct", model.Fill{
		AccountID:    "acct",
		InstrumentID: id,
		VenueOrderID: "shared",
		Side:         enums.SideBuy,
	})
	if err != nil {
		t.Fatalf("resolve venue-only fill: %v", err)
	}
	if !ok || got.Request.ClientID != venueMatch.Request.ClientID {
		t.Fatalf("resolved=(%+v,%v), want venue namespace order %q", got, ok, venueMatch.Request.ClientID)
	}
}

func TestUpsertOrderKeepsTypedAliasNamespaceCollisionsInBothInsertionOrders(t *testing.T) {
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	clientCollision := fillResolutionOrder("acct", id, "shared", "venue-a", enums.SideBuy)
	venueCollision := fillResolutionOrder("acct", id, "client-b", "shared", enums.SideBuy)

	for _, test := range []struct {
		name   string
		orders []model.Order
	}{
		{name: "client namespace first", orders: []model.Order{clientCollision, venueCollision}},
		{name: "venue namespace first", orders: []model.Order{venueCollision, clientCollision}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := New()
			for _, order := range test.orders {
				c.UpsertOrder(order)
			}
			if got := len(c.Orders()); got != 2 {
				t.Fatalf("orders=%d, want two typed-namespace records", got)
			}
			byClient, ok, err := c.ResolveOrderForFill("acct", model.Fill{
				AccountID: "acct", InstrumentID: id, ClientID: "shared", Side: enums.SideBuy,
			})
			if err != nil || !ok || byClient.VenueOrderID != "venue-a" {
				t.Fatalf("client resolution=(%+v,%v) err=%v", byClient, ok, err)
			}
			byVenue, ok, err := c.ResolveOrderForFill("acct", model.Fill{
				AccountID: "acct", InstrumentID: id, VenueOrderID: "shared", Side: enums.SideBuy,
			})
			if err != nil || !ok || byVenue.Request.ClientID != "client-b" {
				t.Fatalf("venue resolution=(%+v,%v) err=%v", byVenue, ok, err)
			}
		})
	}
}

func TestUpsertOrderKeepsClientOnlyAndVenueOnlySameTextSeparate(t *testing.T) {
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	venueOnly := fillResolutionOrder("acct", id, "", "shared", enums.SideBuy)
	clientOnly := fillResolutionOrder("acct", id, "shared", "", enums.SideBuy)
	c := New()
	c.UpsertOrder(venueOnly)
	c.UpsertOrder(clientOnly)
	if got := len(c.Orders()); got != 2 {
		t.Fatalf("orders=%d, want client-only and venue-only identities stored separately", got)
	}
	byClient, ok, err := c.ResolveOrderForFill("acct", model.Fill{AccountID: "acct", InstrumentID: id, ClientID: "shared", Side: enums.SideBuy})
	if err != nil || !ok || byClient.Request.ClientID != "shared" || byClient.VenueOrderID != "" {
		t.Fatalf("client-only resolution=(%+v,%v) err=%v", byClient, ok, err)
	}
	byVenue, ok, err := c.ResolveOrderForFill("acct", model.Fill{AccountID: "acct", InstrumentID: id, VenueOrderID: "shared", Side: enums.SideBuy})
	if err != nil || !ok || byVenue.Request.ClientID != "" || byVenue.VenueOrderID != "shared" {
		t.Fatalf("venue-only resolution=(%+v,%v) err=%v", byVenue, ok, err)
	}
}

func TestUpsertOrderRejectsCrossPairedCompleteAliases(t *testing.T) {
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	first := fillResolutionOrder("acct", id, "client-a", "venue-a", enums.SideBuy)
	second := fillResolutionOrder("acct", id, "client-b", "venue-b", enums.SideBuy)
	c := New()
	c.UpsertOrder(first)
	c.UpsertOrder(second)
	c.UpsertOrder(fillResolutionOrder("acct", id, "client-a", "venue-b", enums.SideBuy))
	if got := len(c.Orders()); got != 2 {
		t.Fatalf("orders=%d, want conflicting bridge ignored without collapsing records", got)
	}
	for _, order := range []model.Order{first, second} {
		got, ok, err := c.ResolveOrderForFill("acct", model.Fill{
			AccountID: "acct", InstrumentID: id, ClientID: order.Request.ClientID, VenueOrderID: order.VenueOrderID, Side: enums.SideBuy,
		})
		if err != nil || !ok || got.Request.ClientID != order.Request.ClientID || got.VenueOrderID != order.VenueOrderID {
			t.Fatalf("resolved=(%+v,%v) err=%v, want original order %+v", got, ok, err, order)
		}
	}
}

func TestResolveOrderForFillRejectsConflictingKnownIdentity(t *testing.T) {
	c := New()
	btc := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	eth := model.InstrumentID{Venue: "T", Symbol: "ETH-USDT", Kind: enums.KindPerp}
	first := fillResolutionOrder("acct", btc, "client-a", "venue-a", enums.SideBuy)
	second := fillResolutionOrder("acct", btc, "client-b", "venue-b", enums.SideBuy)
	c.UpsertOrder(first)
	c.UpsertOrder(second)

	tests := []struct {
		name string
		fill model.Fill
	}{
		{
			name: "aliases resolve to different orders",
			fill: model.Fill{AccountID: "acct", InstrumentID: btc, ClientID: "client-a", VenueOrderID: "venue-b", Side: enums.SideBuy},
		},
		{
			name: "wrong venue for known client",
			fill: model.Fill{AccountID: "acct", InstrumentID: btc, ClientID: "client-a", VenueOrderID: "venue-other", Side: enums.SideBuy},
		},
		{
			name: "wrong client for known venue",
			fill: model.Fill{AccountID: "acct", InstrumentID: btc, ClientID: "client-other", VenueOrderID: "venue-a", Side: enums.SideBuy},
		},
		{
			name: "wrong instrument",
			fill: model.Fill{AccountID: "acct", InstrumentID: eth, ClientID: "client-a", VenueOrderID: "venue-a", Side: enums.SideBuy},
		},
		{
			name: "partial instrument is not missing",
			fill: model.Fill{AccountID: "acct", InstrumentID: model.InstrumentID{Venue: "OTHER"}, ClientID: "client-a", VenueOrderID: "venue-a", Side: enums.SideBuy},
		},
		{
			name: "wrong side",
			fill: model.Fill{AccountID: "acct", InstrumentID: btc, ClientID: "client-a", VenueOrderID: "venue-a", Side: enums.SideSell},
		},
		{
			name: "wrong runtime account",
			fill: model.Fill{AccountID: "other", InstrumentID: btc, ClientID: "client-a", VenueOrderID: "venue-a", Side: enums.SideBuy},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, ok, err := c.ResolveOrderForFill("acct", tt.fill); !errors.Is(err, ErrFillOrderIdentityConflict) || ok {
				t.Fatalf("resolved=(%+v,%v) err=%v, want identity conflict", got, ok, err)
			}
		})
	}
}

func TestResolveOrderForFillAllowsMissingAliasEnrichment(t *testing.T) {
	c := New()
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	clientOnly := fillResolutionOrder("acct", id, "client-a", "", enums.SideBuy)
	c.UpsertOrder(clientOnly)

	got, ok, err := c.ResolveOrderForFill("acct", model.Fill{
		AccountID:    "acct",
		InstrumentID: id,
		ClientID:     "client-a",
		VenueOrderID: "venue-learned",
		Side:         enums.SideBuy,
	})
	if err != nil || !ok || got.Request.ClientID != clientOnly.Request.ClientID || got.VenueOrderID != "venue-learned" {
		t.Fatalf("resolved=(%+v,%v) err=%v, want client-only order enrichment", got, ok, err)
	}
	if _, ok, err = c.ResolveOrderForFill("acct", model.Fill{
		AccountID:    "acct",
		InstrumentID: id,
		VenueOrderID: "venue-learned",
		Side:         enums.SideBuy,
	}); err != nil || ok {
		t.Fatalf("read-only resolution persisted venue alias: ok=%v err=%v", ok, err)
	}
	got, ok, err = c.CommitOrderIdentityForFill("acct", model.Fill{
		AccountID:    "acct",
		InstrumentID: id,
		ClientID:     "client-a",
		VenueOrderID: "venue-learned",
		Side:         enums.SideBuy,
	})
	if err != nil || !ok || got.VenueOrderID != "venue-learned" {
		t.Fatalf("committed resolution=(%+v,%v) err=%v", got, ok, err)
	}
	got, ok, err = c.ResolveOrderForFill("acct", model.Fill{
		AccountID:    "acct",
		InstrumentID: id,
		VenueOrderID: "venue-learned",
		Side:         enums.SideBuy,
	})
	if err != nil || !ok || got.Request.ClientID != clientOnly.Request.ClientID {
		t.Fatalf("venue-only resolved=(%+v,%v) err=%v, want persisted enriched order", got, ok, err)
	}
}

func TestTypedOrderLookupsDisambiguateEqualClientAndVenueText(t *testing.T) {
	c := New()
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	clientOrder := fillResolutionOrder("acct", id, "shared", "venue-a", enums.SideBuy)
	venueOrder := fillResolutionOrder("acct", id, "client-b", "shared", enums.SideSell)
	c.UpsertOrder(clientOrder)
	c.UpsertOrder(venueOrder)

	if _, ok := c.OrderForAccount("acct", "shared"); ok {
		t.Fatal("untyped lookup must remain ambiguous")
	}
	if got, ok := c.OrderByClientIDForAccount("acct", "shared"); !ok || got.Request.ClientID != clientOrder.Request.ClientID {
		t.Fatalf("client lookup=(%+v,%v), want client namespace order", got, ok)
	}
	if got, ok := c.OrderByVenueOrderIDForAccount("acct", "shared"); !ok || got.Request.ClientID != venueOrder.Request.ClientID {
		t.Fatalf("venue lookup=(%+v,%v), want venue namespace order", got, ok)
	}
}

func fillResolutionOrder(accountID string, id model.InstrumentID, clientID, venueOrderID string, side enums.OrderSide) model.Order {
	return model.Order{
		Request: model.OrderRequest{
			AccountID:    accountID,
			InstrumentID: id,
			ClientID:     clientID,
			Side:         side,
			Quantity:     decimal.NewFromInt(10),
			Price:        decimal.NewFromInt(100),
		},
		VenueOrderID: venueOrderID,
		Status:       enums.StatusNew,
	}
}
