import { AlertTriangle } from "lucide-react";

import { EmptyState } from "@/components/ui/empty-state";
import { Metric } from "@/components/ui/panel";
import { FundingMatrix } from "@/features/funding/matrix";
import { useFundingBoard } from "@/features/funding/queries";
import { formatTimestamp } from "@/lib/format";
import { useT } from "@/lib/i18n";

/* ---------------------------------------------------------------------------
 * Funding: nine venues (eleven settlement routes) quoting perpetuals side by side.
 *
 * This is the only market screen in the product that needs no account, no
 * credential and no instrument selection — the Gateway reads it from public
 * endpoints — which makes it the first screen worth opening on a fresh install
 * and the one place a spread is visible before anyone has decided to trade it.
 *
 * Same three-band layout as the terminal and the portfolio: strip, then the
 * table that is the page.
 * ------------------------------------------------------------------------- */

export function FundingPage() {
  const t = useT();
  const board = useFundingBoard();
  const snapshot = board.data;

  const venues = snapshot?.venues ?? [];
  const degraded = venues.filter((venue) => venue.unavailable || venue.stale).length;

  // A failure with nothing already on screen is the empty page; a failure with a
  // previous snapshot still rendered is a banner over live-but-ageing data,
  // because `placeholderData` deliberately keeps the last good matrix visible.
  if (board.isError && snapshot === undefined) {
    return (
      <div className="flex min-h-0 flex-1 flex-col">
        <EmptyState
          icon={AlertTriangle}
          title={t("funding.unavailable")}
          detail={
            board.error instanceof Error
              ? board.error.message
              : t("funding.unavailable.detail")
          }
          className="flex-1"
        />
      </div>
    );
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="workspace-page-header flex h-bar shrink-0 items-center gap-6 overflow-x-auto border-b border-line bg-panel px-3">
        <h1 className="shrink-0 text-title font-semibold text-text">{t("nav.funding")}</h1>

        <Metric label={t("funding.venues")} value={String(venues.length)} />
        <Metric
          label={t("funding.degraded")}
          value={String(degraded)}
          tone={degraded > 0 ? "down" : "none"}
        />
        <Metric
          label={t("funding.observed")}
          value={snapshot ? formatTimestamp(snapshot.observedAt) : "—"}
        />
      </header>

      {/* Below the bar rather than `ml-auto` inside it: the bar is a fixed 44px of readouts, and a sentence that appears
          only when a refresh has failed should not be reflowing them at exactly
          that moment. */}
      {board.isError ? (
        <p className="shrink-0 border-b border-line bg-panel px-3 py-1.5 text-meta text-warn" role="status">
          {t("funding.stale")}
        </p>
      ) : null}

      <FundingMatrix snapshot={snapshot} loading={board.isPending} />
    </div>
  );
}
