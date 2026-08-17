import { setupWorker } from "msw/browser";

import { handlers } from "./handlers";

/**
 * The dev-server mock, started from `main.tsx` behind `VITE_BOLTER_MOCK`.
 *
 * A service worker rather than a patched `fetch`: the app's own client is left
 * completely untouched, so what runs under the mock is the same code path that
 * runs against the auth edge — same relative URLs, same cookie, same CSRF
 * header. Anything the mock catches, the real edge would have received.
 */
export const worker = setupWorker(...handlers);

/**
 * `onUnhandledRequest: "bypass"` for everything, because Vite's own traffic
 * (modules, HMR, fonts) shares this origin and must reach the dev server. The
 * `/api/v1` catch-all in `handlers.ts` is what makes an unimplemented API route
 * visible instead of silently escaping to a server that is not there.
 */
export const startWorker = (): Promise<ServiceWorkerRegistration | undefined> =>
  worker.start({ onUnhandledRequest: "bypass", quiet: true });
