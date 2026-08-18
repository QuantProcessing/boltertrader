import { Link } from "@tanstack/react-router";
import { Command as CommandIcon, LogOut } from "lucide-react";

import { useSession, sessionStore } from "@/app/session";
import { Button } from "@/components/ui/button";
import { StateBadge } from "@/components/ui/state-badge";
import { cn } from "@/lib/utils";

/* ---------------------------------------------------------------------------
 * The top bar: identity of the workspace on the left, sections in the middle,
 * controls on the right.
 *
 * One bar, 44px, present on every authenticated route — the operator should
 * never have to look in a second place to find out that this session reaches a
 * real exchange, or how to leave the screen they are on. `--spacing-bar` is what makes it the
 * same height as the instrument strip below it, so the two read as one header
 * rather than two stacked rules.
 * ------------------------------------------------------------------------- */

interface Section {
  to: string;
  label: string;
}

const SECTIONS: Section[] = [
  { to: "/trade", label: "Trade" },
  { to: "/funding", label: "Funding" },
  { to: "/funding/arbitrage", label: "Arbitrage" },
  { to: "/portfolio", label: "Portfolio" },
  { to: "/activity", label: "Activity" },
  { to: "/settings", label: "Settings" },
];

export function TopNav({ onOpenPalette }: { onOpenPalette: () => void }) {
  const { state } = useSession();

  return (
    <header className="flex h-bar shrink-0 items-center gap-4 border-b border-line bg-panel px-3">
      <div className="flex shrink-0 items-center gap-2.5">
        <Link to="/trade" className="text-title font-semibold text-text">
          Bolter
        </Link>
        <LiveBadge />
      </div>

      <nav aria-label="Sections" className="flex min-w-0 items-center gap-0.5">
        {SECTIONS.map((section) => (
          <Link
            key={section.to}
            to={section.to}
            // The active section is marked by weight and a rule beneath it as
            // well as by colour: 1.4.1 rules out carrying the state in hue
            // alone, whatever the palette allows.
            className={cn(
              "flex h-7 items-center rounded-control border-b-2 border-transparent px-2.5 text-base",
              "text-muted transition-colors hover:bg-raised hover:text-text",
              "focus-visible:ring-accent focus-visible:ring-2 focus-visible:outline-none",
            )}
            activeProps={{ className: "border-b-accent font-semibold text-text" }}
            // Exact, and blind to the query string. Without the first, `/funding`
            // would also light up on `/funding/arbitrage` and two sections would
            // claim to be the current one; without the second, the arbitrage
            // section would go dark the moment a pair was picked, because the
            // pair lives in the search params.
            activeOptions={{ exact: true, includeSearch: false }}
          >
            {section.label}
          </Link>
        ))}
      </nav>

      <div className="ml-auto flex shrink-0 items-center gap-1.5">
        <Button variant="outline" size="sm" onClick={onOpenPalette} className="gap-1.5 text-muted">
          <CommandIcon aria-hidden className="size-3" />
          <span className="text-meta">K</span>
          <span className="sr-only">Open the command palette</span>
        </Button>
        {state ? <SignOut username={state.principal.username} /> : null}
      </div>
    </header>
  );
}

/**
 * That this session spends real money, stated permanently rather than on demand.
 *
 * Nothing here is conditional, because there is no longer a second state to be
 * in: the mode axis is gone and the test environments went with it. A permanent
 * badge does habituate, which is why it is not carrying the weight alone — the
 * ticket's preview-and-seal step is where the acute warning lives. This is the
 * ambient half, and it is `danger` rather than `warn` because what it describes
 * is not a risk, it is the normal case.
 */
function LiveBadge() {
  return <StateBadge tone="danger" label="LIVE" detail="Orders reach a real exchange and spend real money" />;
}

function SignOut({ username }: { username: string }) {
  return (
    <div className="flex items-center gap-1.5 border-l border-line pl-2">
      <span className="max-w-32 truncate text-meta text-muted" title={username}>
        {username}
      </span>
      <Button variant="ghost" size="icon" onClick={() => void sessionStore.logout()} aria-label="Sign out">
        <LogOut aria-hidden className="size-3.5" />
      </Button>
    </div>
  );
}
