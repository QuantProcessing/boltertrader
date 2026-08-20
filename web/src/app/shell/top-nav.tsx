import { Link, useNavigate } from "@tanstack/react-router";
import { Eye, Languages, LogIn, LogOut, Settings } from "lucide-react";

import { modeStore, useReadOnly } from "@/app/mode";
import { useSession, sessionStore } from "@/app/session";
import { BrandMark } from "@/components/brand-mark";
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
import { LOCALES, localeStore, useLocale, useT } from "@/lib/i18n";
import type { Key } from "@/lib/i18n/en";
import { cn } from "@/lib/utils";

/* ---------------------------------------------------------------------------
 * The top bar: brand and sections on the left, session controls on the right.
 *
 * One bar, 44px, present on every route inside the app — the operator should
 * never have to look in a second place to find out what this session is allowed
 * to do, or how to leave the screen they are on. `--spacing-bar` is what makes
 * it the same height as the instrument strip below it, so the two read as one
 * header rather than two stacked rules.
 *
 * There is no theme control, deliberately — this product is dark and has no
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

export function TopNav() {
  const { state } = useSession();
  const readOnly = useReadOnly();
  const navigate = useNavigate();
  const t = useT();
  const privateHint = t("nav.signInToUse");

  return (
    <header className="flex h-nav shrink-0 items-center gap-5 border-b border-line bg-panel px-3">
      <div className="flex shrink-0 items-center gap-2.5 border-r border-line pr-4">
        <Link to="/trade" className="flex items-center gap-2 text-title font-semibold text-text">
          <BrandMark className="size-5" />
          <span>Bolter</span>
        </Link>
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
              "flex h-7 items-center rounded-card px-2.5 text-title",
              "text-muted transition-colors hover:bg-raised hover:text-text",
              "focus-visible:ring-accent focus-visible:ring-2 focus-visible:outline-none",
              // Dimmed rather than removed or disabled. A read-only visitor
              // should be able to see the whole product. The destination then
              // either asks for a session or renders without private cached
              // data; Settings gives the explicit sign-in explanation.
              section.private && readOnly && "text-dim",
            )}
            activeProps={{ className: "bg-raised font-semibold text-text" }}
            // Exact, and blind to the query string. Without the first, `/funding`
            // would also light up on `/funding/arbitrage` and two sections would
            // claim to be the current one; without the second, the arbitrage
            // section would go dark the moment a pair was picked, because the
            // pair lives in the search params.
            activeOptions={{ exact: true, includeSearch: false }}
            title={section.private && readOnly ? privateHint : undefined}
          >
            {t(section.label)}
            {section.private && readOnly ? <span className="sr-only"> — {privateHint}</span> : null}
          </Link>
        ))}
      </nav>

      <div className="ml-auto flex shrink-0 items-center gap-1.5">
        {state ? (
          <span className="max-w-32 truncate px-1.5 text-meta text-muted" title={state.principal.username}>
            {state.principal.username}
          </span>
        ) : (
          <>
            <span className="flex h-7 items-center gap-1.5 px-1.5 text-meta text-muted">
              <Eye aria-hidden className="size-3.5" />
              {t("mode.readOnly")}
            </span>
            <Button
              variant="primary"
              size="sm"
              onClick={() => void navigate({ to: "/login", search: { redirect: "/trade" } })}
            >
              {t("utility.signIn")}
            </Button>
          </>
        )}
        <UtilityMenu />
      </div>
    </header>
  );
}

function UtilityMenu() {
  const { state } = useSession();
  const locale = useLocale();
  const navigate = useNavigate();
  const t = useT();

  return (
    <MenuRoot>
      <MenuTrigger asChild>
        <Button variant="ghost" size="icon" aria-label={t("utility.menu")}>
          <Settings aria-hidden className="size-4" />
        </Button>
      </MenuTrigger>
      <MenuContent align="start">
        <MenuItem
          onSelect={() => void navigate({ to: "/settings" })}
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
 * Ending the session returns this tab to the anonymous public workspace.
 */
async function signOut(): Promise<void> {
  await sessionStore.logout();
  modeStore.choose("readOnly");
}
