import assert from "node:assert/strict";
import test from "node:test";

import {
  EVENT_KINDS,
  PRIVATE_V1_CONTRACT_VERSION,
  SERVER_FILTERABLE_EVENT_KINDS,
  FundingTraderRealtime,
  realtimeDisplayStatus,
  validateRealtimeEnvelope,
} from "../src/realtime.js";

test("realtime connector registers frozen events and defaults to unfiltered stream", () => {
  const sources = [];
  class FakeEventSource {
    constructor(url, options) {
      this.url = url;
      this.options = options;
      this.listeners = new Map();
      sources.push(this);
    }
    addEventListener(name, listener) {
      this.listeners.set(name, listener);
    }
    close() {
      this.closed = true;
    }
  }

  const seen = [];
  const realtime = new FundingTraderRealtime({
    url: "/api/v1/stream",
    EventSourceImpl: FakeEventSource,
  });
  realtime.subscribe((message) => seen.push(message));
  realtime.connect({ cursor: "opaque cursor/1" });

  assert.equal(sources[0].url, "/api/v1/stream?cursor=opaque+cursor%2F1");
  assert.equal(sources[0].options.withCredentials, true);
  sources[0].onopen();
  assert.equal(realtime.getState().status, "connected");
  assert.equal(seen.at(-1).status, "connected");
  for (const kind of EVENT_KINDS) {
    assert.equal(typeof sources[0].listeners.get(kind), "function", kind);
  }

  sources[0].listeners.get("snapshot")({
    data: JSON.stringify({
      cursor: "next:cursor",
      principalId: "principal-default",
      mode: "test",
      contractVersion: PRIVATE_V1_CONTRACT_VERSION,
      data: {
        opportunities: [{
          calculationInputs: {
            longLeg: { venueEnvironment: "demo" },
            shortLeg: { venueEnvironment: "testnet" },
          },
        }],
      },
    }),
  });

  assert.equal(realtime.getState().cursor, "next:cursor");
  assert.equal(seen.at(-1).type, "snapshot");
  assert.equal(seen.at(-1).envelope.principalId, "principal-default");
  assert.equal(seen.at(-1).envelope.mode, "test");
  assert.equal(seen.at(-1).envelope.contractVersion, PRIVATE_V1_CONTRACT_VERSION);
  assert.equal(seen.at(-1).data.opportunities[0].calculationInputs.longLeg.venueEnvironment, "demo");
  assert.equal(seen.at(-1).data.opportunities[0].calculationInputs.shortLeg.venueEnvironment, "testnet");
});

test("realtime envelope validation and display states are product-mode safe", () => {
  const expected = {
    principalId: "principal-default",
    mode: "test",
    contractVersion: PRIVATE_V1_CONTRACT_VERSION,
  };

  assert.deepEqual(validateRealtimeEnvelope(expected, expected), { ok: true });
  assert.deepEqual(
    validateRealtimeEnvelope({ ...expected, mode: "live" }, expected),
    { ok: false, reason: "mode-mismatch" },
  );
  assert.deepEqual(
    validateRealtimeEnvelope({ ...expected, principalId: "other" }, expected),
    { ok: false, reason: "principal-mismatch" },
  );
  assert.deepEqual(
    validateRealtimeEnvelope({ ...expected, contractVersion: "private-v1-mode-0" }, expected),
    { ok: false, reason: "contract-version-mismatch" },
  );

  assert.equal(realtimeDisplayStatus("connected"), "stream connected");
  assert.equal(realtimeDisplayStatus("degraded"), "stream degraded");
  assert.equal(realtimeDisplayStatus("reconnecting"), "stream degraded");
  assert.equal(realtimeDisplayStatus("disconnected"), "stream disconnected");
  assert.equal(realtimeDisplayStatus("closed"), "stream disconnected");
  assert.doesNotMatch(realtimeDisplayStatus("connected"), /\blive\b/i);
});

test("realtime only sends backend-filterable kinds when explicitly requested", () => {
  const sources = [];
  class FakeEventSource {
    constructor(url) {
      this.url = url;
      this.listeners = new Map();
      sources.push(this);
    }
    addEventListener(name, listener) {
      this.listeners.set(name, listener);
    }
    close() {}
  }

  assert.deepEqual(
    SERVER_FILTERABLE_EVENT_KINDS,
    [
      "system.status",
      "opportunity.upsert",
      "alert.upsert",
      "execution.upsert",
      "position.upsert",
      "activity.append",
      "audit.append",
      "ledger.funding_payment.append",
      "ledger.position.verification_changed",
      "ledger.portfolio.updated",
      "verification.status",
      "resnapshot.required",
    ],
  );

  const realtime = new FundingTraderRealtime({ EventSourceImpl: FakeEventSource });
  realtime.connect({ kinds: ["execution.upsert", "heartbeat", "snapshot", "resnapshot.required"] });
  assert.equal(sources[0].url, "/api/v1/stream?kind=execution.upsert&kind=resnapshot.required");
  assert.equal(typeof sources[0].listeners.get("snapshot"), "function");
  assert.equal(typeof sources[0].listeners.get("heartbeat"), "function");
});

test("realtime carries normalized ledger and verification events on the existing stream", () => {
  const sources = [];
  class FakeEventSource {
    constructor(url) {
      this.url = url;
      this.listeners = new Map();
      sources.push(this);
    }
    addEventListener(name, listener) {
      this.listeners.set(name, listener);
    }
    close() {}
  }

  const seen = [];
  const realtime = new FundingTraderRealtime({ EventSourceImpl: FakeEventSource });
  realtime.subscribe((message) => seen.push(message));
  realtime.connect();

  for (const kind of [
    "ledger.funding_payment.append",
    "ledger.position.verification_changed",
    "ledger.portfolio.updated",
    "verification.status",
  ]) {
    assert.equal(typeof sources[0].listeners.get(kind), "function", kind);
  }
  sources[0].listeners.get("ledger.portfolio.updated")({
    data: JSON.stringify({
      cursor: "ledger-cursor-1",
      principalId: "principal-default",
      mode: "test",
      contractVersion: PRIVATE_V1_CONTRACT_VERSION,
      data: { totalNetPnl: "12.5", verificationStatus: "verified" },
    }),
  });
  assert.equal(seen.at(-1).type, "data");
  assert.equal(seen.at(-1).event, "ledger.portfolio.updated");
  assert.equal(seen.at(-1).data.totalNetPnl, "12.5");
  assert.equal(realtime.getState().cursor, "ledger-cursor-1");
});

test("realtime strips browser product mode override query params", () => {
  const sources = [];
  class FakeEventSource {
    constructor(url) {
      this.url = url;
      this.listeners = new Map();
      sources.push(this);
    }
    addEventListener(name, listener) {
      this.listeners.set(name, listener);
    }
    close() {}
  }

  const realtime = new FundingTraderRealtime({
    url: "/api/v1/stream?mode=live&environment=demo&kind=system.status",
    EventSourceImpl: FakeEventSource,
  });
  realtime.connect({ cursor: "cursor-1", kinds: ["resnapshot.required"] });

  assert.equal(sources[0].url, "/api/v1/stream?kind=system.status&cursor=cursor-1&kind=resnapshot.required");
});

test("resnapshot required discards the stale cursor and immediately requests a full snapshot", () => {
  const sources = [];
  class FakeEventSource {
    constructor(url) {
      this.url = url;
      this.listeners = new Map();
      sources.push(this);
    }
    addEventListener(name, listener) {
      this.listeners.set(name, listener);
    }
    close() {
      this.closed = true;
    }
  }

  const seen = [];
  const realtime = new FundingTraderRealtime({ EventSourceImpl: FakeEventSource });
  realtime.subscribe((message) => seen.push(message));
  realtime.connect({ cursor: "stale-backend-cursor" });

  sources[0].listeners.get("resnapshot.required")({
    data: JSON.stringify({ cursor: "rejected-cursor", scope: ["executions"] }),
  });

  assert.equal(sources[0].closed, true);
  assert.equal(sources.length, 2);
  assert.equal(sources[1].url, "/api/v1/stream");
  assert.equal(realtime.getState().cursor, "");
  assert.equal(seen.some((message) => message.type === "resnapshot" && message.cursor === ""), true);
});

test("realtime surfaces resnapshot scope, malformed payloads, and bounded reconnect", () => {
  const sources = [];
  const timers = [];
  class FakeEventSource {
    constructor(url) {
      this.url = url;
      this.listeners = new Map();
      sources.push(this);
    }
    addEventListener(name, listener) {
      this.listeners.set(name, listener);
    }
    close() {
      this.closed = true;
    }
  }

  const seen = [];
  const realtime = new FundingTraderRealtime({
    EventSourceImpl: FakeEventSource,
    setTimeoutImpl: (fn, ms) => {
      timers.push({ fn, ms });
      return timers.length;
    },
    clearTimeoutImpl: () => {},
    backoff: { initialMs: 10, maxMs: 25, factor: 3 },
  });
  realtime.subscribe((message) => seen.push(message));
  realtime.connect();

  sources[0].listeners.get("resnapshot.required")({
    data: JSON.stringify({ cursor: "after-gap", scope: ["positions", "activities"] }),
  });
  assert.equal(seen.at(-1).type, "resnapshot");
  assert.deepEqual(seen.at(-1).scope, ["positions", "activities"]);
  assert.equal(realtime.getState().cursor, "");
  assert.equal(sources.length, 2);
  assert.equal(sources[1].url, "/api/v1/stream");

  sources[1].listeners.get("heartbeat")({ data: "{" });
  assert.equal(seen.at(-1).type, "malformed");

  sources[1].onerror({ type: "error" });
  assert.equal(timers[0].ms, 10);
  assert.equal(realtime.getState().status, "degraded");
  timers[0].fn();
  assert.equal(sources.length, 3);
  assert.equal(sources[2].url, "/api/v1/stream");
});
