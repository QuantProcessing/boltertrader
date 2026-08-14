import { Link } from "@tanstack/react-router";
import { Command as CommandIcon, LogOut, Monitor, Moon, Sun } from "lucide-react";

import { useTheme, type ThemePreference } from "@/app/theme";
import { useSession, sessionStore } from "@/app/session";
import { Button } from "@/components/ui/button";
import { StateBadge } from "@/components/ui/state-badge";
import { cn } from "@/lib/utils";

/* ---------------------------------------------------------------------------
 * The top bar: identity of the workspace on the left, sections in the middle,
 * controls on the right.
 *
 * One bar, 44px, present on every authenticated route — the operator should
 * never have to look in a second place to find out which mode they are in or
 * how to leave the screen they are on. `--spacing-bar` is what makes it the
 * same height as the instrument strip below it, so the two read as one header
 * rather than two stacked rules.
 * ------------------------------------------------------------------------- */

interface Section {
  to: string;
  label: string;
}

const SECTIONS: Section[] = [
  { to: "/trade", label: "Trade" },
  { to: "/portfolio", label: "Portfolio" },
  { to: "/activity", label: "Activity" },
  { to: "/settings", label: "Settings" },
];

export function TopNav({ onOpenPalette }: { onOpenPalette: () => void }) {
  const { state } = useSession();
  const mode = state?.principal.mode ?? "test";

  return (
    <header className="flex h-bar shrink-0 items-center gap-4 border-b border-line bg-panel px-3">
      <div className="flex shrink-0 items-center gap-2.5">
        <Link to="/trade" className="text-title font-semibold tracking-tight text-text">
          Bolter
        </Link>
        <ModeChip mode={mode} />
      </div>

      <nav aria-label="Sections" className="flex min-w-0 items-center gap-0.5">
        {SECTIONS.map((section) => (
          <Link
            key={section.to}
            to={section.to}
            // The active section is marked by weight and a rule beneath it, not
            // by colour alone — the accent is reserved for things that respond
            // to a click, and 1.4.1 rules out carrying the state in hue anyway.
            className={cn(
              "flex h-7 items-center rounded-sm border-b-2 border-transparent px-2.5 text-base",
              "text-muted transition-colors hover:bg-raised hover:text-text",
              "focus-visible:ring-accent focus-visible:ring-2 focus-visible:outline-none",
            )}
            activeProps={{ className: "border-b-accent font-semibold text-text" }}
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
        <ThemeToggle />
        {state ? <SignOut username={state.principal.username} /> : null}
      </div>
    </header>
  );
}

/**
 * Which mode this session trades in, stated permanently rather than on demand.
 *
 * Live is marked read-only because it is: the server refuses private writes in
 * live outright (`LIVE_PRIVATE_WRITE_DISABLED`). Saying so in the header is the
 * difference between an operator who knows why the order button did nothing and
 * one who files a bug.
 */
function ModeChip({ mode }: { mode: "test" | "live" }) {
  return mode === "live" ? (
    <StateBadge tone="danger" label="LIVE · READ-ONLY" detail="Live sessions cannot place private orders" />
  ) : (
    <StateBadge tone="warn" label="TEST" detail="Orders are placed against the venue's test environment" />
  );
}

const NEXT_PREFERENCE: Record<ThemePreference, ThemePreference> = {
  dark: "light",
  light: "system",
  system: "dark",
};

const PREFERENCE_LABEL: Record<ThemePreference, string> = {
  dark: "Dark",
  light: "Light",
  system: "System",
};

function ThemeToggle() {
  const { preference, setPreference } = useTheme();
  const Icon = preference === "dark" ? Moon : preference === "light" ? Sun : Monitor;

  return (
    <Button
      variant="ghost"
      size="icon"
      onClick={() => setPreference(NEXT_PREFERENCE[preference])}
      // The label names the current state, not the action. A button announced
      // as "switch to light" is ambiguous about which one is on right now, and
      // that is the thing the operator is actually asking.
      aria-label={`Theme: ${PREFERENCE_LABEL[preference]}. Activate for ${PREFERENCE_LABEL[NEXT_PREFERENCE[preference]].toLowerCase()}.`}
      title={`Theme: ${PREFERENCE_LABEL[preference]}`}
    >
      <Icon aria-hidden className="size-3.5" />
    </Button>
  );
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
