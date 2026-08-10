import { createHash } from "node:crypto";
import path from "node:path";

export const ACCEPTANCE_EVIDENCE_FILENAME = "browser-trading-acceptance-evidence.json";

export const REQUIRED_METRIC_NAMES = Object.freeze([
  "readiness.open_ready",
  "readiness.close_ready",
  "readiness.recovery_ready",
  "ledger.watermark.complete_through",
  "ledger.verification.status",
]);

export const REQUIRED_LOG_FIELDS = Object.freeze([
  "request_id",
  "run_id",
  "lineage_id",
  "watermark_revision",
  "complete_through",
  "verification_status",
  "stream_cursor",
]);

export const REQUIRED_EVENT_KINDS = Object.freeze([
  "ledger.funding_payment.append",
  "ledger.position.verification_changed",
  "ledger.portfolio.updated",
  "verification.status",
  "resnapshot.required",
]);

const SUITE_STATUSES = new Set(["PASS", "FAIL", "BLOCKED"]);
const BROWSER_STATUSES = new Set(["PASS", "FAIL"]);
const RUN_STATUSES = new Set(["PASS", "FAIL", "NOT_ATTEMPTED"]);
const VERIFICATION_STATUSES = new Set([
  "unsupported",
  "temporarily_unavailable",
  "incomplete",
  "provisional",
  "verified",
  "revised",
  "blocked",
]);
const RAW_NETWORK_ARTIFACT_KINDS = new Set(["har", "trace"]);
const REDACTED_VALUES = new Set(["[REDACTED]", "<redacted>", "REDACTED"]);
const SENSITIVE_KEYS = new Set([
  "password",
  "passwd",
  "passphrase",
  "secret",
  "token",
  "accesstoken",
  "refreshtoken",
  "authorization",
  "apikey",
  "apisecret",
  "privatekey",
  "cookie",
  "setcookie",
  "csrf",
  "csrftoken",
  "session",
  "sessionid",
  "sessioncookie",
  "ftsession",
]);
const SECRET_VALUE_PATTERNS = [
  { label: "Bearer authorization", pattern: /\bBearer\s+[A-Za-z0-9._~+/=-]{8,}/i },
  { label: "funding-trader session cookie", pattern: /\bft_session\s*=\s*[^\s;,]+/i },
  { label: "cookie header", pattern: /\b(?:set-)?cookie\s*:\s*(?!\[REDACTED\])[^\r\n]+/i },
  { label: "credential assignment", pattern: /\b(?:password|passwd|secret|api[_-]?(?:key|secret)|private[_-]?key|csrf(?:[_-]?token)?|authorization)\s*[:=]\s*(?!\[REDACTED\]|<redacted>|REDACTED)[^\s,;}]+/i },
  { label: "private key material", pattern: /-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----/i },
  { label: "OpenAI-style secret", pattern: /\bsk-[A-Za-z0-9_-]{12,}/ },
];

export function validateAcceptanceEvidence(manifest, {
  filename = ACCEPTANCE_EVIDENCE_FILENAME,
  artifactContents,
} = {}) {
  const errors = [];
  const add = (message) => errors.push(message);

  if (path.basename(String(filename)) !== ACCEPTANCE_EVIDENCE_FILENAME) {
    add(`manifest filename must be ${ACCEPTANCE_EVIDENCE_FILENAME}`);
  }
  if (!isPlainObject(manifest)) {
    return { ok: false, errors: [...errors, "manifest must be an object"] };
  }

  if (manifest.schemaVersion !== 1) add("schemaVersion must be 1");
  if (!isSafeRunId(manifest.runId)) add("runId must be a non-empty filesystem-safe identifier");
  if (!isRFC3339(manifest.createdAt)) add("createdAt must be an RFC3339 timestamp");
  if (manifest.mode !== "test") add("acceptance mode must be test");
  if (!SUITE_STATUSES.has(manifest.suiteStatus)) add("suiteStatus must be PASS, FAIL, or BLOCKED");
  if (!BROWSER_STATUSES.has(manifest.browserStatus)) add("browserStatus must be PASS or FAIL");
  if (!RUN_STATUSES.has(manifest.r1Status)) add("r1Status must be PASS, FAIL, or NOT_ATTEMPTED");
  if (!RUN_STATUSES.has(manifest.r2Status)) add("r2Status must be PASS, FAIL, or NOT_ATTEMPTED");
  if (!isNonNegativeInteger(manifest.privateWriteAttempts)) add("privateWriteAttempts must be a non-negative integer");
  if (manifest.livePrivateOrderCount !== 0) add("Live private order count must be zero");

  validateReadiness(manifest.readiness, add);
  validateLedger(manifest.ledger, add);
  validateObservability(manifest.observability, manifest.suiteStatus, add);
  validateMetricConsistency(manifest.observability?.metrics, manifest.readiness, manifest.ledger, add);
  validateArtifacts(manifest.artifacts, artifactContents, add);

  if (manifest.suiteStatus === "BLOCKED") {
    if (manifest.r1Status !== "NOT_ATTEMPTED" || manifest.r2Status !== "NOT_ATTEMPTED") {
      add("BLOCKED suite requires R1 and R2 to remain NOT_ATTEMPTED");
    }
    if (manifest.privateWriteAttempts !== 0) add("BLOCKED suite requires zero private write attempts");
    if (typeof manifest.blockReason !== "string" || manifest.blockReason.trim() === "") {
      add("BLOCKED suite requires a non-empty blockReason");
    }
  }

  if (manifest.r2Status === "PASS" && manifest.r1Status !== "PASS") {
    add("R2 PASS requires R1 PASS");
  }

  if (manifest.suiteStatus === "PASS") {
    if (manifest.browserStatus !== "PASS") add("suite PASS requires browser PASS");
    if (manifest.r1Status !== "PASS") add("suite PASS requires R1 PASS");
    if (manifest.r2Status !== "PASS") add("suite PASS requires R2 PASS");
    if (manifest.ledger?.verificationStatus !== "verified" || manifest.ledger?.watermarkVerified !== true) {
      add("suite PASS requires verified status and a verified-through watermark");
    }
    if (!isRFC3339(manifest.ledger?.completeThrough)) {
      add("suite PASS requires ledger completeThrough");
    }
    if (!isExactZero(manifest.ledger?.residualNotional)) {
      add("suite PASS requires zero ledger residualNotional");
    }
    if (!allObservabilityObserved(manifest.observability)) {
      add("suite PASS requires every frozen metric, log field, and event to be observed");
    }
  }

  validateLifecycle("R1", manifest.r1Status, manifest.lifecycles?.r1, false, add);
  validateLifecycle("R2", manifest.r2Status, manifest.lifecycles?.r2, true, add);

  for (const finding of findEvidenceSecrets(manifest)) {
    add(`secret material at ${finding.path}: ${finding.reason}`);
  }

  return { ok: errors.length === 0, errors };
}

export function assertAcceptanceEvidence(manifest, options) {
  const result = validateAcceptanceEvidence(manifest, options);
  if (!result.ok) {
    throw new AggregateError(result.errors.map((message) => new Error(message)), "invalid browser acceptance evidence");
  }
  return manifest;
}

export function findEvidenceSecrets(value, currentPath = "$") {
  const findings = [];
  visit(value, currentPath, findings, new WeakSet());
  return findings;
}

function validateReadiness(readiness, add) {
  if (!isPlainObject(readiness)) {
    add("readiness must be an object");
    return;
  }
  for (const key of ["openReady", "closeReady", "recoveryReady"]) {
    if (typeof readiness[key] !== "boolean") add(`readiness.${key} must be boolean`);
  }
  if (typeof readiness.reason !== "string" || readiness.reason.trim() === "") {
    add("readiness.reason must be non-empty");
  }
}

function validateLedger(ledger, add) {
  if (!isPlainObject(ledger)) {
    add("ledger must be an object");
    return;
  }
  if (!VERIFICATION_STATUSES.has(ledger.verificationStatus)) add("ledger.verificationStatus must use a backend verification status");
  if (typeof ledger.watermarkVerified !== "boolean") add("ledger.watermarkVerified must be boolean");
  if (ledger.completeThrough !== null && !isRFC3339(ledger.completeThrough)) {
    add("ledger.completeThrough must be null or RFC3339");
  }
  if (!isNonNegativeInteger(ledger.watermarkRevision)) {
    add("ledger.watermarkRevision must be a non-negative integer");
  }
  if (ledger.residualNotional !== null && typeof ledger.residualNotional !== "string") {
    add("ledger.residualNotional must be a decimal string or null");
  }
}

function validateObservability(observability, suiteStatus, add) {
  if (!isPlainObject(observability)) {
    add("observability must be an object");
    return;
  }
  validateNamedEvidence("metric", observability.metrics, REQUIRED_METRIC_NAMES, add);
  validateNamedEvidence("log field", observability.logFields, REQUIRED_LOG_FIELDS, add);
  validateNamedEvidence("event", observability.events, REQUIRED_EVENT_KINDS, add);
  if (suiteStatus === "PASS" && !allObservabilityObserved(observability)) {
    add("suite PASS requires all observability evidence to be observed");
  }
}

function validateNamedEvidence(label, entries, requiredNames, add) {
  if (!Array.isArray(entries)) {
    add(`observability ${label}s must be an array`);
    return;
  }
  const counts = new Map();
  for (const entry of entries) {
    if (!isPlainObject(entry) || typeof entry.name !== "string") {
      add(`every observability ${label} must have a name`);
      continue;
    }
    counts.set(entry.name, (counts.get(entry.name) ?? 0) + 1);
    if (typeof entry.observed !== "boolean") add(`${label} ${entry.name} observed must be boolean`);
  }
  for (const name of requiredNames) {
    if (counts.get(name) !== 1) add(`required ${label} ${name} must appear exactly once`);
  }
  for (const name of counts.keys()) {
    if (!requiredNames.includes(name)) add(`unknown ${label} ${name}`);
  }
}

function validateMetricConsistency(metrics, readiness, ledger, add) {
  if (!Array.isArray(metrics) || !isPlainObject(readiness) || !isPlainObject(ledger)) return;
  const expected = new Map([
    ["readiness.open_ready", readiness.openReady],
    ["readiness.close_ready", readiness.closeReady],
    ["readiness.recovery_ready", readiness.recoveryReady],
    ["ledger.watermark.complete_through", ledger.completeThrough],
    ["ledger.verification.status", ledger.verificationStatus],
  ]);
  for (const metric of metrics) {
    if (expected.has(metric?.name) && metric.value !== expected.get(metric.name)) {
      add(`metric ${metric.name} value must match the canonical manifest field`);
    }
  }
}

function validateArtifacts(artifacts, artifactContents, add) {
  if (!Array.isArray(artifacts) || artifacts.length === 0) {
    add("artifacts must contain at least one evidence item");
    return;
  }
  const paths = new Set();
  for (const [index, artifact] of artifacts.entries()) {
    const prefix = `artifacts[${index}]`;
    if (!isPlainObject(artifact)) {
      add(`${prefix} must be an object`);
      continue;
    }
    if (typeof artifact.kind !== "string" || artifact.kind.trim() === "") add(`${prefix}.kind must be non-empty`);
    if (RAW_NETWORK_ARTIFACT_KINDS.has(String(artifact.kind).toLowerCase())) {
      add(`${prefix}: raw HAR and trace artifacts are forbidden`);
    }
    if (!isSafeArtifactPath(artifact.path)) add(`${prefix}.path must stay inside the evidence run directory`);
    if (artifact.redacted !== true) add(`${prefix}.redacted must be true`);
    if (artifact.sha256 !== undefined && !/^[a-f0-9]{64}$/.test(String(artifact.sha256))) {
      add(`${prefix}.sha256 must be a lowercase SHA-256 digest`);
    }
    if (paths.has(artifact.path)) add(`${prefix}.path must be unique`);
    paths.add(artifact.path);

    if (artifactContents instanceof Map) {
      if (!artifactContents.has(artifact.path)) {
        add(`${prefix}.path has no supplied artifact content`);
      } else {
        const content = artifactContents.get(artifact.path);
        if (artifact.sha256 !== undefined) {
          const digest = createHash("sha256").update(toEvidenceBuffer(content)).digest("hex");
          if (digest !== artifact.sha256) add(`${prefix}.sha256 does not match artifact content`);
        }
        const findings = findEvidenceSecrets(toEvidenceText(content), `$artifact:${artifact.path}`);
        for (const finding of findings) add(`secret material at ${finding.path}: ${finding.reason}`);
      }
    }
  }
}

function validateLifecycle(label, status, lifecycle, requireFunding, add) {
  if (status !== "PASS") return;
  if (!isPlainObject(lifecycle)) {
    add(`${label} PASS requires lifecycle evidence`);
    return;
  }
  if (typeof lifecycle.lineageId !== "string" || lifecycle.lineageId.trim() === "") {
    add(`${label} PASS requires lineageId`);
  }
  if (requireFunding) validateR2FundingPayments(lifecycle, add);
  validateFlatProof(label, lifecycle.flatProof, add);
}

function validateR2FundingPayments(lifecycle, add) {
  if (!Array.isArray(lifecycle.fundingPaymentsByLeg) || lifecycle.fundingPaymentsByLeg.length < 2) {
    add("R2 PASS requires attributable actual funding payments for both legs");
    return;
  }
  const legKeys = new Set();
  for (const [index, payment] of lifecycle.fundingPaymentsByLeg.entries()) {
    const prefix = `R2 fundingPaymentsByLeg[${index}]`;
    if (!isPlainObject(payment)) {
      add(`${prefix} must be an object`);
      continue;
    }
    if ("rate" in payment || "fundingRate" in payment) add(`${prefix} must contain payment facts, not funding rates`);
    for (const field of ["accountId", "instrumentId", "lineageId", "amount", "settlementAsset", "effectiveAt"]) {
      if (typeof payment[field] !== "string" || payment[field].trim() === "") add(`${prefix}.${field} must be non-empty`);
    }
    if (payment.lineageId !== lifecycle.lineageId) add(`${prefix}.lineageId must match the lifecycle`);
    if (!isRFC3339(payment.effectiveAt)) add(`${prefix}.effectiveAt must be RFC3339`);
    legKeys.add(`${payment.accountId}\u0000${payment.instrumentId}`);
  }
  if (legKeys.size < 2) add("R2 PASS requires actual funding attributable to two distinct legs");
}

function validateFlatProof(label, flatProof, add) {
  if (!isPlainObject(flatProof)) {
    add(`${label} PASS requires flatProof`);
    return;
  }
  if (!Array.isArray(flatProof.legQuantities) || flatProof.legQuantities.length < 2) {
    add(`${label} PASS requires two authoritative leg quantities`);
  } else {
    for (const [index, leg] of flatProof.legQuantities.entries()) {
      if (!isPlainObject(leg) || !isExactZero(leg.quantity)) {
        add(`${label} flatProof legQuantities[${index}] must be exact zero`);
      }
    }
  }
  if (flatProof.scopedOpenOrders !== 0) add(`${label} PASS requires zero scoped open orders`);
  if (flatProof.allRunOrdersTerminal !== true) add(`${label} PASS requires all run orders terminal`);
  if (flatProof.pendingCompensation !== 0) add(`${label} PASS requires zero pending compensation`);
  if (flatProof.verificationStatus !== "verified" || flatProof.watermarkVerified !== true || !isRFC3339(flatProof.completeThrough)) {
    add(`${label} PASS requires a verified-through watermark`);
  }
  if (!isExactZero(flatProof.closedUnrealizedPnl)) add(`${label} PASS requires zero closed UPnL`);
  if (!isExactZero(flatProof.residualNotional)) add(`${label} PASS requires zero residual notional`);
}

function allObservabilityObserved(observability) {
  if (!isPlainObject(observability)) return false;
  return [observability.metrics, observability.logFields, observability.events]
    .every((entries) => Array.isArray(entries) && entries.length > 0 && entries.every((entry) => entry?.observed === true));
}

function isSafeArtifactPath(value) {
  if (typeof value !== "string" || value.trim() === "") return false;
  if (value.includes("\\") || value.includes("://") || path.posix.isAbsolute(value)) return false;
  const normalized = path.posix.normalize(value);
  return normalized === value && normalized !== ".." && !normalized.startsWith("../") && !normalized.includes("/../");
}

function visit(value, currentPath, findings, seen) {
  if (typeof value === "string") {
    scanString(value, currentPath, findings);
    return;
  }
  if (value === null || typeof value !== "object") return;
  if (seen.has(value)) return;
  seen.add(value);

  if (Array.isArray(value)) {
    value.forEach((item, index) => visit(item, `${currentPath}[${index}]`, findings, seen));
    return;
  }
  for (const [key, child] of Object.entries(value)) {
    const childPath = `${currentPath}.${key}`;
    if (isSensitiveKey(key) && !isSafelyRedacted(child)) {
      findings.push({ path: childPath, reason: `unredacted sensitive field ${key}` });
    }
    visit(child, childPath, findings, seen);
  }
}

function scanString(value, currentPath, findings) {
  if (REDACTED_VALUES.has(value)) return;
  for (const { label, pattern } of SECRET_VALUE_PATTERNS) {
    if (pattern.test(value)) findings.push({ path: currentPath, reason: label });
  }
}

function isSensitiveKey(key) {
  const normalized = String(key).replace(/[^a-z0-9]/gi, "").toLowerCase();
  return SENSITIVE_KEYS.has(normalized);
}

function isSafelyRedacted(value) {
  return value === null || value === false || value === "" || (typeof value === "string" && REDACTED_VALUES.has(value));
}

function toEvidenceText(value) {
  if (Buffer.isBuffer(value)) return value.toString("utf8");
  if (typeof value === "string") return value;
  return JSON.stringify(value);
}

function toEvidenceBuffer(value) {
  if (Buffer.isBuffer(value)) return value;
  if (typeof value === "string") return Buffer.from(value);
  return Buffer.from(JSON.stringify(value));
}

function isPlainObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function isRFC3339(value) {
  if (typeof value !== "string" || value.trim() === "") return false;
  const timestamp = Date.parse(value);
  return Number.isFinite(timestamp) && /(?:Z|[+-]\d{2}:\d{2})$/.test(value);
}

function isNonNegativeInteger(value) {
  return Number.isInteger(value) && value >= 0;
}

function isSafeRunId(value) {
  return typeof value === "string" && /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(value);
}

function isExactZero(value) {
  return typeof value === "string" && /^[+-]?0+(?:\.0+)?$/.test(value);
}
