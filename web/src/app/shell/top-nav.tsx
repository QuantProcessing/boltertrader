import { useState } from "react";
import { setTheme, useTheme, type Theme } from "@/app/theme";
import { Link, useLocation, useNavigate } from "@tanstack/react-router";
import {
  Activity,
  CandlestickChart,
  CircleDollarSign,
  Languages,
  LogIn,
  LogOut,
  Settings,
  Wallet,
  PanelLeftClose,
  PanelLeftOpen,
  Sun,
  Moon,
  Monitor,
} from "lucide-react";

import { useReadOnly } from "@/app/mode";
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
import {
  PopoverClose,
  PopoverContent,
  PopoverRoot,
  PopoverTrigger,
} from "@/components/ui/popover";
import { Switch } from "@/components/ui/switch";
import { LOCALES, localeStore, useLocale, useT } from "@/lib/i18n";
import type { Key } from "@/lib/i18n/en";
import { cn } from "@/lib/utils";

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

const icons = [CandlestickChart, CircleDollarSign, Wallet, Activity, Settings];

export function TopNav() {
  const t = useT();
  const readOnly = useReadOnly();
  const [collapsed, setCollapsed] = useState(() => {
    try {
      const stored = localStorage.getItem("bolter.sidebar");
      return stored ? stored === "collapsed" : window.innerWidth < 1430;
    } catch {
      return false;
    }
  });
  return (
    <aside className={cn("workspace-sidebar", collapsed && "is-collapsed")}>
      <Link to="/trade" className="workspace-brand" aria-label="Bolter">
        <span className="brand-symbol" aria-hidden>
          ⌁
        </span>
        <span className="sidebar-label">
          Bolter<span className="brand-caption">{t("workspace.caption")}</span>
        </span>
      </Link>
      <span className="sidebar-section sidebar-label">
        {t("workspace.label")}
      </span>
      <nav aria-label={t("nav.sections")}>
        {SECTIONS.map((section, i) => {
          const Icon = icons[i]!;
          return (
            <Link
              key={section.to}
              to={section.to}
              title={t(section.label)}
              className="workspace-nav-link"
              activeProps={{ className: "is-active" }}
              activeOptions={{ exact: true, includeSearch: false }}
            >
              <Icon size={19} aria-hidden />
              <span className="sidebar-label">{t(section.label)}</span>
              {section.private && readOnly ? (
                <span className="sr-only">{t("nav.signInToUse")}</span>
              ) : null}
            </Link>
          );
        })}
      </nav>
      <div className="sidebar-bottom">
        <p className="sidebar-label text-meta text-dim leading-relaxed">
          {t("workspace.description")}
        </p>
        <Button
          variant="ghost"
          size="icon"
          aria-label={t(collapsed ? "workspace.expand" : "workspace.collapse")}
          onClick={() => {
            const next = !collapsed;
            setCollapsed(next);
            try {
              localStorage.setItem(
                "bolter.sidebar",
                next ? "collapsed" : "expanded",
              );
            } catch {
              /* optional */
            }
          }}
        >
          {collapsed ? (
            <PanelLeftOpen size={18} />
          ) : (
            <PanelLeftClose size={18} />
          )}
        </Button>
      </div>
    </aside>
  );
}

export function WorkspaceToolbar() {
  const { state } = useSession();
  const { pathname } = useLocation();
  const t = useT();
  const navigate = useNavigate();
  const section = SECTIONS.find((item) => item.to === pathname);
  const { preference } = useTheme();
  const ThemeIcon =
    preference === "light" ? Sun : preference === "dark" ? Moon : Monitor;
  return (
    <header className="workspace-toolbar">
      <div className="flex items-center">
        <span className="text-meta text-dim mr-2 hidden lg:inline">
          {t("workspace.label")} /
        </span>
        <span className="font-semibold">
          {section ? t(section.label) : "Bolter"}
        </span>
        {import.meta.env.VITE_BOLTER_MOCK === "1" ? (
          <span
            className="ml-3 rounded-pill bg-fill px-2 py-1 text-micro text-dim"
            title={t("workspace.previewDetail")}
          >
            {t("workspace.preview")}
          </span>
        ) : null}
      </div>
      <div className="ml-auto flex items-center gap-1.5 min-w-0">
        <span className="session-label text-meta text-muted">
          {state ? state.principal.username : t("mode.readOnly")}
        </span>
        {!state ? (
          <Button
            variant="primary"
            size="sm"
            onClick={() =>
              void navigate({ to: "/login", search: { redirect: pathname } })
            }
          >
            {t("utility.signIn")}
          </Button>
        ) : null}
        <MenuRoot>
          <MenuTrigger asChild>
            <Button
              variant="ghost"
              size="icon"
              aria-label={t("workspace.theme")}
            >
              <ThemeIcon size={17} />
            </Button>
          </MenuTrigger>
          <MenuContent align="end">
            <MenuLabel>{t("workspace.appearance")}</MenuLabel>
            <MenuRadioGroup
              value={preference}
              onValueChange={(value) => setTheme(value as Theme)}
            >
              <MenuRadioItem value="light">
                {t("workspace.light")}
              </MenuRadioItem>
              <MenuRadioItem value="dark">{t("workspace.dark")}</MenuRadioItem>
              <MenuRadioItem value="system">
                {t("workspace.system")}
              </MenuRadioItem>
            </MenuRadioGroup>
          </MenuContent>
        </MenuRoot>
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
        <Button
          variant="ghost"
          size="sm"
          aria-label={t("utility.language")}
          className="gap-1.5 px-2"
        >
          <Languages aria-hidden className="size-3.5" />
          <span>{current?.short ?? locale.toUpperCase()}</span>
        </Button>
      </MenuTrigger>
      <MenuContent align="end">
        <MenuLabel className="flex items-center gap-2">
          <Languages aria-hidden className="size-3.5 shrink-0" />
          {t("utility.language")}
        </MenuLabel>
        <MenuRadioGroup
          value={locale}
          onValueChange={(next) => localeStore.set(next as typeof locale)}
        >
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
        <Button
          variant="ghost"
          size="icon"
          aria-label={t("utility.quickSettings")}
        >
          <Settings aria-hidden className="size-4" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-80">
        <div className="border-b border-line px-3 py-2.5">
          <h2 className="text-title font-semibold text-text">
            {t("utility.quickSettings")}
          </h2>
          <p className="mt-0.5 text-meta text-dim">
            {t("utility.quickSettings.detail")}
          </p>
        </div>

        <div className="p-3">
          <span className="th">{t("utility.trading")}</span>
          <div className="mt-2 flex items-start justify-between gap-4 rounded-card bg-fill p-2.5">
            <div className="min-w-0">
              <label
                htmlFor="confirm-orders"
                className="block cursor-pointer text-base font-medium text-text"
              >
                {t("utility.confirmOrders")}
              </label>
              <p
                id="confirm-orders-detail"
                className="mt-0.5 text-meta leading-relaxed text-dim"
              >
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
              <Button
                variant="ghost"
                size="md"
                className="w-full justify-start"
                onClick={() => void signOut()}
              >
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
                onClick={() =>
                  void navigate({
                    to: "/login",
                    search: { redirect: "/trade" },
                  })
                }
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
}
