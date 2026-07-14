package exec

import (
	"errors"
	"strings"
	"sync"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/orderstate"
	"github.com/shopspring/decimal"
)

type BufferedFill struct {
	Fill model.Fill
	Meta contract.EventMeta
}

type fillIndexKey struct {
	accountID string
	id        string
}

type FillBuffer struct {
	mu               sync.Mutex
	byClient         map[fillIndexKey][]BufferedFill
	byVenue          map[fillIndexKey][]BufferedFill
	seen             map[string]appliedFillRecord
	orderAliasGroups map[string]*appliedOrderGroup
	appliedOrder     []string
	maxAppliedFill   int
}

type appliedFillRecord struct {
	aliases appliedOrderAliases
	group   *appliedOrderGroup
}

// appliedOrderGroup is a mutex-confined union of client/venue aliases. Its
// cumulative quantity and aliases persist while the order is active. Once the
// order is terminal, the group expires after its last retained trade ID leaves
// the bounded dedupe window.
type appliedOrderGroup struct {
	parent         *appliedOrderGroup
	quantity       decimal.Decimal
	retained       int
	terminal       bool
	aliases        map[string]struct{}
	clientAlias    string
	venueAlias     string
	anonymousAlias string
}

type appliedOrderAliases struct {
	client    string
	venue     string
	anonymous string
}

func (a appliedOrderAliases) all() []string {
	aliases := make([]string, 0, 2)
	if a.client != "" {
		aliases = append(aliases, a.client)
	}
	if a.venue != "" {
		aliases = append(aliases, a.venue)
	}
	if a.anonymous != "" {
		aliases = append(aliases, a.anonymous)
	}
	return aliases
}

func (a appliedOrderAliases) sharesIdentity(other appliedOrderAliases) bool {
	return (a.client != "" && a.client == other.client) ||
		(a.venue != "" && a.venue == other.venue) ||
		(a.anonymous != "" && a.anonymous == other.anonymous)
}

// defaultAppliedFillLimit bounds the in-memory idempotency window while still
// being deliberately large enough to cover long reconnect/reconciliation
// bursts. Pending unmatched fills are held separately and are never evicted by
// this limit.
const defaultAppliedFillLimit = 100_000

// ErrFillOrderAliasConflict means one fill would bind a known client alias to
// a different known venue alias. The checked marking API fails closed so a bad
// event cannot contaminate cumulative coverage for two orders.
var ErrFillOrderAliasConflict = errors.New("exec: fill order alias conflict")

func NewFillBuffer() *FillBuffer {
	return NewFillBufferWithAppliedLimit(defaultAppliedFillLimit)
}

// NewFillBufferWithAppliedLimit creates a buffer whose applied-fill
// idempotency window contains at most limit distinct fill keys. Once a key
// leaves that FIFO window a very old duplicate can be applied again; callers
// that need a larger replay horizon should raise the limit. Cumulative active
// order coverage remains available until MarkOrderTerminal observes a terminal
// order and all of that order's retained keys leave the window. A non-positive
// limit uses the conservative production default.
func NewFillBufferWithAppliedLimit(limit int) *FillBuffer {
	if limit <= 0 {
		limit = defaultAppliedFillLimit
	}
	return &FillBuffer{
		byClient:         make(map[fillIndexKey][]BufferedFill),
		byVenue:          make(map[fillIndexKey][]BufferedFill),
		seen:             make(map[string]appliedFillRecord),
		orderAliasGroups: make(map[string]*appliedOrderGroup),
		maxAppliedFill:   limit,
	}
}

func (b *FillBuffer) MarkApplied(fill model.Fill) bool {
	applied, _ := b.MarkAppliedWithCoverage(fill)
	return applied
}

// MarkAppliedWithCoverage atomically marks a trade-id fill and returns the
// cumulative authoritative quantity already applied for the same active order.
func (b *FillBuffer) MarkAppliedWithCoverage(fill model.Fill) (bool, decimal.Decimal) {
	applied, prior, _ := b.MarkAppliedWithCoverageChecked(fill)
	return applied, prior
}

// MarkAppliedWithCoverageChecked is MarkAppliedWithCoverage with explicit
// identity-conflict reporting for runtime callers that must fail closed.
func (b *FillBuffer) MarkAppliedWithCoverageChecked(fill model.Fill) (bool, decimal.Decimal, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.markAppliedWithCoverageCheckedLocked(fill)
}

// AcceptAppliedWithCoverageChecked coordinates fill dedupe with the caller's
// authoritative cache commit. It validates without mutation, invokes accept,
// and only then records the fill while the FillBuffer lock is still held. If
// accept fails the buffer remains unchanged. accept must not call FillBuffer.
func (b *FillBuffer) AcceptAppliedWithCoverageChecked(
	fill model.Fill,
	accept func(applied bool, previouslyAppliedQty decimal.Decimal) error,
) (bool, decimal.Decimal, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	applied, prior, err := b.previewAppliedWithCoverageCheckedLocked(fill)
	if err != nil {
		return false, decimal.Zero, err
	}
	if accept != nil {
		if err := accept(applied, prior); err != nil {
			return false, decimal.Zero, err
		}
	}
	return b.markAppliedWithCoverageCheckedLocked(fill)
}

func (b *FillBuffer) markAppliedWithCoverageCheckedLocked(fill model.Fill) (bool, decimal.Decimal, error) {
	key := fillKey(fill)
	if key == "" {
		return true, decimal.Zero, nil
	}
	if record, ok := b.seen[key]; ok {
		if err := b.enrichAppliedOrderAliasesLocked(key, record, appliedFillOrderAliases(fill)); err != nil {
			return false, decimal.Zero, err
		}
		return false, decimal.Zero, nil
	}
	aliases := appliedFillOrderAliases(fill)
	group, err := b.resolveAppliedOrderGroupLocked(aliases)
	if err != nil {
		return false, decimal.Zero, err
	}
	prior := group.quantity
	qty := fill.Quantity
	if qty.IsNegative() {
		qty = decimal.Zero
	}
	group.quantity = prior.Add(qty)
	group.retained++
	b.retainOrderAliasesLocked(group, aliases)
	b.seen[key] = appliedFillRecord{aliases: aliases, group: group}
	b.appliedOrder = append(b.appliedOrder, key)
	for len(b.seen) > b.maxAppliedFill {
		oldest := b.appliedOrder[0]
		b.appliedOrder = b.appliedOrder[1:]
		record := b.seen[oldest]
		delete(b.seen, oldest)
		group := appliedOrderGroupRoot(record.group)
		group.retained--
		if group.terminal && group.retained <= 0 {
			b.deleteAppliedOrderGroupLocked(group)
		}
	}
	return true, prior, nil
}

func (b *FillBuffer) previewAppliedWithCoverageCheckedLocked(fill model.Fill) (bool, decimal.Decimal, error) {
	key := fillKey(fill)
	if key == "" {
		return true, decimal.Zero, nil
	}
	aliases := appliedFillOrderAliases(fill)
	if record, ok := b.seen[key]; ok {
		if appliedOrderAliasesConflict(record.aliases, aliases) {
			return false, decimal.Zero, ErrFillOrderAliasConflict
		}
		if !record.aliases.sharesIdentity(aliases) {
			return false, decimal.Zero, nil
		}
		if _, err := b.previewAppliedOrderGroupsLocked(aliases, appliedOrderGroupRoot(record.group)); err != nil {
			return false, decimal.Zero, err
		}
		return false, decimal.Zero, nil
	}
	groups, err := b.previewAppliedOrderGroupsLocked(aliases, nil)
	if err != nil {
		return false, decimal.Zero, err
	}
	prior := decimal.Zero
	for _, group := range groups {
		prior = prior.Add(group.quantity)
	}
	return true, prior, nil
}

func (b *FillBuffer) previewAppliedOrderGroupsLocked(aliases appliedOrderAliases, required *appliedOrderGroup) ([]*appliedOrderGroup, error) {
	seen := make(map[*appliedOrderGroup]struct{})
	groups := make([]*appliedOrderGroup, 0, 3)
	add := func(group *appliedOrderGroup) {
		group = appliedOrderGroupRoot(group)
		if group == nil {
			return
		}
		if _, exists := seen[group]; exists {
			return
		}
		seen[group] = struct{}{}
		groups = append(groups, group)
	}
	add(required)
	for _, alias := range aliases.all() {
		add(b.orderAliasGroups[alias])
	}
	for i, group := range groups {
		if err := validateAppliedOrderAliases(group, aliases); err != nil {
			return nil, err
		}
		for _, other := range groups[i+1:] {
			if !appliedOrderGroupsCompatible(group, other) {
				return nil, ErrFillOrderAliasConflict
			}
		}
	}
	return groups, nil
}

func appliedOrderGroupRoot(group *appliedOrderGroup) *appliedOrderGroup {
	if group == nil || group.parent == nil {
		return group
	}
	group.parent = appliedOrderGroupRoot(group.parent)
	return group.parent
}

func newAppliedOrderGroup() *appliedOrderGroup {
	return &appliedOrderGroup{
		aliases: make(map[string]struct{}),
	}
}

func (b *FillBuffer) resolveAppliedOrderGroupLocked(aliases appliedOrderAliases) (*appliedOrderGroup, error) {
	var group *appliedOrderGroup
	for _, alias := range aliases.all() {
		if existing := appliedOrderGroupRoot(b.orderAliasGroups[alias]); existing != nil {
			if group == nil {
				group = existing
			} else {
				var err error
				group, err = b.mergeAppliedOrderGroupsLocked(group, existing)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	if group == nil {
		group = newAppliedOrderGroup()
	}
	if err := validateAppliedOrderAliases(group, aliases); err != nil {
		return nil, err
	}
	return group, nil
}

func (b *FillBuffer) mergeAppliedOrderGroupsLocked(target, source *appliedOrderGroup) (*appliedOrderGroup, error) {
	target = appliedOrderGroupRoot(target)
	source = appliedOrderGroupRoot(source)
	if target == source {
		return target, nil
	}
	if !appliedOrderGroupsCompatible(target, source) {
		return nil, ErrFillOrderAliasConflict
	}
	if target.aliases == nil {
		target.aliases = make(map[string]struct{})
	}
	target.quantity = target.quantity.Add(source.quantity)
	target.retained += source.retained
	target.terminal = target.terminal || source.terminal
	for alias := range source.aliases {
		target.aliases[alias] = struct{}{}
		b.orderAliasGroups[alias] = target
	}
	if target.clientAlias == "" {
		target.clientAlias = source.clientAlias
	}
	if target.venueAlias == "" {
		target.venueAlias = source.venueAlias
	}
	if target.anonymousAlias == "" {
		target.anonymousAlias = source.anonymousAlias
	}
	source.parent = target
	source.quantity = decimal.Zero
	source.retained = 0
	source.aliases = nil
	return target, nil
}

func (b *FillBuffer) enrichAppliedOrderAliasesLocked(fillKey string, record appliedFillRecord, incoming appliedOrderAliases) error {
	if appliedOrderAliasesConflict(record.aliases, incoming) {
		return ErrFillOrderAliasConflict
	}
	if !record.aliases.sharesIdentity(incoming) {
		return nil
	}
	var additions []string
	if record.aliases.client == "" && incoming.client != "" {
		additions = append(additions, incoming.client)
	}
	if record.aliases.venue == "" && incoming.venue != "" {
		additions = append(additions, incoming.venue)
	}
	if len(additions) == 0 {
		return nil
	}
	group := appliedOrderGroupRoot(record.group)
	if err := validateAppliedOrderAliases(group, incoming); err != nil {
		return err
	}
	for _, alias := range additions {
		if existing := appliedOrderGroupRoot(b.orderAliasGroups[alias]); existing != nil {
			var err error
			group, err = b.mergeAppliedOrderGroupsLocked(group, existing)
			if err != nil {
				return err
			}
		}
	}
	if err := validateAppliedOrderAliases(group, incoming); err != nil {
		return err
	}
	if record.aliases.client == "" && incoming.client != "" {
		record.aliases.client = incoming.client
		b.retainOrderAliasLocked(group, incoming.client, "client")
	}
	if record.aliases.venue == "" && incoming.venue != "" {
		record.aliases.venue = incoming.venue
		b.retainOrderAliasLocked(group, incoming.venue, "venue")
	}
	record.group = group
	b.seen[fillKey] = record
	return nil
}

func appliedOrderAliasesConflict(known, incoming appliedOrderAliases) bool {
	return (known.client != "" && incoming.client != "" && known.client != incoming.client) ||
		(known.venue != "" && incoming.venue != "" && known.venue != incoming.venue)
}

func (b *FillBuffer) retainOrderAliasesLocked(group *appliedOrderGroup, aliases appliedOrderAliases) {
	if aliases.client != "" {
		b.retainOrderAliasLocked(group, aliases.client, "client")
	}
	if aliases.venue != "" {
		b.retainOrderAliasLocked(group, aliases.venue, "venue")
	}
	if aliases.anonymous != "" {
		b.retainOrderAliasLocked(group, aliases.anonymous, "anonymous")
	}
}

func (b *FillBuffer) retainOrderAliasLocked(group *appliedOrderGroup, alias, kind string) {
	group.aliases[alias] = struct{}{}
	b.orderAliasGroups[alias] = group
	switch kind {
	case "client":
		group.clientAlias = alias
	case "venue":
		group.venueAlias = alias
	case "anonymous":
		group.anonymousAlias = alias
	}
}

func (b *FillBuffer) deleteAppliedOrderGroupLocked(group *appliedOrderGroup) {
	group.quantity = decimal.Zero
	group.retained = 0
	for alias := range group.aliases {
		if appliedOrderGroupRoot(b.orderAliasGroups[alias]) == group {
			delete(b.orderAliasGroups, alias)
		}
	}
	group.aliases = nil
}

// MarkOrderTerminal releases active cumulative coverage once every retained
// TradeID for the order has also left the bounded dedupe window.
func (b *FillBuffer) MarkOrderTerminal(order model.Order) error {
	if !orderstate.IsTerminal(order.Status) {
		return nil
	}
	aliases := appliedOrderAliasesForOrder(order)
	b.mu.Lock()
	defer b.mu.Unlock()
	var group *appliedOrderGroup
	for _, alias := range aliases.all() {
		existing := appliedOrderGroupRoot(b.orderAliasGroups[alias])
		if existing == nil {
			continue
		}
		if group == nil {
			group = existing
			continue
		}
		var err error
		group, err = b.mergeAppliedOrderGroupsLocked(group, existing)
		if err != nil {
			return err
		}
	}
	if group == nil {
		return nil
	}
	if err := validateAppliedOrderAliases(group, aliases); err != nil {
		return err
	}
	b.bindOrderAliasesLocked(group, aliases)
	group.terminal = true
	if group.retained <= 0 {
		b.deleteAppliedOrderGroupLocked(group)
	}
	return nil
}

func (b *FillBuffer) bindOrderAliasesLocked(group *appliedOrderGroup, aliases appliedOrderAliases) {
	if aliases.client != "" && group.clientAlias == "" {
		group.clientAlias = aliases.client
		group.aliases[aliases.client] = struct{}{}
		b.orderAliasGroups[aliases.client] = group
	}
	if aliases.venue != "" && group.venueAlias == "" {
		group.venueAlias = aliases.venue
		group.aliases[aliases.venue] = struct{}{}
		b.orderAliasGroups[aliases.venue] = group
	}
}

func appliedOrderGroupsCompatible(left, right *appliedOrderGroup) bool {
	return aliasesCompatible(left.clientAlias, right.clientAlias) &&
		aliasesCompatible(left.venueAlias, right.venueAlias) &&
		aliasesCompatible(left.anonymousAlias, right.anonymousAlias) &&
		!((left.anonymousAlias != "" || right.anonymousAlias != "") &&
			(left.clientAlias != "" || right.clientAlias != "" || left.venueAlias != "" || right.venueAlias != ""))
}

func validateAppliedOrderAliases(group *appliedOrderGroup, aliases appliedOrderAliases) error {
	group = appliedOrderGroupRoot(group)
	if !aliasesCompatible(group.clientAlias, aliases.client) ||
		!aliasesCompatible(group.venueAlias, aliases.venue) ||
		!aliasesCompatible(group.anonymousAlias, aliases.anonymous) {
		return ErrFillOrderAliasConflict
	}
	if aliases.anonymous != "" && (group.clientAlias != "" || group.venueAlias != "") {
		return ErrFillOrderAliasConflict
	}
	if (aliases.client != "" || aliases.venue != "") && group.anonymousAlias != "" {
		return ErrFillOrderAliasConflict
	}
	return nil
}

func aliasesCompatible(known, incoming string) bool {
	return known == "" || incoming == "" || known == incoming
}

func (b *FillBuffer) Buffer(fill model.Fill) {
	b.BufferEnvelope(fill, contract.EventMeta{})
}

func (b *FillBuffer) BufferEnvelope(fill model.Fill, meta contract.EventMeta) {
	b.mu.Lock()
	defer b.mu.Unlock()
	buffered := BufferedFill{Fill: fill, Meta: meta}
	if fill.ClientID != "" {
		key := fillIndexKey{accountID: fill.AccountID, id: fill.ClientID}
		b.byClient[key] = append(b.byClient[key], buffered)
	}
	if fill.VenueOrderID != "" {
		key := fillIndexKey{accountID: fill.AccountID, id: fill.VenueOrderID}
		b.byVenue[key] = append(b.byVenue[key], buffered)
	}
}

func (b *FillBuffer) Drain(order model.Order) []model.Fill {
	buffered := b.DrainBuffered(order)
	out := make([]model.Fill, 0, len(buffered))
	for _, fill := range buffered {
		out = append(out, fill.Fill)
	}
	return out
}

func (b *FillBuffer) DrainBuffered(order model.Order) []BufferedFill {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []BufferedFill
	accountID := order.Request.AccountID
	if clientID := order.Request.ClientID; clientID != "" {
		out = append(out, b.drainIndex(b.byClient, accountID, clientID)...)
	}
	if venueID := order.VenueOrderID; venueID != "" {
		out = append(out, b.drainIndex(b.byVenue, accountID, venueID)...)
	}
	return dedupeBufferedFills(out)
}

func (b *FillBuffer) drainIndex(index map[fillIndexKey][]BufferedFill, accountID, id string) []BufferedFill {
	var out []BufferedFill
	if accountID != "" {
		key := fillIndexKey{accountID: accountID, id: id}
		out = append(out, index[key]...)
		delete(index, key)
	}
	unscoped := fillIndexKey{id: id}
	out = append(out, index[unscoped]...)
	delete(index, unscoped)
	return out
}

func (b *FillBuffer) Count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	seen := make(map[string]struct{})
	for _, fills := range b.byClient {
		for _, fill := range fills {
			seen[pendingFillKey(fill.Fill)] = struct{}{}
		}
	}
	for _, fills := range b.byVenue {
		for _, fill := range fills {
			seen[pendingFillKey(fill.Fill)] = struct{}{}
		}
	}
	return len(seen)
}

func dedupeBufferedFills(fills []BufferedFill) []BufferedFill {
	if len(fills) < 2 {
		return fills
	}
	seen := make(map[string]struct{}, len(fills))
	out := fills[:0]
	for _, fill := range fills {
		key := fillKey(fill.Fill)
		if key == "" {
			key = pendingFillKey(fill.Fill)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, fill)
	}
	return out
}

func fillKey(fill model.Fill) string {
	return orderstate.FillKey(fill)
}

func appliedFillOrderAliases(fill model.Fill) appliedOrderAliases {
	instrument := fill.InstrumentID.String()
	aliases := appliedOrderAliases{}
	if fill.ClientID != "" {
		aliases.client = strings.Join([]string{fill.AccountID, instrument, "client", fill.ClientID}, "\x00")
	}
	if fill.VenueOrderID != "" {
		aliases.venue = strings.Join([]string{fill.AccountID, instrument, "venue", fill.VenueOrderID}, "\x00")
	}
	if aliases.client == "" && aliases.venue == "" {
		aliases.anonymous = strings.Join([]string{fill.AccountID, instrument, "anonymous"}, "\x00")
	}
	return aliases
}

func appliedOrderAliasesForOrder(order model.Order) appliedOrderAliases {
	return appliedFillOrderAliases(model.Fill{
		AccountID:    order.Request.AccountID,
		InstrumentID: order.Request.InstrumentID,
		ClientID:     order.Request.ClientID,
		VenueOrderID: order.VenueOrderID,
	})
}

func pendingFillKey(fill model.Fill) string {
	if key := fillKey(fill); key != "" {
		return key
	}
	return strings.Join([]string{
		fill.AccountID,
		fill.InstrumentID.String(),
		fill.ClientID,
		fill.VenueOrderID,
		fill.Price.String(),
		fill.Quantity.String(),
		fill.Timestamp.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
	}, "\x00")
}
