import { QueryClient, QueryClientProvider, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type FormEvent } from "react";

import { api, setUnauthenticatedHandler } from "@/api/client";
import { realtime } from "@/api/realtime";
import { Blotter } from "@/features/blotter";
import { ChartPanel } from "@/features/chart";
import { AccountSwitcher, ConnectDialog } from "@/features/connect";
import { MarketHeader } from "@/features/market-header";
import { BookPanel } from "@/features/order-book";
import { OrderTicket } from "@/features/order-ticket";
import { AccountSummary } from "@/features/account-summary";
import { useConnection } from "@/state/hooks";
import { WorkspaceProvider } from "@/state/workspace";
import { PrimaryButton, cx } from "@/ui/primitives";
import { ToastProvider } from "@/ui/toast";

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: false, staleTime: 5_000 } },
});

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <ToastProvider>
        <Gate />
      </ToastProvider>
    </QueryClientProvider>
  );
}

function Gate() {
  const client = useQueryClient();
  const session = useQuery({ queryKey: ["session"], queryFn: api.session, staleTime: Infinity });
  useEffect(() => {
    setUnauthenticatedHandler(() => client.setQueryData(["session"], { authenticated: false }));
  }, [client]);
  const authenticated = session.data?.authenticated;
  useEffect(() => {
    if (authenticated) realtime.start();
    else realtime.stop();
  }, [authenticated]);

  if (session.isLoading) return null;
  if (!authenticated) return <Login />;
  return (
    <WorkspaceProvider>
      <Terminal />
      <ConnectDialog />
    </WorkspaceProvider>
  );
}

function Login() {
  const client = useQueryClient();
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.login(password);
      // Drop data cached under a previous session, but keep the session query
      // itself observed so Gate re-renders.
      client.removeQueries({ predicate: (query) => query.queryKey[0] !== "session" });
      client.setQueryData(["session"], { authenticated: true });
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="flex h-full items-center justify-center">
      <form onSubmit={submit} className="w-80 rounded-xl border border-line bg-surface p-6">
        <div className="mb-5 flex items-center justify-center gap-2 text-lg"><Logo /> Bolter</div>
        <input
          type="password" autoFocus autoComplete="current-password" placeholder="Password" value={password}
          onChange={(e) => setPassword(e.target.value)}
          className="h-10 w-full rounded-lg border border-line bg-transparent px-3 outline-none focus:border-line-strong"
        />
        <PrimaryButton type="submit" className="mt-4" disabled={busy || !password}>{busy ? "Signing in…" : "Sign in"}</PrimaryButton>
        {error && <div className="mt-3 text-center text-down">{error}</div>}
      </form>
    </div>
  );
}

function Logo() {
  return (
    <svg viewBox="0 0 32 32" className="size-6" aria-hidden><path d="M18 4 8 18h7l-2 10 11-15h-7z" fill="#50d2c1" /></svg>
  );
}

function Terminal() {
  const logout = async () => {
    await api.logout();
    queryClient.removeQueries({ predicate: (query) => query.queryKey[0] !== "session" });
    queryClient.setQueryData(["session"], { authenticated: false });
  };
  return (
    <div className="flex h-full flex-col gap-1">
      <header className="flex h-12 shrink-0 items-center gap-6 bg-bg px-4">
        <div className="flex items-center gap-2 text-md"><Logo /> Bolter</div>
        <nav className="flex gap-5"><span className="text-accent">Trade</span></nav>
        <div className="ml-auto flex items-center gap-2">
          <AccountSwitcher />
          <button onClick={logout} title="Sign out" className="flex size-8 items-center justify-center rounded-lg border border-line text-muted hover:text-text">⎋</button>
        </div>
      </header>

      {/* Desktop: chart+book | ticket on top, blotter below chart+book. */}
      <main className="grid min-h-0 flex-1 gap-1 px-1 max-[1000px]:flex max-[1000px]:flex-col max-[1000px]:overflow-y-auto min-[1001px]:grid-cols-[minmax(0,1fr)_300px_320px] min-[1001px]:grid-rows-[56px_minmax(0,1fr)_minmax(220px,35%)]">
        <div className="min-[1001px]:col-span-2"><MarketHeader /></div>
        <div className="flex min-h-[420px] min-[1001px]:col-start-1 min-[1001px]:row-start-2"><ChartPanel /></div>
        <div className="flex min-h-[420px] min-[1001px]:col-start-2 min-[1001px]:row-start-2"><BookPanel /></div>
        <aside className="flex flex-col gap-1 overflow-y-auto min-[1001px]:col-start-3 min-[1001px]:row-span-3 min-[1001px]:row-start-1">
          <OrderTicket />
          <AccountSummary />
        </aside>
        <div className="flex min-h-[260px] min-[1001px]:col-span-2 min-[1001px]:row-start-3"><Blotter /></div>
      </main>

      <StatusBar />
    </div>
  );
}

function StatusBar() {
  const state = useConnection();
  return (
    <footer className="flex h-6 shrink-0 items-center px-2">
      <span className={cx("flex items-center gap-1.5 rounded-md border px-2 text-xs", state === "online" ? "border-up/60 text-up" : "border-down/60 text-down")}>
        <span className={cx("size-1.5 rounded-full", state === "online" ? "bg-up" : "bg-down")} />
        {state === "online" ? "Online" : state === "connecting" ? "Connecting" : "Reconnecting"}
      </span>
    </footer>
  );
}
