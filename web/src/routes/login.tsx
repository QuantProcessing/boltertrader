import { useNavigate, useSearch } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";

import { isApiError } from "@/api/errors";
import { sessionStore } from "@/app/session";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/field";
import { useT } from "@/lib/i18n";

/* ---------------------------------------------------------------------------
 * The only unauthenticated screen.
 *
 * Deliberately plain: one card, two fields, one primary action. There is
 * nothing here to explore, and a login page that fills the viewport with
 * marketing is a login page that took longer to sign in through.
 * ------------------------------------------------------------------------- */

export function LoginPage() {
  const t = useT();
  const navigate = useNavigate();
  const { redirect } = useSearch({ from: "/login" });
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setPending(true);
    setError(null);
    try {
      await sessionStore.login(username, password);
      await navigate({ href: redirect ?? "/trade", replace: true });
    } catch (cause) {
      // The server's message is shown as written. It says "invalid username or
      // password" without saying which, and rewording it here would either lose
      // that property or invent a distinction the server refused to make.
      setError(isApiError(cause) ? cause.message : t("login.unreachable"));
      setPassword("");
    } finally {
      setPending(false);
    }
  }

  return (
    <div className="flex min-h-dvh items-center justify-center bg-bg px-4 text-text">
      <div className="w-full max-w-80">
        <div className="mb-6">
          <h1 className="text-title font-semibold">{t("login.wordmark")}</h1>
          <p className="mt-1 text-meta text-muted">{t("login.subtitle")}</p>
        </div>

        <form onSubmit={(event) => void submit(event)} className="flex flex-col gap-3.5">
          <Field label={t("login.username")} required>
            {(props) => (
              <Input
                {...props}
                name="username"
                value={username}
                onChange={(event) => setUsername(event.target.value)}
                autoComplete="username"
                autoFocus
                required
              />
            )}
          </Field>

          <Field label={t("login.password")} required error={error}>
            {(props) => (
              <Input
                {...props}
                name="password"
                type="password"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                autoComplete="current-password"
                required
              />
            )}
          </Field>

          <Button type="submit" variant="primary" size="lg" disabled={pending} className="mt-1 w-full justify-center">
            {pending ? t("login.pending") : t("login.submit")}
          </Button>
        </form>
      </div>
    </div>
  );
}
