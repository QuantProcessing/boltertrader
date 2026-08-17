import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { App } from "@/app/App";
import "@/styles/app.css";

const container = document.getElementById("root");

if (!container) {
  throw new Error("#root is missing from index.html");
}

const root = createRoot(container);

/**
 * The mock edge is started before the first render, and only when asked for.
 *
 * Awaited rather than fired alongside `render`, because the app's first act is
 * to fetch `/api/v1/me`: a worker that is still activating would let that one
 * request through to a dev server with no API behind it, and the app would boot
 * signed out for reasons that have nothing to do with the session.
 *
 * The import is dynamic so `msw` and the fixtures stay out of the production
 * bundle entirely — `VITE_BOLTER_MOCK` is unset in every build but `dev:mock`,
 * and this branch is then dead code Rollup drops along with everything it
 * reaches.
 */
async function boot(): Promise<void> {
  if (import.meta.env.VITE_BOLTER_MOCK) {
    const { startWorker } = await import("@/mocks/browser");
    await startWorker();
  }
  root.render(
    <StrictMode>
      <App />
    </StrictMode>,
  );
}

void boot();
