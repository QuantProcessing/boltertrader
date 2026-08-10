#!/usr/bin/env node

import { lstat, readFile, realpath } from "node:fs/promises";
import path from "node:path";
import process from "node:process";

import {
  ACCEPTANCE_EVIDENCE_FILENAME,
  findEvidenceSecrets,
  validateAcceptanceEvidence,
} from "../src/acceptanceEvidence.js";

const MAX_MANIFEST_BYTES = 2 * 1024 * 1024;
const MAX_ARTIFACT_BYTES = 50 * 1024 * 1024;

const manifestArgument = process.argv[2];
if (!manifestArgument) {
  fail(`usage: node scripts/validate-browser-evidence.mjs <run-directory>/${ACCEPTANCE_EVIDENCE_FILENAME}`);
} else {
  await main(path.resolve(manifestArgument));
}

async function main(manifestPath) {
  if (path.basename(manifestPath) !== ACCEPTANCE_EVIDENCE_FILENAME) {
    fail(`manifest filename must be ${ACCEPTANCE_EVIDENCE_FILENAME}`);
    return;
  }

  let manifestBytes;
  try {
    manifestBytes = await readBoundedFile(manifestPath, MAX_MANIFEST_BYTES, "manifest");
  } catch (error) {
    fail(error.message);
    return;
  }

  let manifest;
  try {
    manifest = JSON.parse(manifestBytes.toString("utf8"));
  } catch {
    fail("manifest must contain valid JSON");
    return;
  }

  const structural = validateAcceptanceEvidence(manifest, { filename: manifestPath });
  const rawManifestLeaks = findEvidenceSecrets(manifestBytes.toString("utf8"), "$manifest-text");
  if (!structural.ok || rawManifestLeaks.length > 0) {
    reportErrors([
      ...structural.errors,
      ...rawManifestLeaks.map((finding) => `secret material at ${finding.path}: ${finding.reason}`),
    ]);
    return;
  }

  const runDirectory = path.dirname(manifestPath);
  let canonicalRunDirectory;
  try {
    canonicalRunDirectory = await realpath(runDirectory);
  } catch {
    fail("unable to resolve evidence run directory");
    return;
  }

  const artifactContents = new Map();
  for (const artifact of manifest.artifacts) {
    const artifactPath = path.resolve(runDirectory, artifact.path);
    try {
      const canonicalArtifactPath = await realpath(artifactPath);
      if (!isInside(canonicalRunDirectory, canonicalArtifactPath)) {
        throw new Error("resolved path leaves the evidence run directory");
      }
      artifactContents.set(artifact.path, await readBoundedFile(canonicalArtifactPath, MAX_ARTIFACT_BYTES, "artifact"));
    } catch (error) {
      fail(`unable to read artifact ${artifact.path}: ${error.message}`);
      return;
    }
  }

  const result = validateAcceptanceEvidence(manifest, {
    filename: manifestPath,
    artifactContents,
  });
  if (!result.ok) {
    reportErrors(result.errors);
    return;
  }

  process.stdout.write(`browser evidence valid: ${manifest.runId} (${manifest.suiteStatus})\n`);
}

async function readBoundedFile(filePath, maxBytes, label) {
  const metadata = await lstat(filePath);
  if (!metadata.isFile()) throw new Error(`${label} is not a regular file`);
  if (metadata.size > maxBytes) throw new Error(`${label} exceeds ${maxBytes} bytes`);
  return readFile(filePath);
}

function isInside(root, candidate) {
  const relative = path.relative(root, candidate);
  return relative !== "" && !relative.startsWith(`..${path.sep}`) && relative !== ".." && !path.isAbsolute(relative);
}

function reportErrors(errors) {
  for (const error of errors) process.stderr.write(`- ${error}\n`);
  process.exitCode = 1;
}

function fail(message) {
  reportErrors([message]);
}
