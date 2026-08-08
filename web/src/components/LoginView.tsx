import { LockKeyhole, Moon, Sun } from "lucide-react";
import { type FormEvent, useState } from "react";

interface LoginViewProps {
  theme: "dark" | "light";
  busy: boolean;
  error: string;
  onLogin: (username: string, password: string) => Promise<void>;
  onThemeToggle: () => void;
}

export function LoginView({ theme, busy, error, onLogin, onThemeToggle }: LoginViewProps) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    await onLogin(username, password);
    setPassword("");
  };

  return <main className="login-view">
    <header className="login-header"><div className="brand-lockup"><div className="brand-mark" aria-hidden="true">BT</div><div><strong>BolterTrader</strong><span>Private trading workspace</span></div></div><button className="icon-button" aria-label={theme === "dark" ? "Use light theme" : "Use dark theme"} onClick={onThemeToggle}>{theme === "dark" ? <Sun /> : <Moon />}</button></header>
    <section className="login-panel" aria-labelledby="login-title">
      <div className="login-panel__icon"><LockKeyhole aria-hidden="true" /></div>
      <p className="eyebrow">Restricted access</p>
      <h1 id="login-title">Sign in</h1>
      <form onSubmit={submit}>
        <label>Email<input type="email" autoComplete="username" value={username} onChange={(event) => setUsername(event.target.value)} required autoFocus /></label>
        <label>Password<input type="password" autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} required /></label>
        {error && <div className="finding finding--danger" role="alert">{error}</div>}
        <button className="button button--primary button--wide" disabled={busy}>{busy ? "Signing in..." : "Sign in"}</button>
      </form>
    </section>
  </main>;
}
