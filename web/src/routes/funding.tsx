import { AlertTriangle } from "lucide-react";

import { EmptyState } from "@/components/ui/empty-state";
import { Metric } from "@/components/ui/panel";
import { FundingMatrix } from "@/features/funding/matrix";
import { useFundingBoard } from "@/features/funding/queries";
import { formatTimestamp } from "@/lib/format";

/* ---------------------------------------------------------------------------
 * Funding: ten venues quoting the same perpetuals, side by side.
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
  const board = useFundingBoard();
  const snapshot = board.data?.value;
  const budget = board.data?.rateLimit;

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
          title="The funding board is not available"
          detail={
            board.error instanceof Error
              ? board.error.message
              : "The Gateway has not published a board yet. It retries on its own."
          }
          className="flex-1"
        />
      </div>
    );
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="flex h-bar shrink-0 items-center gap-6 overflow-x-auto border-b border-line bg-panel px-3">
        <div className="flex items-center gap-2">
          <h1 className="text-title font-semibold tracking-tight text-text">Funding</h1>
        </div>

        <Metric label="Venues" value={String(venues.length)} />
        <Metric
          label="Degraded"
          value={String(degraded)}
          tone={degraded > 0 ? "down" : "none"}
        />
        <Metric
          label="Observed"
          value={snapshot ? formatTimestamp(snapshot.observedAt) : "—"}
        />
        {budget ? (
          <Metric label="Budget left" value={`${budget.remaining}/${budget.limit}`} />
        ) : null}

        {board.isError ? (
          <p className="ml-auto text-meta text-warn" role="status">
            Showing the last board — the refresh failed.
          </p>
        ) : null}
      </header>

      <FundingMatrix snapshot={snapshot} loading={board.isPending} />
    </div>
  );
}
