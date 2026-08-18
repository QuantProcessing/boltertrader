import { useNavigate, useSearch } from "@tanstack/react-router";
import { AlertTriangle, SearchX } from "lucide-react";
import { useMemo, useState } from "react";

import { describeError } from "@/api/errors";
import type { ArbitrageOpportunity } from "@/api/types";
import { EmptyState } from "@/components/ui/empty-state";
import { inputClass } from "@/components/ui/field";
import { Num } from "@/components/ui/num";
import { Metric } from "@/components/ui/panel";
import { ArbitragePairs } from "@/features/funding/arbitrage-pairs";
import { ArbitrageTicket } from "@/features/funding/arbitrage-ticket";
import { useArbitrageOpportunities } from "@/features/funding/queries";
import { SpreadChart } from "@/features/funding/spread-chart";
import { useAccounts } from "@/features/market/queries";
import { formatTimestamp } from "@/lib/format";
import { cn } from "@/lib/utils";

/* ---------------------------------------------------------------------------
 * Cross-venue funding arbitrage: one underlying, two venues, opposite sides.
 *
 * The hedge is delta-neutral by construction — the same size long where funding
 * is cheapest and short where it is dearest — so the position makes no
 * directional bet and the spread between the two rates is the whole of the
 * return. That is why this screen is organised around a pair rather than an
 * instrument: there is no single price to chart, no single book to read, and
 * nothing on it means anything until both legs are named.
 *
 * Three columns, same skeleton as the terminal. Ranked spreads on the left
 * because choosing the pair is the first decision; the observed rates in the
 * middle because the spread is the thing being judged; the ticket on the right
 * because sending it is the last. Open hedges sit under the chart rather than
 * on a separate screen: a pair that is half-open is the one thing here that can
 * hurt, and it should be visible while the next one is being considered.
 *
 * Orientation lives in the URL. The server always proposes the profitable one —
 * long the venue paying least — but an operator who swaps the legs, or who
 * arrived from the matrix with a pair already in mind, has expressed a choice
 * that a poll fifteen seconds later must not quietly overrule.
 * ------------------------------------------------------------------------- */

export function ArbitragePage() {
  // `/app/…` rather than the address in the bar: the auth guard is a pathless
  // layout route, so the *id* this route is filed under carries its parent's.
  const search = useSearch({ from: "/app/funding/arbitrage" });
  const navigate = useNavigate();
  const [needle, setNeedle] = useState("");

  const accountsQuery = useAccounts();
  // An account without the entitlement is refused at the product boundary
  // rather than here; filtering early means it is never offered.
  const accounts = useMemo(
    () => (accountsQuery.data ?? []).filter((account) => account.productPolicy.includes("arbitrage")),
    [accountsQuery.data],
  );
  // Sorted so that the query key is a property of the set rather than of the
  // order the accounts happened to arrive in.
  const accountIds = useMemo(() => accounts.map((account) => account.accountId).sort(), [accounts]);

  const opportunities = useArbitrageOpportunities(accountIds);
  const items = opportunities.data?.items ?? [];
  const observedAt = opportunities.data?.observedAt;

  // The server returns exactly one pair per underlying, so the symbol alone
  // identifies a row. Falling back to the first executable one — rather than to
  // the first one — means an arrival with no parameters lands on something that
  // can actually be sent.
  const selected = useMemo(
    () => items.find((item) => item.symbol === search.symbol) ?? items.find((item) => item.executable) ?? items[0],
    [items, search.symbol],
  );

  const oriented = useMemo(() => {
    if (!selected) return null;
    const flipped = search.long !== undefined && search.long === selected.short.venue;
    return flipped
      ? { long: selected.short, short: selected.long }
      : { long: selected.long, short: selected.short };
  }, [selected, search.long]);

  const select = (symbol: string) => {
    // Dropping the orientation deliberately: a different underlying is a
    // different decision, and inheriting the last one's sides would be a guess.
    void navigate({ to: "/funding/arbitrage", search: { symbol }, replace: true });
  };

  const swap = () => {
    if (!selected || !oriented) return;
    void navigate({
      to: "/funding/arbitrage",
      search: { symbol: selected.symbol, long: oriented.short.venue, short: oriented.long.venue },
      replace: true,
    });
  };

  const executable = items.filter((item) => item.executable).length;
  const visible = needle.trim()
    ? items.filter((item) => item.symbol.toLowerCase().includes(needle.trim().toLowerCase()))
    : items;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="flex h-bar shrink-0 items-center gap-6 overflow-x-auto border-b border-line bg-panel px-3">
        <div className="flex items-center gap-2">
          <h1 className="text-title font-semibold tracking-tight text-text">Arbitrage</h1>
        </div>
        <Metric label="Pairs" value={String(items.length)} />
        <Metric label="Executable" value={String(executable)} tone={executable === 0 ? "down" : "none"} />
        <Metric label="Accounts" value={String(accounts.length)} tone={accounts.length < 2 ? "down" : "none"} />
        <Metric label="Observed" value={observedAt ? formatTimestamp(observedAt) : "—"} />
        {accounts.length < 2 ? (
          <p className="ml-auto text-meta text-warn" role="status">
            A hedge needs two venues. Add arbitrage-capable accounts in Settings.
          </p>
        ) : opportunities.isError ? (
          <p className="ml-auto text-meta text-warn" role="status">
            Showing the last read — the refresh failed.
          </p>
        ) : null}
      </header>

      <div className="flex min-h-0 flex-1">
        <aside className="flex w-64 shrink-0 flex-col border-r border-line">
          <div className="flex h-row shrink-0 items-center justify-between gap-2 border-b border-line px-3">
            <h2 className="text-base font-medium text-text">Spreads</h2>
            <span className="text-meta text-dim">{visible.length}</span>
          </div>
          <div className="shrink-0 border-b border-line px-3 py-2">
            <label className="flex flex-col gap-1">
              <span className="sr-only">Search underlyings</span>
              <input
                type="search"
                value={needle}
                onChange={(event) => setNeedle(event.target.value)}
                placeholder="BTC"
                className={cn(inputClass, "w-full")}
              />
            </label>
          </div>
          <SpreadList
            items={visible}
            selected={selected?.symbol}
            pending={opportunities.isPending}
            error={opportunities.isError ? describeError(opportunities.error) : null}
            onSelect={select}
          />
        </aside>

        <div className="flex min-w-0 flex-1 flex-col">
          {oriented && selected ? (
            <SpreadChart
              symbol={selected.symbol}
              longVenue={oriented.long.venue}
              longEnvironment={oriented.long.environment}
              shortVenue={oriented.short.venue}
              shortEnvironment={oriented.short.environment}
            />
          ) : (
            <div className="flex min-h-0 flex-1 flex-col">
              <EmptyState
                icon={AlertTriangle}
                title={opportunities.isPending ? "Reading the board" : "No pair to hedge"}
                detail={
                  opportunities.isPending
                    ? "Ranking every underlying quoted by more than one venue."
                    : "A pair appears once two venues quote the same perpetual. Nothing does right now."
                }
                className="flex-1"
              />
            </div>
          )}

          {/* Fixed height rather than a share of the column: the chart is the
              thing being read, and a list that grew with the number of open
              hedges would take the chart away as the day went on. */}
          <div className="flex h-72 shrink-0 flex-col border-t border-line">
            <ArbitragePairs accounts={accounts} />
          </div>
        </div>

        {oriented && selected ? (
          <div className="w-80 shrink-0 overflow-y-auto border-l border-line p-3">
            <ArbitrageTicket
              symbol={selected.symbol}
              baseAsset={selected.baseAsset}
              quoteAsset={selected.quoteAsset}
              long={oriented.long}
              short={oriented.short}
              accounts={accounts}
              onSwap={swap}
            />
          </div>
        ) : null}
      </div>
    </div>
  );
}

/** The ranked list. Spread-descending, straight from the server. */
function SpreadList({
  items,
  selected,
  pending,
  error,
  onSelect,
}: {
  items: ArbitrageOpportunity[];
  selected: string | undefined;
  pending: boolean;
  error: string | null;
  onSelect: (symbol: string) => void;
}) {
  if (pending) {
    return <p className="px-3 py-4 text-base text-dim">Ranking spreads…</p>;
  }
  if (items.length === 0) {
    return error ? (
      <p className="px-3 py-4 text-base text-danger">{error}</p>
    ) : (
      <EmptyState icon={SearchX} title="Nothing matches" detail="No underlying by that name is quoted twice." />
    );
  }

  return (
    <ul className="min-h-0 flex-1 overflow-y-auto">
      {items.map((item) => (
        <li key={item.symbol}>
          <button
            type="button"
            onClick={() => onSelect(item.symbol)}
            aria-current={item.symbol === selected ? "true" : undefined}
            className={cn(
              "flex w-full flex-col gap-0.5 border-b border-line px-3 py-2 text-left transition-colors hover:bg-raised focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent",
              item.symbol === selected && "bg-raised",
            )}
          >
            <span className="flex items-baseline justify-between gap-2">
              <span className="text-base font-medium text-text">{item.symbol}</span>
              <span className="text-base">
                {aprPercent(item.spreadApr) === null ? (
                  <span className="text-dim">—</span>
                ) : (
                  <>
                    <Num value={aprPercent(item.spreadApr) ?? "0"} decimals={2} signed directional />
                    <span className="text-meta text-dim">%</span>
                  </>
                )}
              </span>
            </span>
            <span className="flex items-baseline justify-between gap-2 text-meta text-dim">
              <span className="truncate">
                {item.long.venue} → {item.short.venue}
              </span>
              {/* Named rather than dotted: "not executable" is a state with a
                  reason, and the reason is one word long. */}
              {item.executable ? null : <span className="shrink-0 text-warn">{item.blockers[0] ?? "blocked"}</span>}
            </span>
          </button>
        </li>
      ))}
    </ul>
  );
}

/** A fractional annualised spread as a percent string, ready for `Num`. */
function aprPercent(apr: string | undefined): string | null {
  const parsed = Number.parseFloat(apr ?? "");
  return Number.isFinite(parsed) ? (parsed * 100).toFixed(2) : null;
}
