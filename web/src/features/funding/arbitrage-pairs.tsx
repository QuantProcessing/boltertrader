import { Layers } from "lucide-react";

import { describeError } from "@/api/errors";
import type { Account, ArbitrageLegView, ArbitragePair, ArbitragePairState, CommandState, Position } from "@/api/types";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Num, Side } from "@/components/ui/num";
import { StateBadge, Tag, type StateTone } from "@/components/ui/state-badge";
import { useToast } from "@/components/ui/toast";
import { useArbitragePositions, useCancelArbitrageLeg, useCloseArbitrage } from "@/features/funding/queries";
import { formatTimestamp, splitInstrument } from "@/lib/format";
import { useT } from "@/lib/i18n";

/* ---------------------------------------------------------------------------
 * Open hedges, both legs each.
 *
 * A pair is not two orders that happen to share a screen — it is one position
 * held in two places, and the only reading of it that means anything shows both
 * halves at once. So this is a list of composites rather than a table of legs:
 * a table would sort the two halves apart the moment anyone clicked a column.
 *
 * The state that matters most is `partially_open`. It means one leg landed and
 * the other did not, which leaves the account directionally exposed in a way
 * nobody chose — the server deliberately does not unwind it (an automatic
 * reversal would be the product trading on its own), so undoing it is a
 * decision presented here rather than a thing that quietly already happened.
 * ------------------------------------------------------------------------- */

export function ArbitragePairs({ accounts }: { accounts: Account[] }) {
  const t = useT();
  const positions = useArbitragePositions();
  const close = useCloseArbitrage();
  const cancel = useCancelArbitrageLeg();
  const toast = useToast();

  const pairs = positions.data ?? [];
  const venues = new Map(accounts.map((account) => [account.accountId, account]));
  const attention = pairs.filter(needsAttention).length;

  const onClose = (pair: ArbitragePair) => {
    close.mutate(pair.lineageId, {
      onSuccess: (execution) => {
        toast.push({
          tone: execution.failures.length > 0 ? "warn" : "ok",
          title: execution.failures.length > 0 ? t("arb.closePartly") : t("arb.closing"),
          detail:
            execution.failures.length > 0 ? t("arb.closePartly.detail") : t("arb.closing.detail"),
        });
      },
      onError: (error) => {
        toast.push({ tone: "danger", title: t("arb.closeRejected"), detail: describeError(error, t) });
      },
    });
  };

  const onCancel = (leg: ArbitrageLegView) => {
    cancel.mutate(
      { lineageId: leg.lineageId, commandId: leg.commandId },
      {
        onSuccess: () => {
          toast.push({
            tone: "ok",
            title: t("arb.cancelSent"),
            // `leg.leg` is the server's own `long` / `short`.
            detail: t("arb.cancelSent.detail", { leg: leg.leg }),
            duration: 4000,
          });
        },
        onError: (error) => {
          toast.push({ tone: "danger", title: t("blotter.cancelRejected"), detail: describeError(error, t) });
        },
      },
    );
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex h-row shrink-0 items-center justify-between gap-3 border-b border-line px-3">
        <h2 className="text-base font-medium text-text">{t("arb.openPairs")}</h2>
        <div className="flex items-center gap-3 text-meta text-dim">
          {attention > 0 ? (
            <span className="text-danger">{t("arb.attention", { count: attention })}</span>
          ) : null}
          <span>{pairs.length}</span>
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {positions.isPending ? (
          <p className="px-3 py-4 text-base text-dim">{t("arb.reading")}</p>
        ) : positions.isError ? (
          <p className="px-3 py-4 text-base text-danger">{describeError(positions.error, t)}</p>
        ) : pairs.length === 0 ? (
          <EmptyState icon={Layers} title={t("arb.empty.pairs")} detail={t("arb.empty.pairs.detail")} />
        ) : (
          <ul>
            {[...pairs].sort(byUrgency).map((pair) => (
              <PairRow
                key={pair.lineageId}
                pair={pair}
                venues={venues}
                closing={close.isPending && close.variables === pair.lineageId}
                cancelling={cancel.isPending ? cancel.variables?.commandId : undefined}
                onClose={() => onClose(pair)}
                onCancel={onCancel}
              />
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

function PairRow({
  pair,
  venues,
  closing,
  cancelling,
  onClose,
  onCancel,
}: {
  pair: ArbitragePair;
  venues: Map<string, Account>;
  closing: boolean;
  /** The command id of the leg whose cancel is in flight, if any. */
  cancelling: string | undefined;
  onClose: () => void;
  onCancel: (leg: ArbitrageLegView) => void;
}) {
  const t = useT();
  const long = pair.legs.filter((leg) => leg.leg === "long");
  const short = pair.legs.filter((leg) => leg.leg === "short");
  const partial = pair.state === "partially_open";
  const symbol = splitInstrument(pair.legs[0]?.instrumentId ?? "").base;

  return (
    <li
      className={
        // The only rule inside this list, and it is between pairs rather than
        // between rows: a composite needs a bound, or the second pair's long leg
        // reads as the first pair's third.
        "border-t border-line px-3 py-2.5 first:border-t-0" + (partial ? " border-l-2 border-l-danger" : "")
      }
    >
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <span className="text-base font-medium text-text">{symbol || "—"}</span>
        {/* The lineage state is the server's own word, printed as it arrived. */}
        <StateBadge tone={pairTone(pair.state)} label={pair.state.replace("_", " ")} />
        <FundingRoll amounts={pair.funding} />
        <span className="ml-auto flex items-center gap-2">
          <span className="text-meta text-dim">{formatTimestamp(pair.updatedAt)}</span>
          {pair.state === "open" || partial ? (
            <Button size="sm" variant={partial ? "danger" : "outline"} onClick={onClose} disabled={closing}>
              {closing ? t("arb.closingButton") : partial ? t("arb.closeOpenLeg") : t("arb.closePair")}
            </Button>
          ) : null}
        </span>
      </div>

      {partial ? <p className="mt-1.5 text-meta text-danger">{t("arb.directional")}</p> : null}

      {/* The blockers are the server's own sentences. */}
      {pair.blockers.length > 0 ? <p className="mt-1.5 text-meta text-warn">{pair.blockers.join(" · ")}</p> : null}

      <div className="mt-2 grid gap-2 md:grid-cols-2">
        <LegColumn legs={long} pair={pair} venues={venues} cancelling={cancelling} onCancel={onCancel} />
        <LegColumn legs={short} pair={pair} venues={venues} cancelling={cancelling} onCancel={onCancel} />
      </div>
    </li>
  );
}

/**
 * One side of the hedge.
 *
 * Plural because closing a pair adds reduce-only commands under the same
 * lineage and the same leg label: showing only the first would hide the order
 * that is currently doing the work.
 */
function LegColumn({
  legs,
  pair,
  venues,
  cancelling,
  onCancel,
}: {
  legs: ArbitrageLegView[];
  pair: ArbitragePair;
  venues: Map<string, Account>;
  cancelling: string | undefined;
  onCancel: (leg: ArbitrageLegView) => void;
}) {
  // Above the early return: a hook cannot sit behind a conditional.
  const t = useT();

  if (legs.length === 0) {
    // The one border §5 permits inside a panel, because it is not a card: a
    // dashed outline is the shape of an absent leg, and the shape is the
    // message. `line-strong` rather than `line` — a hairline that reads as a
    // seam when solid disappears entirely once it is broken into dashes.
    return (
      <div className="rounded-card border border-dashed border-line-strong px-2.5 py-2 text-meta text-dim">
        {t("arb.noLeg")}
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-2">
      {legs.map((leg) => (
        <LegCard
          key={leg.commandId}
          leg={leg}
          account={venues.get(leg.accountId)}
          position={positionFor(pair.positions, leg)}
          cancelling={cancelling === leg.commandId}
          onCancel={() => onCancel(leg)}
        />
      ))}
    </div>
  );
}

function LegCard({
  leg,
  account,
  position,
  cancelling,
  onCancel,
}: {
  leg: ArbitrageLegView;
  account: Account | undefined;
  position: Position | undefined;
  cancelling: boolean;
  onCancel: () => void;
}) {
  const t = useT();

  return (
    <div className="rounded-card bg-fill px-2.5 py-2">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <Side side={leg.leg} className="text-base" />
        <span className="text-base text-text">{account?.venue ?? t("arb.unknownVenue")}</span>
        {/* The native environment and the command state are both the server's
            own words, printed as they arrived. */}
        {account ? <Tag>{account.nativeEnvironment}</Tag> : null}
        {leg.reduceOnly ? <Tag>{t("arb.reduceOnly")}</Tag> : null}
        <span className="ml-auto">
          <StateBadge tone={legTone(leg.state)} label={leg.state} />
        </span>
      </div>

      <div className="mt-1.5 flex flex-wrap items-baseline gap-x-3 gap-y-1 text-meta text-dim">
        <span className="text-text">{leg.instrumentId}</span>
        <span>
          {/* `market` is the order type as the server names it, the same word
              the blotter's type column prints. */}
          <Num value={leg.quantity} /> @ {leg.orderType === "limit" ? <Num value={leg.limitPrice} /> : "market"}
        </span>
        {position ? (
          <>
            <span>
              {t("arb.mark")} <Num value={position.markPrice} />
            </span>
            <span>
              {t("arb.upnl")} <Num value={position.unrealizedPnl} directional signed />{" "}
              {position.settlementCurrency}
            </span>
          </>
        ) : null}
      </div>

      {isRestable(leg.state) ? (
        <div className="mt-1.5">
          <Button size="sm" variant="ghost" onClick={onCancel} disabled={cancelling}>
            {cancelling ? t("arb.cancelling") : t("arb.cancelLeg")}
          </Button>
        </div>
      ) : null}
    </div>
  );
}

/** Accrued funding, one figure per settlement currency. */
function FundingRoll({ amounts }: { amounts: { currency: string; amount: string }[] }) {
  // Above the early return: a hook cannot sit behind a conditional.
  const t = useT();

  if (amounts.length === 0) {
    return <span className="text-meta text-dim">{t("arb.funding")} —</span>;
  }
  return (
    <span className="flex flex-wrap items-baseline gap-2 text-meta text-dim">
      {t("arb.funding")}
      {amounts.map((amount) => (
        <span key={amount.currency} className="text-text">
          <Num value={amount.amount} directional signed /> {amount.currency}
        </span>
      ))}
    </span>
  );
}

function positionFor(positions: Position[], leg: ArbitrageLegView): Position | undefined {
  return positions.find(
    (position) => position.accountId === leg.accountId && position.instrumentId === leg.instrumentId,
  );
}

/** A pair that is one-sided or broken outranks one that is merely working. */
function needsAttention(pair: ArbitragePair): boolean {
  return pair.state === "partially_open" || pair.state === "failed" || pair.blockers.length > 0;
}

function byUrgency(left: ArbitragePair, right: ArbitragePair): number {
  const leftUrgent = needsAttention(left);
  const rightUrgent = needsAttention(right);
  if (leftUrgent !== rightUrgent) return leftUrgent ? -1 : 1;
  return right.updatedAt.localeCompare(left.updatedAt);
}

/**
 * `partially_open` shares `danger` with `failed` even though one carries
 * exposure and the other carries none. The tone says "this will not resolve on
 * its own"; which of the two it is, the label says.
 */
function pairTone(state: ArbitragePairState): StateTone {
  switch (state) {
    case "open":
      return "ok";
    case "opening":
    case "closing":
      return "pending";
    case "partially_open":
    case "failed":
      return "danger";
    case "closed":
      return "neutral";
  }
}

function legTone(state: CommandState): StateTone {
  switch (state) {
    case "filled":
      return "ok";
    case "accepted":
    case "submitted":
      return "pending";
    case "uncertain":
      return "warn";
    case "failed":
      return "danger";
    case "canceled":
      return "neutral";
  }
}

/** Only a leg the venue is still holding can be pulled back. */
function isRestable(state: CommandState): boolean {
  return state === "accepted" || state === "submitted";
}
