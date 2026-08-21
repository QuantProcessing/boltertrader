import { Layers } from "lucide-react";

import { describeError } from "@/api/errors";
import type { Account, ArbitrageLegView, ArbitragePair, ArbitragePairState, CommandState } from "@/api/types";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Num } from "@/components/ui/num";
import { StateBadge, type StateTone } from "@/components/ui/state-badge";
import { useToast } from "@/components/ui/toast";
import { useArbitragePositions, useCancelArbitrageLeg, useCloseArbitrage } from "@/features/funding/queries";
import { formatTimestamp } from "@/lib/format";
import { instrumentSymbol } from "@/lib/instrument";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

export function ArbitragePairs({ accounts }: { accounts: Account[] }) {
  const t = useT();
  const positions = useArbitragePositions();
  const close = useCloseArbitrage();
  const cancel = useCancelArbitrageLeg();
  const toast = useToast();
  const pairs = positions.data ?? [];
  const accountByID = new Map(accounts.map((account) => [account.accountId, account]));
  const attention = pairs.filter(needsAttention).length;

  const onClose = (pair: ArbitragePair) => {
    close.mutate(pair.lineageId, {
      onSuccess: (execution) => toast.push({ tone: execution.failures.length > 0 ? "warn" : "ok", title: execution.failures.length > 0 ? t("arb.closePartly") : t("arb.closing"), detail: execution.failures.length > 0 ? t("arb.closePartly.detail") : t("arb.closing.detail") }),
      onError: (error) => toast.push({ tone: "danger", title: t("arb.closeRejected"), detail: describeError(error, t) }),
    });
  };
  const onCancel = (leg: ArbitrageLegView) => {
    cancel.mutate({ lineageId: leg.lineageId, commandId: leg.commandId }, {
      onSuccess: () => toast.push({ tone: "ok", title: t("arb.cancelSent"), detail: t("arb.cancelSent.detail", { leg: leg.leg }), duration: 4000 }),
      onError: (error) => toast.push({ tone: "danger", title: t("blotter.cancelRejected"), detail: describeError(error, t) }),
    });
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-panel">
      <div className="flex h-row shrink-0 items-center justify-between border-b border-line px-3">
        <h2 className="text-title font-semibold text-text">{t("arb.openPairs")} <span className="ml-1 text-meta font-normal text-dim">({pairs.length})</span></h2>
        {attention > 0 ? <span className="text-meta text-danger">{t("arb.attention", { count: attention })}</span> : null}
      </div>
      {positions.isPending ? <p className="px-3 py-4 text-base text-dim">{t("arb.reading")}</p>
        : positions.isError ? <p className="px-3 py-4 text-base text-danger">{describeError(positions.error, t)}</p>
        : pairs.length === 0 ? (
          <div className="flex min-h-0 flex-1 flex-col">
            <PairTableHeader />
            <EmptyState icon={Layers} title={t("arb.empty.pairs")} className="flex-1" />
          </div>
        )
        : (
          <div className="min-h-0 flex-1 overflow-auto">
            <table className="w-full min-w-[980px] border-collapse text-left">
              <PairTableHead />
              <tbody>
                {[...pairs].sort(byUrgency).map((pair) => {
                  const long = latestLeg(pair, "long");
                  const short = latestLeg(pair, "short");
                  const partial = pair.state === "partially_open";
                  const restable = pair.legs.find((leg) => isRestable(leg.state));
                  return (
                    <tr key={pair.lineageId} className={cn("h-row border-b border-line text-base hover:bg-raised", partial && "bg-danger/5")}>
                      <td className="px-3 font-medium text-text">{instrumentSymbol(pair.legs[0]?.instrumentId) || "—"}</td>
                      <td className="px-2 text-muted">{t("arb.long")} / {t("arb.short")}</td>
                      <td className="px-2"><LegVenue leg={long} accountByID={accountByID} tone="up" /></td>
                      <td className="px-2"><LegVenue leg={short} accountByID={accountByID} tone="down" /></td>
                      <td className="px-2 text-right"><Num value={long?.quantity ?? short?.quantity} /></td>
                      <td className="px-2 text-right"><FundingRoll amounts={pair.funding} /></td>
                      <td className="px-2"><StateBadge tone={pairTone(pair.state)} label={pair.state.replaceAll("_", " ")} /></td>
                      <td className="px-2 text-meta text-dim">{formatTimestamp(pair.updatedAt)}</td>
                      <td className="px-3 text-right">
                        {restable ? (
                          <Button size="sm" variant="ghost" onClick={() => onCancel(restable)} disabled={cancel.isPending && cancel.variables?.commandId === restable.commandId}>{t("arb.cancelLeg")}</Button>
                        ) : pair.state === "open" || partial ? (
                          <Button size="sm" variant={partial ? "danger" : "ghost"} onClick={() => onClose(pair)} disabled={close.isPending && close.variables === pair.lineageId}>{partial ? t("arb.closeOpenLeg") : t("arb.closePair")}</Button>
                        ) : null}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
    </div>
  );
}

function PairTableHeader() {
  const t = useT();
  return (
    <div className="grid h-7 min-w-[980px] grid-cols-[1.1fr_1fr_1fr_1fr_.7fr_.8fr_.8fr_1fr_.7fr] items-center border-b border-line px-3 text-micro uppercase tracking-wide text-dim">
      <span>{t("arb.instrument")}</span>
      <span>{t("arb.direction")}</span>
      <span>{t("arb.long")}</span>
      <span>{t("arb.short")}</span>
      <span className="text-right">{t("ticket.quantity")}</span>
      <span className="text-right">{t("arb.funding")}</span>
      <span>{t("arb.status")}</span>
      <span>{t("arb.updated")}</span>
      <span className="text-right">{t("arb.action")}</span>
    </div>
  );
}

function PairTableHead() {
  const t = useT();
  return (
    <thead className="sticky top-0 z-10 bg-panel">
      <tr className="h-7 border-b border-line text-micro uppercase tracking-wide text-dim">
        <th className="px-3 font-medium">{t("arb.instrument")}</th>
        <th className="px-2 font-medium">{t("arb.direction")}</th>
        <th className="px-2 font-medium">{t("arb.long")}</th>
        <th className="px-2 font-medium">{t("arb.short")}</th>
        <th className="px-2 text-right font-medium">{t("ticket.quantity")}</th>
        <th className="px-2 text-right font-medium">{t("arb.funding")}</th>
        <th className="px-2 font-medium">{t("arb.status")}</th>
        <th className="px-2 font-medium">{t("arb.updated")}</th>
        <th className="px-3 text-right font-medium">{t("arb.action")}</th>
      </tr>
    </thead>
  );
}

function LegVenue({ leg, accountByID, tone }: { leg: ArbitrageLegView | undefined; accountByID: Map<string, Account>; tone: "up" | "down" }) {
  if (!leg) return <span className="text-danger">—</span>;
  return <span className={tone === "up" ? "text-up" : "text-down"}>{accountByID.get(leg.accountId)?.venue ?? "—"}</span>;
}

function FundingRoll({ amounts }: { amounts: { currency: string; amount: string }[] }) {
  if (amounts.length === 0) return <span className="text-dim">—</span>;
  return <span>{amounts.map((amount, index) => <span key={amount.currency}>{index > 0 ? " · " : ""}<Num value={amount.amount} directional signed /> <span className="text-dim">{amount.currency}</span></span>)}</span>;
}

function latestLeg(pair: ArbitragePair, side: "long" | "short"): ArbitrageLegView | undefined {
  return [...pair.legs].reverse().find((leg) => leg.leg === side && !leg.closing) ?? [...pair.legs].reverse().find((leg) => leg.leg === side);
}

function needsAttention(pair: ArbitragePair): boolean { return pair.state === "partially_open" || pair.state === "failed" || pair.blockers.length > 0; }
function byUrgency(left: ArbitragePair, right: ArbitragePair): number {
  if (needsAttention(left) !== needsAttention(right)) return needsAttention(left) ? -1 : 1;
  return right.updatedAt.localeCompare(left.updatedAt);
}
function pairTone(state: ArbitragePairState): StateTone {
  switch (state) {
    case "open": return "ok";
    case "opening": case "closing": return "pending";
    case "partially_open": case "failed": return "danger";
    case "closed": return "neutral";
  }
}
function isRestable(state: CommandState): boolean { return state === "accepted" || state === "submitted"; }
