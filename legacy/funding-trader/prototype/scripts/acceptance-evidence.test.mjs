import assert from "node:assert/strict";
import test from "node:test";

import {
  ACCEPTANCE_EVIDENCE_FILENAME,
  REQUIRED_EVENT_KINDS,
  REQUIRED_LOG_FIELDS,
  REQUIRED_METRIC_NAMES,
  findEvidenceSecrets,
  validateAcceptanceEvidence,
} from "../src/acceptanceEvidence.js";

test("blocked browser evidence is truthful, complete, and write-free", () => {
  const manifest = validBlockedManifest();
  const result = validateAcceptanceEvidence(manifest, {
    filename: ACCEPTANCE_EVIDENCE_FILENAME,
    artifactContents: new Map([
      ["screenshots/readiness-blocked.png", Buffer.from("sanitized screenshot")],
      ["transcripts/browser.jsonl", Buffer.from('{"step":"readiness","status":"blocked"}\n')],
    ]),
  });

  assert.deepEqual(result, { ok: true, errors: [] });
});

test("manifest inventories every frozen metric, log field, and event name", () => {
  const manifest = validBlockedManifest();

  assert.deepEqual(manifest.observability.metrics.map(({ name }) => name), REQUIRED_METRIC_NAMES);
  assert.deepEqual(manifest.observability.logFields.map(({ name }) => name), REQUIRED_LOG_FIELDS);
  assert.deepEqual(manifest.observability.events.map(({ name }) => name), REQUIRED_EVENT_KINDS);

  manifest.observability.events.pop();
  const result = validateAcceptanceEvidence(manifest, {
    filename: ACCEPTANCE_EVIDENCE_FILENAME,
    artifactContents: safeArtifactContents(),
  });
  assert.equal(result.ok, false);
  assert.match(result.errors.join("\n"), /resnapshot\.required/);
});

test("observability metric values must match the canonical readiness and ledger fields", () => {
  const manifest = validBlockedManifest();
  manifest.observability.metrics.find(({ name }) => name === "readiness.close_ready").value = false;
  const result = validateAcceptanceEvidence(manifest, {
    filename: ACCEPTANCE_EVIDENCE_FILENAME,
    artifactContents: safeArtifactContents(),
  });
  assert.equal(result.ok, false);
  assert.match(result.errors.join("\n"), /readiness\.close_ready value must match/);
});

test("blocked inventory cannot disguise attempted writes or R1/R2 success", () => {
  for (const mutate of [
    (manifest) => { manifest.privateWriteAttempts = 1; },
    (manifest) => { manifest.r1Status = "PASS"; },
    (manifest) => { manifest.r2Status = "FAIL"; },
  ]) {
    const manifest = validBlockedManifest();
    mutate(manifest);
    const result = validateAcceptanceEvidence(manifest, {
      filename: ACCEPTANCE_EVIDENCE_FILENAME,
      artifactContents: safeArtifactContents(),
    });
    assert.equal(result.ok, false, JSON.stringify(manifest));
    assert.match(result.errors.join("\n"), /BLOCKED/);
  }
});

test("suite PASS requires browser, R1, R2, verified-through ledger, flat residual, and observed evidence", () => {
  const manifest = validPassingManifest();
  assert.equal(validateAcceptanceEvidence(manifest, {
    filename: ACCEPTANCE_EVIDENCE_FILENAME,
    artifactContents: safeArtifactContents(),
  }).ok, true);

  const corruptions = [
    (candidate) => { candidate.browserStatus = "FAIL"; },
    (candidate) => { candidate.r1Status = "NOT_ATTEMPTED"; },
    (candidate) => { candidate.r2Status = "NOT_ATTEMPTED"; },
    (candidate) => { candidate.ledger.verificationStatus = "provisional"; },
    (candidate) => { candidate.ledger.watermarkVerified = false; },
    (candidate) => { candidate.ledger.completeThrough = null; },
    (candidate) => { candidate.ledger.residualNotional = "0.0001"; },
    (candidate) => { candidate.observability.events[0].observed = false; },
  ];
  for (const corrupt of corruptions) {
    const candidate = structuredClone(manifest);
    corrupt(candidate);
    const result = validateAcceptanceEvidence(candidate, {
      filename: ACCEPTANCE_EVIDENCE_FILENAME,
      artifactContents: safeArtifactContents(),
    });
    assert.equal(result.ok, false, JSON.stringify(candidate));
    assert.match(result.errors.join("\n"), /PASS/);
  }
});

test("R2 cannot pass before R1 and Live private order count is always zero", () => {
  const r2First = validPassingManifest();
  r2First.r1Status = "FAIL";
  assert.match(validateAcceptanceEvidence(r2First, {
    filename: ACCEPTANCE_EVIDENCE_FILENAME,
    artifactContents: safeArtifactContents(),
  }).errors.join("\n"), /R2 PASS requires R1 PASS/);

  const liveWrite = validBlockedManifest();
  liveWrite.livePrivateOrderCount = 1;
  assert.match(validateAcceptanceEvidence(liveWrite, {
    filename: ACCEPTANCE_EVIDENCE_FILENAME,
    artifactContents: safeArtifactContents(),
  }).errors.join("\n"), /Live private order count must be zero/);

  const liveMode = validBlockedManifest();
  liveMode.mode = "live";
  assert.match(validateAcceptanceEvidence(liveMode, {
    filename: ACCEPTANCE_EVIDENCE_FILENAME,
    artifactContents: safeArtifactContents(),
  }).errors.join("\n"), /mode must be test/);
});

test("manifest filename and every artifact path are constrained to the run directory", () => {
  for (const { filename, path } of [
    { filename: "evidence.json", path: "screenshots/readiness-blocked.png" },
    { filename: ACCEPTANCE_EVIDENCE_FILENAME, path: "../session.txt" },
    { filename: ACCEPTANCE_EVIDENCE_FILENAME, path: "/tmp/session.txt" },
    { filename: ACCEPTANCE_EVIDENCE_FILENAME, path: "screenshots\\session.txt" },
    { filename: ACCEPTANCE_EVIDENCE_FILENAME, path: "https://example.invalid/evidence.json" },
  ]) {
    const manifest = validBlockedManifest();
    manifest.artifacts[0].path = path;
    const result = validateAcceptanceEvidence(manifest, {
      filename,
      artifactContents: safeArtifactContents(path),
    });
    assert.equal(result.ok, false);
  }
});

test("raw browser network artifacts are forbidden even when marked redacted", () => {
  for (const kind of ["har", "trace"]) {
    const manifest = validBlockedManifest();
    manifest.artifacts[0] = {
      kind,
      path: `network/browser.${kind}`,
      redacted: true,
      containsNetworkBodies: false,
    };
    const result = validateAcceptanceEvidence(manifest, {
      filename: ACCEPTANCE_EVIDENCE_FILENAME,
      artifactContents: safeArtifactContents(`network/browser.${kind}`),
    });
    assert.equal(result.ok, false);
    assert.match(result.errors.join("\n"), /HAR and trace artifacts are forbidden/);
  }
});

test("declared artifact digests must match supplied evidence bytes", () => {
  const manifest = validBlockedManifest();
  manifest.artifacts[0].sha256 = "0".repeat(64);
  const result = validateAcceptanceEvidence(manifest, {
    filename: ACCEPTANCE_EVIDENCE_FILENAME,
    artifactContents: safeArtifactContents(),
  });
  assert.equal(result.ok, false);
  assert.match(result.errors.join("\n"), /sha256 does not match/);
});

test("R2 PASS requires attributable actual funding on both legs and all eight flat proofs", () => {
  const manifest = validPassingManifest();
  assert.equal(validateAcceptanceEvidence(manifest, {
    filename: ACCEPTANCE_EVIDENCE_FILENAME,
    artifactContents: safeArtifactContents(),
  }).ok, true);

  const corruptions = [
    (candidate) => { candidate.lifecycles.r2.fundingPaymentsByLeg.pop(); },
    (candidate) => { candidate.lifecycles.r2.fundingPaymentsByLeg[0].fundingRate = "0.001"; },
    (candidate) => { candidate.lifecycles.r2.fundingPaymentsByLeg[0].amount = null; },
    (candidate) => { candidate.lifecycles.r2.flatProof.legQuantities[0].quantity = "0.01"; },
    (candidate) => { candidate.lifecycles.r2.flatProof.scopedOpenOrders = 1; },
    (candidate) => { candidate.lifecycles.r2.flatProof.allRunOrdersTerminal = false; },
    (candidate) => { candidate.lifecycles.r2.flatProof.pendingCompensation = 1; },
    (candidate) => { candidate.lifecycles.r2.flatProof.verificationStatus = "provisional"; },
    (candidate) => { candidate.lifecycles.r2.flatProof.watermarkVerified = false; },
    (candidate) => { candidate.lifecycles.r2.flatProof.closedUnrealizedPnl = "1"; },
    (candidate) => { candidate.lifecycles.r2.flatProof.residualNotional = "1"; },
  ];
  for (const corrupt of corruptions) {
    const candidate = structuredClone(manifest);
    corrupt(candidate);
    const result = validateAcceptanceEvidence(candidate, {
      filename: ACCEPTANCE_EVIDENCE_FILENAME,
      artifactContents: safeArtifactContents(),
    });
    assert.equal(result.ok, false, JSON.stringify(candidate));
  }
});

test("recursive manifest and artifact scans reject credentials and private material", () => {
  const secretCases = [
    { authorization: "Bearer abcdefghijklmnop" },
    { nested: { csrfToken: "csrf-value-that-must-not-leak" } },
    { password: "correct horse battery staple" },
    { note: "Cookie: ft_session=raw-session-value" },
    { payload: "-----BEGIN PRIVATE KEY-----" },
  ];
  for (const secret of secretCases) {
    assert.ok(findEvidenceSecrets(secret).length > 0, JSON.stringify(secret));
  }
  assert.deepEqual(findEvidenceSecrets({ authorization: "[REDACTED]", sessionExpired: true }), []);

  const manifest = validBlockedManifest();
  manifest.operator = { apiKey: "plain-text-key" };
  assert.equal(validateAcceptanceEvidence(manifest, {
    filename: ACCEPTANCE_EVIDENCE_FILENAME,
    artifactContents: safeArtifactContents(),
  }).ok, false);

  const artifactLeak = validateAcceptanceEvidence(validBlockedManifest(), {
    filename: ACCEPTANCE_EVIDENCE_FILENAME,
    artifactContents: new Map([
      ["screenshots/readiness-blocked.png", Buffer.from("sanitized")],
      ["transcripts/browser.jsonl", Buffer.from("Authorization: Bearer leaked-token-value")],
    ]),
  });
  assert.equal(artifactLeak.ok, false);
  assert.match(artifactLeak.errors.join("\n"), /transcripts\/browser\.jsonl/);
});

function validBlockedManifest() {
  return {
    schemaVersion: 1,
    runId: "acceptance-20260801T120000Z",
    createdAt: "2026-08-01T12:00:00.000Z",
    mode: "test",
    suiteStatus: "BLOCKED",
    browserStatus: "PASS",
    r1Status: "NOT_ATTEMPTED",
    r2Status: "NOT_ATTEMPTED",
    blockReason: "insufficient-ready-perp-venues",
    privateWriteAttempts: 0,
    livePrivateOrderCount: 0,
    readiness: {
      openReady: false,
      closeReady: true,
      recoveryReady: true,
      reason: "insufficient-ready-perp-venues",
    },
    ledger: {
      verificationStatus: "incomplete",
      watermarkVerified: false,
      completeThrough: null,
      watermarkRevision: 7,
      residualNotional: null,
    },
    observability: {
      metrics: [
        { name: "readiness.open_ready", observed: true, value: false },
        { name: "readiness.close_ready", observed: true, value: true },
        { name: "readiness.recovery_ready", observed: true, value: true },
        { name: "ledger.watermark.complete_through", observed: false, value: null },
        { name: "ledger.verification.status", observed: false, value: "incomplete" },
      ],
      logFields: REQUIRED_LOG_FIELDS.map((name) => ({ name, observed: name === "request_id" || name === "run_id" })),
      events: REQUIRED_EVENT_KINDS.map((name) => ({ name, observed: name === "resnapshot.required" })),
    },
    artifacts: [
      { kind: "screenshot", path: "screenshots/readiness-blocked.png", redacted: true },
      { kind: "transcript", path: "transcripts/browser.jsonl", redacted: true },
    ],
    lifecycles: {
      r1: null,
      r2: null,
    },
  };
}

function validPassingManifest() {
  const manifest = validBlockedManifest();
  manifest.suiteStatus = "PASS";
  manifest.r1Status = "PASS";
  manifest.r2Status = "PASS";
  manifest.blockReason = null;
  manifest.privateWriteAttempts = 4;
  manifest.readiness.openReady = true;
  manifest.readiness.reason = "ready";
  manifest.ledger.verificationStatus = "verified";
  manifest.ledger.watermarkVerified = true;
  manifest.ledger.completeThrough = "2026-08-01T11:59:59.000Z";
  manifest.ledger.residualNotional = "0.00000000";
  manifest.observability.metrics.find(({ name }) => name === "readiness.open_ready").value = true;
  manifest.observability.metrics.find(({ name }) => name === "ledger.watermark.complete_through").value = manifest.ledger.completeThrough;
  manifest.observability.metrics.find(({ name }) => name === "ledger.verification.status").value = "verified";
  manifest.lifecycles.r1 = passingLifecycle("r1-lineage", false);
  manifest.lifecycles.r2 = passingLifecycle("r2-lineage", true);
  for (const collection of Object.values(manifest.observability)) {
    for (const entry of collection) entry.observed = true;
  }
  return manifest;
}

function passingLifecycle(lineageId, withFunding) {
  return {
    lineageId,
    fundingPaymentsByLeg: withFunding ? [
      {
        accountId: "account-long",
        instrumentId: "instrument-long",
        lineageId,
        amount: "0.12500000",
        settlementAsset: "USDT",
        effectiveAt: "2026-08-01T11:00:00.000Z",
      },
      {
        accountId: "account-short",
        instrumentId: "instrument-short",
        lineageId,
        amount: "-0.12400000",
        settlementAsset: "USDT",
        effectiveAt: "2026-08-01T11:00:00.000Z",
      },
    ] : [],
    flatProof: {
      legQuantities: [
        { accountId: "account-long", instrumentId: "instrument-long", quantity: "0" },
        { accountId: "account-short", instrumentId: "instrument-short", quantity: "0.00000000" },
      ],
      scopedOpenOrders: 0,
      allRunOrdersTerminal: true,
      pendingCompensation: 0,
      verificationStatus: "verified",
      watermarkVerified: true,
      completeThrough: "2026-08-01T11:59:59.000Z",
      closedUnrealizedPnl: "0.00000000",
      residualNotional: "0",
    },
  };
}

function safeArtifactContents(firstPath = "screenshots/readiness-blocked.png") {
  return new Map([
    [firstPath, Buffer.from("sanitized screenshot")],
    ["transcripts/browser.jsonl", Buffer.from('{"step":"readiness","status":"blocked"}\n')],
  ]);
}
