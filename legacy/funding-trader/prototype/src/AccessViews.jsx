"use client";

import { useState } from "react";

function SurfaceStatus({ tone = "neutral", children }) {
  return <span className={`status-tag status-tag--${tone}`}>{children}</span>;
}

export function LoginScreen({ onLogin, theme, onToggleTheme, loading = false, sessionExpired = false, systemStatus = null }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const handleSubmit = async (event) => {
    event.preventDefault();
    if (busy) return;
    setBusy(true);
    setError("");
    const result = await onLogin({ email: email.trim(), password });
    setBusy(false);
    if (!result.ok) setError(result.message);
  };

  return (
    <main className="login-screen">
      <button
        className="theme-button login-theme-toggle"
        type="button"
        onClick={onToggleTheme}
        aria-label={`切换为${theme === "dark" ? "浅色" : "深色"}主题`}
      >
        {theme === "dark" ? "浅色主题" : "深色主题"}
      </button>
      <section className="login-context" aria-labelledby="login-product-title">
        <div className="login-wordmark">FUNDING <span>/</span> ARB</div>
        <div>
          <div className="eyebrow">AUTHENTICATED TERMINAL</div>
          <h1 id="login-product-title">自费套利工作台</h1>
          <p>登录后查看实时机会、系统状态、当前持仓、活动记录与审计事件。</p>
        </div>
        <dl className="login-facts">
          <div><dt>访问方式</dt><dd>后端会话认证</dd></div>
          <div><dt>交易门槛</dt><dd>两腿写入能力就绪</dd></div>
          <div><dt>当前模式</dt><dd>{systemStatus?.modeLabel ?? "等待后端状态"}</dd></div>
        </dl>
      </section>

      <section className="login-panel" aria-labelledby="login-heading">
        <div className="login-panel__heading">
          <div>
            <div className="eyebrow">ACCOUNT ACCESS</div>
            <h2 id="login-heading">邮箱登录</h2>
          </div>
          <SurfaceStatus tone={systemStatus?.ready ? "positive" : "warning"}>
            {systemStatus?.ready ? "就绪" : "检查中"}
          </SurfaceStatus>
        </div>

        <form className="login-form" onSubmit={handleSubmit}>
          <label className="stacked-field">
            <span>邮箱</span>
            <input
              type="email"
              autoComplete="username"
              value={email}
              onChange={(event) => setEmail(event.target.value)}
              required
              disabled={loading || busy}
            />
          </label>
          <label className="stacked-field">
            <span>密码</span>
            <input
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              required
              disabled={loading || busy}
            />
          </label>

          {sessionExpired && <div className="form-message form-message--danger" role="alert">会话已过期，请重新登录。</div>}
          {error && <div className="form-message form-message--danger" role="alert">{error}</div>}

          <button className="primary-button login-submit" type="submit" disabled={loading || busy}>
            {busy || loading ? "正在验证…" : "进入工作台"}
          </button>
        </form>

        <p className="login-help">账号、权限与产品模式由后端服务控制。</p>
      </section>
    </main>
  );
}
