import assert from "node:assert/strict";
import test from "node:test";

import { createBrowserFixtureState, startBrowserFixtureServer } from "./browser-fixture-server.mjs";

async function withFixture(fn) {
  const server = await startBrowserFixtureServer({ port: 0 });
  try {
    await fn(server);
  } finally {
    await server.close();
  }
}

async function request(server, path, options = {}) {
  return fetch(`${server.origin}${path}`, {
    redirect: "manual",
    ...options,
    headers: {
      Accept: "application/json",
      ...(options.body ? { "Content-Type": "application/json" } : {}),
      ...(options.headers ?? {}),
    },
  });
}

async function json(response) {
  return response.json();
}

function cookieFrom(response) {
  return response.headers.get("set-cookie")?.split(";")[0] ?? "";
}

test("fixture login/session/logout uses cookie and CSRF without leaking credentials", async () => {
  await withFixture(async (server) => {
    const login = await request(server, "/api/v1/auth/login", {
      method: "POST",
      body: JSON.stringify({ email: "fixture@example.test", password: "unused-local-password" }),
    });
    assert.equal(login.status, 200);
    const cookie = cookieFrom(login);
    assert.match(cookie, /^ft_fixture_session=/);
    assert.doesNotMatch(cookie, /unused-local-password|fixture@example\.test/);
    const body = await json(login);
    assert.equal(body.data.principal.mode, "test");
    assert.equal(body.data.session.csrfToken.startsWith("csrf_"), true);

    const session = await request(server, "/api/v1/auth/session", { headers: { Cookie: cookie } });
    assert.equal(session.status, 200);
    assert.equal((await json(session)).data.principal.principalId, "fixture-principal");

    const denied = await request(server, "/api/v1/auth/session");
    assert.equal(denied.status, 401);

    const logout = await request(server, "/api/v1/auth/session", {
      method: "DELETE",
      headers: { Cookie: cookie, "X-CSRF-Token": body.data.session.csrfToken },
    });
    assert.equal(logout.status, 200);
    const after = await request(server, "/api/v1/auth/session", { headers: { Cookie: cookie } });
    assert.equal(after.status, 401);
  });
});

test("fixture exposes TEST readiness, opportunity, blocked preview, and explicit ledger economics", async () => {
  await withFixture(async (server) => {
    const login = await request(server, "/api/v1/auth/login", { method: "POST", body: JSON.stringify({ email: "a", password: "b" }) });
    const cookie = cookieFrom(login);
    const csrf = (await json(login)).data.session.csrfToken;

    const readiness = await json(await request(server, "/api/v1/system/readiness", { headers: { Cookie: cookie } }));
    assert.equal(readiness.data.openReady, false);
    assert.equal(readiness.data.closeReady, true);
    assert.equal(readiness.data.recoveryReady, true);
    assert.equal(readiness.data.readinessReason, "fixture_open_blocked_close_ready");

    const opportunities = await json(await request(server, "/api/v1/opportunities", { headers: { Cookie: cookie } }));
    assert.equal(opportunities.data.items.length, 1);
    assert.equal(opportunities.data.items[0].twoLegWriteReady, false);
    assert.equal(typeof opportunities.data.items[0].netAprPct, "string");
    assert.equal(typeof opportunities.data.items[0].calculationInputs.longLeg.currentFunding.rate, "string");

    const positions = await json(await request(server, "/api/v1/positions", { headers: { Cookie: cookie } }));
    assert.equal(positions.data.items[0].unrealizedPnlUsd, "6.00000000");
    assert.equal("unrealizedTradingPnl" in positions.data.items[0], false);

    const preview = await json(await request(server, "/api/v1/execution-previews", {
      method: "POST",
      headers: { Cookie: cookie, "X-CSRF-Token": csrf },
      body: JSON.stringify({ opportunityId: "fixture-opp-btc", capital: "5000", leverage: 1 }),
    }));
    assert.equal(preview.data.canSubmit, false);
    assert.equal(preview.data.blockers[0].code, "fixture_open_writes_blocked");
    assert.equal(typeof preview.data.estimatedFeesUsd, "string");
    assert.equal(preview.data.fixtureOnly, true);

    const portfolio = await json(await request(server, "/api/v1/ledger/portfolio", { headers: { Cookie: cookie } }));
    assert.equal(portfolio.data.verificationStatus, "verified");
    assert.equal(portfolio.data.watermark.verified, true);
    assert.equal(typeof portfolio.data.totalNetPnl, "string");
    assert.equal(portfolio.data.conversionSource, "fixture-static-usdt-usd");
    assert.equal(portfolio.data.formulaVersion, "fixture-ledger-v1");
  });
});

test("fixture stream emits an authenticated heartbeat immediately after connect", async () => {
  await withFixture(async (server) => {
    const login = await request(server, "/api/v1/auth/login", { method: "POST", body: JSON.stringify({ email: "a", password: "b" }) });
    const cookie = cookieFrom(login);

    const streamResponse = await request(server, "/api/v1/stream", { headers: { Cookie: cookie, Accept: "text/event-stream" } });
    const reader = streamResponse.body.getReader();
    const chunk = await Promise.race([
      reader.read(),
      new Promise((_, reject) => setTimeout(() => reject(new Error("fixture stream did not emit an initial frame")), 250)),
    ]);
    const text = new TextDecoder().decode(chunk.value);
    await reader.cancel();

    assert.match(text, /event: heartbeat/);
    assert.match(text, /"kind":"heartbeat"/);
    assert.match(text, /"principalId":"fixture-principal"/);
    assert.match(text, /"mode":"test"/);
  });
});

test("fixture close mutates open and closed position ledgers and emits resnapshot SSE", async () => {
  await withFixture(async (server) => {
    const login = await request(server, "/api/v1/auth/login", { method: "POST", body: JSON.stringify({ email: "a", password: "b" }) });
    const cookie = cookieFrom(login);
    const csrf = (await json(login)).data.session.csrfToken;

    const streamResponse = await request(server, "/api/v1/stream", { headers: { Cookie: cookie, Accept: "text/event-stream" } });
    const reader = streamResponse.body.getReader();

    const close = await request(server, "/api/v1/positions/fixture-position-open/close", {
      method: "POST",
      headers: { Cookie: cookie, "X-CSRF-Token": csrf, "Idempotency-Key": "idem_fixture_close" },
    });
    assert.equal(close.status, 200);
    assert.equal((await json(close)).data.state, "completed");

    const positions = await json(await request(server, "/api/v1/positions", { headers: { Cookie: cookie } }));
    assert.equal(positions.data.items.length, 0);
    const closed = await json(await request(server, "/api/v1/positions?state=closed", { headers: { Cookie: cookie } }));
    assert.equal(closed.data.items.length, 1);
    assert.equal(closed.data.items[0].closedTradingUpnl, "0.00000000");
    assert.equal(closed.data.items[0].residual.reportingAssetNotional, "0.00000000");
    assert.equal(closed.data.items[0].actualFundingCashFlow, "2.74000000");

    const portfolio = await json(await request(server, "/api/v1/ledger/portfolio", { headers: { Cookie: cookie } }));
    assert.equal(portfolio.data.openPositionCount, 0);
    assert.equal(portfolio.data.closedPositionCount, 1);
    assert.equal(portfolio.data.totalNetPnl, "7.49000000");

    const chunk = await reader.read();
    const text = new TextDecoder().decode(chunk.value);
    reader.cancel();
    assert.match(text, /event: resnapshot\.required/);
    assert.match(text, /"scope":\["positions","closedPositions","portfolio","verification","activities","auditEvents","executions"\]/);
  });
});

test("fixture state redaction omits secrets and raw cookies from audit logs", () => {
  const state = createBrowserFixtureState();
  state.recordAudit("auth.login", { email: "user@example.test", password: "secret", cookie: "ft_fixture_session=abc" });
  assert.doesNotMatch(JSON.stringify(state.auditEvents), /secret|ft_fixture_session=abc/);
  assert.match(JSON.stringify(state.auditEvents), /\[redacted\]/);
});
