import { Link, useNavigate } from "@tanstack/react-router";
import { Command as CommandIcon, Languages, LogIn, LogOut, Menu as MenuIcon, Settings } from "lucide-react";

import { modeStore, useReadOnly } from "@/app/mode";
import { useSession, sessionStore } from "@/app/session";
import { Button } from "@/components/ui/button";
import {
  MenuContent,
  MenuItem,
  MenuLabel,
  MenuRadioGroup,
  MenuRadioItem,
  MenuRoot,
  MenuSeparator,
  MenuTrigger,
} from "@/components/ui/menu";
import { StateBadge } from "@/components/ui/state-badge";
import { LOCALES, localeStore, useLocale, useT } from "@/lib/i18n";
import type { Key } from "@/lib/i18n/en";
import { cn } from "@/lib/utils";

/* ---------------------------------------------------------------------------
 * The top bar: workspace controls and identity on the left, sections in the
 * middle, the palette on the right.
 *
 * One bar, 44px, present on every route inside the app — the operator should
 * never have to look in a second place to find out what this session is allowed
 * to do, or how to leave the screen they are on. `--spacing-bar` is what makes
 * it the same height as the instrument strip below it, so the two read as one
 * header rather than two stacked rules.
 *
 * The utility cluster sits at the far left, before the wordmark: settings,
 * language and sign-in are workspace-level and belong together, and a menu
 * hidden behind an avatar in the opposite corner is a menu nobody finds. There
 * is no theme control in it, deliberately — this product is dark and has no
 * second theme to switch to.
 * ------------------------------------------------------------------------- */

interface Section {
  to: string;
  label: Key;
  /** Needs a session; shown to a read-only visitor as a way in, not a wall. */
  private?: boolean;
}

const SECTIONS: Section[] = [
  { to: "/trade", label: "nav.trade" },
  { to: "/funding", label: "nav.funding" },
  // Arbitrage looks like it belongs with the two above and does not: its
  // handler asks for an identity before it does anything else, so it stays
  // private until that changes upstream.
  { to: "/funding/arbitrage", label: "nav.arbitrage", private: true },
  { to: "/portfolio", label: "nav.portfolio", private: true },
  { to: "/activity", label: "nav.activity", private: true },
  { to: "/settings", label: "nav.settings", private: true },
];

export function TopNav({ onOpenPalette }: { onOpenPalette: () => void }) {
  const { state } = useSession();
  const readOnly = useReadOnly();
  const t = useT();

  return (
    <header className="flex h-bar shrink-0 items-center gap-4 border-b border-line bg-panel px-3">
      <div className="flex shrink-0 items-center gap-2.5">
        <UtilityMenu />
        <Link to="/trade" className="text-title font-semibold text-text">
          Bolter
        </Link>
        <ModeBadge readOnly={readOnly} />
      </div>

      <nav aria-label={t("nav.sections")} className="flex min-w-0 items-center gap-0.5">
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
              // Dimmed rather than removed or disabled. A read-only visitor
              // should be able to see the whole shape of the product and find
              // out what signing in is for; following one lands on the login
              // form, which is the answer to the question they just asked.
              section.private && readOnly && "text-dim",
            )}
            activeProps={{ className: "border-b-accent font-semibold text-text" }}
            // Exact, and blind to the query string. Without the first, `/funding`
            // would also light up on `/funding/arbitrage` and two sections would
            // claim to be the current one; without the second, the arbitrage
            // section would go dark the moment a pair was picked, because the
            // pair lives in the search params.
            activeOptions={{ exact: true, includeSearch: false }}
            title={section.private && readOnly ? t("nav.signInToUse") : undefined}
          >
            {t(section.label)}
            {section.private && readOnly ? <span className="sr-only"> — {t("nav.signInToUse")}</span> : null}
          </Link>
        ))}
      </nav>

      <div className="ml-auto flex shrink-0 items-center gap-1.5">
        <Button variant="outline" size="sm" onClick={onOpenPalette} className="gap-1.5 text-muted">
          <CommandIcon aria-hidden className="size-3" />
          <span className="text-meta">K</span>
          <span className="sr-only">{t("utility.palette")}</span>
        </Button>
        {state ? (
          <span className="max-w-32 truncate border-l border-line pl-2 text-meta text-muted" title={state.principal.username}>
            {state.principal.username}
          </span>
        ) : null}
      </div>
    </header>
  );
}

/**
 * What this session is allowed to do, stated permanently rather than on demand.
 *
 * The badge that used to live here said `LIVE` unconditionally, because for a
 * while there was genuinely nothing else to be: the mode axis had been deleted
 * along with the test environments. A second state is back, but it is not that
 * one — it is a property of *this session*, not of the process, and read-write
 * still reaches the same real exchange it always did.
 *
 * So read-write keeps `danger`, and for the same reason as before: what it
 * describes is not a risk, it is the normal case. Read-only is neutral, because
 * a warning colour on a state that cannot spend anything is how an operator
 * learns to stop reading the badge.
 *
 * A permanent badge does habituate either way, which is why it is not carrying
 * the weight alone — the ticket's preview-and-seal step is the acute warning,
 * and this is the ambient half.
 */
function ModeBadge({ readOnly }: { readOnly: boolean }) {
  const t = useT();
  return readOnly ? (
    <StateBadge tone="neutral" label={t("mode.readOnly")} detail={t("mode.readOnly.detail")} />
  ) : (
    <StateBadge tone="danger" label={t("mode.readWrite")} detail={t("mode.readWrite.detail")} />
  );
}

function UtilityMenu() {
  const { state } = useSession();
  const locale = useLocale();
  const readOnly = useReadOnly();
  const navigate = useNavigate();
  const t = useT();

  return (
    <MenuRoot>
      <MenuTrigger asChild>
        <Button variant="ghost" size="icon" aria-label={t("utility.menu")}>
          <MenuIcon aria-hidden className="size-4" />
        </Button>
      </MenuTrigger>
      <MenuContent align="start">
        <MenuItem
          disabled={readOnly}
          onSelect={() => void navigate({ to: "/settings" })}
          title={readOnly ? t("nav.signInToUse") : undefined}
        >
          <Settings aria-hidden className="size-3.5 shrink-0 text-muted" />
          {t("utility.settings")}
        </MenuItem>

        <MenuSeparator />

        <MenuLabel className="flex items-center gap-2">
          <Languages aria-hidden className="size-3.5 shrink-0" />
          {t("utility.language")}
        </MenuLabel>
        <MenuRadioGroup value={locale} onValueChange={(next) => localeStore.set(next as typeof locale)}>
          {LOCALES.map((option) => (
            <MenuRadioItem key={option.value} value={option.value}>
              {option.label}
            </MenuRadioItem>
          ))}
        </MenuRadioGroup>

        <MenuSeparator />

        {state ? (
          <MenuItem onSelect={() => void signOut()}>
            <LogOut aria-hidden className="size-3.5 shrink-0 text-muted" />
            {t("utility.signOut")}
          </MenuItem>
        ) : (
          <MenuItem onSelect={() => void navigate({ to: "/login", search: { redirect: "/trade" } })}>
            <LogIn aria-hidden className="size-3.5 shrink-0 text-muted" />
            {t("utility.signIn")}
          </MenuItem>
        )}
      </MenuContent>
    </MenuRoot>
  );
}

/**
 * Ending the session ends the mode with it.
 *
 * Without the reset, an operator who signs out is left holding `readWrite` with
 * nothing to authorise it — a state whose only symptom is that every write is
 * refused, and which suppresses the entry dialog that would have explained why.
 */
async function signOut(): Promise<void> {
  await sessionStore.logout();
  modeStore.reset();
}
