import { Moon, Sun } from "lucide-react";

import type { Mode } from "../types";
import { StatusBadge } from "../../components/StatusBadge";

interface SettingsProps { theme: "dark" | "light"; mode: Mode | string; username: string; contractVersion: string; onThemeChange: (theme: "dark" | "light") => void }

export function SettingsRoute(props: SettingsProps) {
  return <div className="route-stack settings-route">
    <header className="page-heading"><div><p className="eyebrow">Workspace / Settings</p><h1>Settings</h1></div></header>
    <section className="settings-band"><div><h2>Appearance</h2></div><div className="theme-options" role="radiogroup" aria-label="Theme"><label className={props.theme === "dark" ? "is-selected" : ""}><input type="radio" name="theme" value="dark" checked={props.theme === "dark"} onChange={() => props.onThemeChange("dark")} /><Moon />Dark</label><label className={props.theme === "light" ? "is-selected" : ""}><input type="radio" name="theme" value="light" checked={props.theme === "light"} onChange={() => props.onThemeChange("light")} /><Sun />Light</label></div></section>
    <section className="settings-band"><div><h2>Deployment</h2><p>Server configured</p></div><dl className="definition-list"><div><dt>Mode</dt><dd><StatusBadge state={props.mode} label={props.mode === "live" ? "Live" : "Test"} /></dd></div><div><dt>Signed in as</dt><dd>{props.username}</dd></div><div><dt>Contract</dt><dd><code>{props.contractVersion}</code></dd></div></dl></section>
  </div>;
}
