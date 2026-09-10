import { existsSync, readFileSync } from "node:fs";
import { resolve } from "node:path";

import { describe, expect, it } from "vitest";

import { PUBLIC_PATHS } from "./public-paths";

/**
 * The mock's public carve-out and the auth edge's must name the same routes.
 *
 * They are separate implementations on purpose — the mock exists to catch a
 * screen that renders against a session the server would have rejected, and it
 * cannot do that if it is generated from the thing it is checking. But
 * "separate" and "disagreeing" are different states, and only one of them is
 * useful: a path public in the mock and private at the edge ships a screen that
 * works offline and 401s in production, while the reverse hides a route the
 * edge has genuinely opened.
 *
 * So the list is duplicated and the duplication is pinned. The Go side has its
 * own guard (`contract_test.go` compares it against trading's `publicDataRoutes`);
 * this is the third leg, and it reads `routes.go` as text rather than through a
 * build step because a test that needed a toolchain to run is a test nobody
 * runs.
 */
// Resolved from the vitest root (`web/`) rather than from import.meta.url,
// which is an http URL under the jsdom environment.
const routesGo = resolve(process.cwd(), "../services/authedge/proxy/routes.go");

/** Every `Path:` on a route line that also carries `Public: true`. */
function publicPathsInGoTable(source: string): string[] {
  const found: string[] = [];
  for (const line of source.split("\n")) {
    if (!line.includes("Public: true")) continue;
    const match = /Path:\s*"([^"]+)"/.exec(line);
    if (match?.[1]) found.push(match[1]);
  }
  return found.sort();
}

describe("the mock's public carve-out", () => {
  it("can find the auth edge's route table", () => {
    // Named separately so a moved file reads as "the test cannot find its
    // subject" rather than as a carve-out disagreement.
    expect(existsSync(routesGo), `expected the auth edge route table at ${routesGo}`).toBe(true);
  });

  const source = existsSync(routesGo) ? readFileSync(routesGo, "utf8") : "";

  it("reads the auth edge's route table", () => {
    // Guards the parse itself. If routes.go were reformatted so `Public: true`
    // no longer shared a line with `Path:`, every extraction below would come
    // back empty and the comparison would pass by matching nothing.
    expect(source).toContain("Public bool");
    expect(publicPathsInGoTable(source).length).toBeGreaterThan(0);
  });

  it("names exactly the routes the auth edge marks Public", () => {
    expect(publicPathsInGoTable(source)).toEqual([...PUBLIC_PATHS].sort());
  });

  it("contains the public market socket and excludes private and retired sockets", () => {
    expect(PUBLIC_PATHS.has("/api/v1/market/realtime/ws")).toBe(true);
    expect(PUBLIC_PATHS.has("/api/v1/realtime/ws")).toBe(false);
    expect(PUBLIC_PATHS.has("/api/v1/account/realtime/ws")).toBe(false);
    expect(PUBLIC_PATHS.has("/api/v1/realtime/stream")).toBe(false);
  });
});
