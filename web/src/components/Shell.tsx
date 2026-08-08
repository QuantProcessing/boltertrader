import {
  Activity,
  BookOpenCheck,
  ChevronDown,
  CircleUserRound,
  Landmark,
  LayoutDashboard,
  LogOut,
  Menu,
  Moon,
  Settings,
  ShieldCheck,
  Sun,
  X,
} from "lucide-react";
import { type ReactNode, useState } from "react";

import type { Mode } from "../app/types";
import { StatusBadge } from "./StatusBadge";

const navigation = [
  { path: "/overview", label: "Overview", icon: LayoutDashboard },
  { path: "/funding", label: "Funding", icon: Landmark },
  { path: "/manual", label: "Manual", icon: BookOpenCheck },
  { path: "/accounts", label: "Accounts", icon: ShieldCheck },
  { path: "/activity", label: "Activity", icon: Activity },
  { path: "/settings", label: "Settings", icon: Settings },
];

interface ShellProps {
  currentPath: string;
  mode: Mode;
  username: string;
  ready: boolean;
  streamState: string;
  theme: "dark" | "light";
  onNavigate: (path: string) => void;
  onThemeToggle: () => void;
  onLogout: () => void;
  children: ReactNode;
}

export function Shell(props: ShellProps) {
  const [menuOpen, setMenuOpen] = useState(false);
  const selectRoute = (path: string) => {
    props.onNavigate(path);
    setMenuOpen(false);
  };

  return (
    <div className="app-shell">
      <aside className={`side-nav${menuOpen ? " side-nav--open" : ""}`} aria-label="Primary navigation">
        <div className="brand-lockup">
          <div className="brand-mark" aria-hidden="true">BT</div>
          <div><strong>BolterTrader</strong><span>Private workspace</span></div>
          <button className="icon-button side-nav__close" aria-label="Close navigation" onClick={() => setMenuOpen(false)}><X /></button>
        </div>
        <nav>
          {navigation.map(({ path, label, icon: Icon }) => (
            <button key={path} className={`nav-item${props.currentPath === path ? " nav-item--active" : ""}`} aria-label={label} aria-current={props.currentPath === path ? "page" : undefined} onClick={() => selectRoute(path)}>
              <Icon aria-hidden="true" /><span>{label}</span>
            </button>
          ))}
        </nav>
        <div className="side-nav__status">
          <div><span>Platform</span><StatusBadge state={props.ready ? "ready" : "degraded"} /></div>
          <div><span>Realtime</span><StatusBadge state={props.streamState} /></div>
        </div>
      </aside>

      <div className="shell-main">
        <header className="top-bar">
          <button className="icon-button mobile-menu" aria-label="Open navigation" onClick={() => setMenuOpen(true)}><Menu /></button>
          <div className={`environment-chip environment-chip--${props.mode}`}>
            <span className="environment-chip__dot" />{props.mode === "live" ? "Live environment" : "Test environment"}
          </div>
          <div className="top-bar__spacer" />
          <button className="icon-button" aria-label={props.theme === "dark" ? "Use light theme" : "Use dark theme"} onClick={props.onThemeToggle}>
            {props.theme === "dark" ? <Sun /> : <Moon />}
          </button>
          <div className="identity-menu">
            <CircleUserRound aria-hidden="true" /><span>{props.username}</span><ChevronDown aria-hidden="true" />
          </div>
          <button className="icon-button" aria-label="Log out" onClick={props.onLogout}><LogOut /></button>
        </header>
        <main className="workspace">{props.children}</main>
      </div>
      {menuOpen && <button className="nav-scrim" aria-label="Close navigation overlay" onClick={() => setMenuOpen(false)} />}
    </div>
  );
}
