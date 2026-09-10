import { afterAll, beforeAll, expect, it } from "vitest";

import { SESSION } from "./fixtures";
import { session } from "./db";
import { server } from "./node";

beforeAll(() => { server.listen({ onUnhandledRequest: "error" }); session.signIn(); });
afterAll(() => { session.signOut(); server.close(); });

it("does not fabricate successful responses for retired routes", async () => {
  for (const path of [
    "/api/v1/products/arbitrage/opportunities",
    "/api/v1/products/arbitrage/positions",
    "/api/v1/products/arbitrage/execution-previews",
    "/api/v1/products/arbitrage/commands",
    "/api/v1/realtime/ws",
    "/api/v1/reservations",
    "/api/v1/reservations/old",
  ]) {
    for (const method of ["GET", "POST"]) {
      const response = await fetch(new URL(path, window.location.href), {
        method, headers: { "X-CSRF-Token": SESSION.session.csrfToken },
      });
      expect(response.status, `${method} ${path}`).toBe(404);
    }
  }
});

it("rejects writes to the command read model", async () => {
 const response = await fetch(new URL("/api/v1/commands", window.location.href), {
  method: "POST", headers: { "X-CSRF-Token": SESSION.session.csrfToken },
 });
 expect(response.status).toBe(405);
});
