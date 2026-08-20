import { useQuery } from "@tanstack/react-query";

import { system } from "@/api/endpoints";
import { StateBadge, type StateTone } from "@/components/ui/state-badge";
import { useT, type Key } from "@/lib/i18n";

const STATUS_KEY = ["system", "status"] as const;

export function BottomStatus() {
  const t = useT();
  const status = useQuery({
    queryKey: STATUS_KEY,
    queryFn: ({ signal }) => system.status(signal),
    refetchInterval: 10_000,
    retry: 1,
  });

  let tone: StateTone = "pending";
  let label: Key = "utility.connecting";
  if (status.isError) {
    tone = "danger";
    label = "utility.offline";
  } else if (status.data?.ready === false) {
    tone = "warn";
    label = "utility.degraded";
  } else if (status.data?.ready) {
    tone = "ok";
    label = "utility.online";
  }

  return (
    <footer className="flex h-status shrink-0 items-center gap-2 border-t border-line bg-panel px-3 text-meta text-muted">
      <StateBadge tone={tone} label={t(label)} aria-live="polite" />
      <span className="text-dim" aria-hidden="true">/</span>
      <span>{t("utility.mainnet")}</span>
    </footer>
  );
}
