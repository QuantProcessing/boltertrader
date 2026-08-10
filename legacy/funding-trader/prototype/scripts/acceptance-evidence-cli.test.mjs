import assert from "node:assert/strict";
import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";

import {
  ACCEPTANCE_EVIDENCE_FILENAME,
  REQUIRED_EVENT_KINDS,
  REQUIRED_LOG_FIELDS,
  REQUIRED_METRIC_NAMES,
} from "../src/acceptanceEvidence.js";

const CLI_PATH = new URL("./validate-browser-evidence.mjs", import.meta.url);

test("evidence CLI validates an on-disk sanitized bundle", async (t) => {
  const runDir = await createRunDirectory(t);
  const manifestPath = await writeBundle(runDir, "sanitized transcript\n");

  const result = spawnSync(process.execPath, [CLI_PATH.pathname, manifestPath], { encoding: "utf8" });

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /browser evidence valid/);
});

test("evidence CLI fails closed when an artifact is missing or contains a credential", async (t) => {
  const missingDir = await createRunDirectory(t);
  const missingManifest = await writeBundle(missingDir, "sanitized\n");
  await rm(path.join(missingDir, "transcripts", "browser.jsonl"));
  const missing = spawnSync(process.execPath, [CLI_PATH.pathname, missingManifest], { encoding: "utf8" });
  assert.notEqual(missing.status, 0);
  assert.match(missing.stderr, /unable to read artifact/);

  const leakedDir = await createRunDirectory(t);
  const leakedManifest = await writeBundle(leakedDir, "Authorization: Bearer leaked-browser-token\n");
  const leaked = spawnSync(process.execPath, [CLI_PATH.pathname, leakedManifest], { encoding: "utf8" });
  assert.notEqual(leaked.status, 0);
  assert.match(leaked.stderr, /secret material/);
});

async function createRunDirectory(t) {
  const directory = await mkdtemp(path.join(os.tmpdir(), "funding-trader-evidence-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  return directory;
}

async function writeBundle(runDir, transcript) {
  await mkdir(path.join(runDir, "screenshots"), { recursive: true });
  await mkdir(path.join(runDir, "transcripts"), { recursive: true });
  await writeFile(path.join(runDir, "screenshots", "readiness-blocked.png"), "sanitized screenshot");
  await writeFile(path.join(runDir, "transcripts", "browser.jsonl"), transcript);

  const manifest = {
    schemaVersion: 1,
    runId: "acceptance-cli-test",
    createdAt: "2026-08-01T12:00:00.000Z",
    mode: "test",
    suiteStatus: "BLOCKED",
    browserStatus: "PASS",
    r1Status: "NOT_ATTEMPTED",
    r2Status: "NOT_ATTEMPTED",
    blockReason: "insufficient-ready-perp-venues",
    privateWriteAttempts: 0,
    livePrivateOrderCount: 0,
    readiness: { openReady: false, closeReady: true, recoveryReady: true, reason: "insufficient-ready-perp-venues" },
    ledger: { verificationStatus: "incomplete", watermarkVerified: false, completeThrough: null, watermarkRevision: 0, residualNotional: null },
    observability: {
      metrics: [
        { name: "readiness.open_ready", observed: false, value: false },
        { name: "readiness.close_ready", observed: false, value: true },
        { name: "readiness.recovery_ready", observed: false, value: true },
        { name: "ledger.watermark.complete_through", observed: false, value: null },
        { name: "ledger.verification.status", observed: false, value: "incomplete" },
      ],
      logFields: REQUIRED_LOG_FIELDS.map((name) => ({ name, observed: false })),
      events: REQUIRED_EVENT_KINDS.map((name) => ({ name, observed: false })),
    },
    artifacts: [
      { kind: "screenshot", path: "screenshots/readiness-blocked.png", redacted: true },
      { kind: "transcript", path: "transcripts/browser.jsonl", redacted: true },
    ],
    lifecycles: { r1: null, r2: null },
  };
  const manifestPath = path.join(runDir, ACCEPTANCE_EVIDENCE_FILENAME);
  await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
  return manifestPath;
}
