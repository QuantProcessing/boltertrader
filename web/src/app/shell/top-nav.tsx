import { Link, useLocation, useNavigate } from "@tanstack/react-router";
import { Eye, Languages, LogIn, LogOut, Settings } from "lucide-react";

import { modeStore, useReadOnly } from "@/app/mode";
import { preferencesStore, usePreferences } from "@/app/preferences";
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
import { PopoverClose, PopoverContent, PopoverRoot, PopoverTrigger } from "@/components/ui/popover";
import { Switch } from "@/components/ui/switch";
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
    <header className="flex h-nav shrink-0 items-center gap-6 border-b border-line bg-panel px-6">
      <div className="flex shrink-0 items-center border-r border-line pr-5">
        <Link to="/trade" className="flex h-8 items-center" aria-label="Bolter">
          <img
            src="/brand/bolter-wordmark-transparent.png"
            alt="Bolter"
            className="h-[29px] w-auto object-contain"
          />
        </Link>
      </div>

      <nav aria-label={t("nav.sections")} className="flex min-w-0 items-center gap-0.5">
        {SECTIONS.slice(0, 2).map((section) => (
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
            // Match the pathname independently of search parameters.
            // a separate grouped menu, so `/funding` must not claim its routes.
            activeOptions={{ exact: true, includeSearch: false }}
            title={section.private && readOnly ? privateHint : undefined}
          >
            {t(section.label)}
            {section.private && readOnly ? <span className="sr-only"> — {privateHint}</span> : null}
          </Link>
        ))}
        {SECTIONS.slice(2).map((section) => (
          <Link
            key={section.to}
            to={section.to}
            className={cn(
              "flex h-7 items-center rounded-card px-2.5 text-title",
              "text-muted transition-colors hover:bg-raised hover:text-text",
              "focus-visible:ring-accent focus-visible:ring-2 focus-visible:outline-none",
              section.private && readOnly && "text-dim",
            )}
            activeProps={{ className: "bg-raised font-semibold text-text" }}
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
        <LanguageMenu />
        <SettingsPopover />
      </div>
    </header>
  );
}

function LanguageMenu() {
  const locale = useLocale();
  const t = useT();
  const current = LOCALES.find((option) => option.value === locale);

  return (
    <MenuRoot>
      <MenuTrigger asChild>
        <Button variant="ghost" size="sm" aria-label={t("utility.language")} className="gap-1.5 px-2">
          <Languages aria-hidden className="size-3.5" />
          <span>{current?.short ?? locale.toUpperCase()}</span>
        </Button>
      </MenuTrigger>
      <MenuContent align="end">
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
      </MenuContent>
    </MenuRoot>
  );
}

function SettingsPopover() {
  const { state } = useSession();
  const preferences = usePreferences();
  const navigate = useNavigate();
  const t = useT();

  return (
    <PopoverRoot>
      <PopoverTrigger asChild>
        <Button variant="ghost" size="icon" aria-label={t("utility.quickSettings")}>
          <Settings aria-hidden className="size-4" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-80">
        <div className="border-b border-line px-3 py-2.5">
          <h2 className="text-title font-semibold text-text">{t("utility.quickSettings")}</h2>
          <p className="mt-0.5 text-meta text-dim">{t("utility.quickSettings.detail")}</p>
        </div>

        <div className="p-3">
          <span className="th">{t("utility.trading")}</span>
          <div className="mt-2 flex items-start justify-between gap-4 rounded-card bg-fill p-2.5">
            <div className="min-w-0">
              <label htmlFor="confirm-orders" className="block cursor-pointer text-base font-medium text-text">
                {t("utility.confirmOrders")}
              </label>
              <p id="confirm-orders-detail" className="mt-0.5 text-meta leading-relaxed text-dim">
                {t("utility.confirmOrders.detail")}
              </p>
            </div>
            <Switch
              id="confirm-orders"
              checked={preferences.confirmOrders}
              onCheckedChange={preferencesStore.setConfirmOrders}
              aria-describedby="confirm-orders-detail"
            />
          </div>
        </div>

        <div className="flex flex-col gap-0.5 border-t border-line p-1.5">
          <PopoverClose asChild>
            <Button
              variant="ghost"
              size="md"
              className="w-full justify-start"
              onClick={() => void navigate({ to: "/settings" })}
            >
              <Settings aria-hidden className="size-3.5 shrink-0 text-muted" />
              {t("utility.accountSettings")}
            </Button>
          </PopoverClose>

          {state ? (
            <PopoverClose asChild>
              <Button variant="ghost" size="md" className="w-full justify-start" onClick={() => void signOut()}>
                <LogOut aria-hidden className="size-3.5 shrink-0 text-muted" />
                {t("utility.signOut")}
              </Button>
            </PopoverClose>
          ) : (
            <PopoverClose asChild>
              <Button
                variant="ghost"
                size="md"
                className="w-full justify-start"
                onClick={() => void navigate({ to: "/login", search: { redirect: "/trade" } })}
              >
                <LogIn aria-hidden className="size-3.5 shrink-0 text-muted" />
                {t("utility.signIn")}
              </Button>
            </PopoverClose>
          )}
        </div>
      </PopoverContent>
    </PopoverRoot>
  );
}

/**
 * Ending the session returns this tab to the anonymous public workspace.
 */
async function signOut(): Promise<void> {
  await sessionStore.logout();
  modeStore.choose("readOnly");
}
