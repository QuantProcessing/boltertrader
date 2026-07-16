# Contributing Documentation

[简体中文](../zh-CN/contributing/documentation.md)

> English is canonical. **Owner:** this page owns public-safe curation,
> canonical-page ownership, English/Chinese mirror maintenance, and documentation
> review rules.

## Curate for the public boundary

Apply the [glossary](../reference/glossary.md) classifications before moving
development material into `docs/`. Public pages must not contain:

- API secrets, private keys, credential values, proxy credentials, or unredacted
  signed payloads;
- account addresses when they identify validation residue, balances, positions,
  order IDs, fill IDs, or cleanup residue;
- absolute local filesystem paths, machine/user names, raw command output, or
  full test logs;
- internal plans, goal/task identifiers, temporary traceability tables, reviewer
  conversations, traces, or implementation chronology; or
- links to private or development-only artifacts as evidence for current public
  behavior.

Exact environment-variable names, repository-relative source paths, complete
safe examples, and concise redacted candidate/date/scope summaries are allowed
when they help a user operate the project. Raw evidence remains outside the
public tree.

## One topic, one detail owner

Route readers through the [documentation index](../README.md) instead of copying
canonical inventories:

- Getting Started owns end-to-end onboarding journeys.
- Concepts owns stable architecture and execution semantics.
- Guides own task-oriented API, data, strategy, and recovery procedures.
- Venue pages own current venue/product behavior, caveats, selectors, and target
  use.
- The [capability matrix](../adapter-capabilities.md) owns the detailed static
  runtime product table.
- [Unsupported surfaces](../reference/unsupported.md) owns cross-venue absent,
  deferred, and SDK-only inventory.
- [Testing and evidence](../reference/testing.md) owns the complete command
  ladder and evidence rules.
- [Configuration](../reference/configuration.md) owns exact shared environment
  names, defaults, aliases, and endpoint-write safety.
- The [glossary](../reference/glossary.md) owns normative terminology.
- Contributor pages own extension and maintenance obligations.

A secondary page may state the minimum fact needed for its task and link to the
owner. It must not copy a full credential table, command ladder, capability
matrix, generic submission contract, SDK-only inventory, or status ontology.

## English and Chinese mirror contract

Every curated English page has one Chinese counterpart at the corresponding
`docs/zh-CN/**` path. English is the source of truth; Chinese is a maintained
mirror, not an independent specification. Each pair must:

- link to its counterpart at the top and state the canonical/mirror role;
- preserve heading hierarchy and order;
- preserve table headers, row keys, row order, and factual cell structure;
- preserve fenced-code count, fence language, warning/admonition placement, and
  list semantics;
- preserve commands, environment variables, identifiers, status terms, import
  paths, repository paths, URLs, venue/product names, and numeric defaults
  exactly; and
- map internal links to the corresponding language tree while keeping anchors
  valid.

Translate explanations, not technical tokens. An ordinary public documentation
change is incomplete until both members of the pair have been updated and
reviewed.

## Source-backed writing workflow

1. Identify the canonical detail owner and the exact user outcome.
2. Verify every behavioral claim against current code, contracts, Make targets,
   configuration loaders, and tests. Existing prose is a lead, not sole proof.
3. Update the canonical English owner first. Prefer deletion and links when a
   fact is duplicated elsewhere.
4. Freeze English headings, tables, commands, warnings, exact tokens, and links.
5. Update the Chinese mirror from that frozen English source.
6. Compare both pages structurally and token-for-token where content must remain
   exact.
7. Run link, formatting, capability, and relevant repository tests before
   claiming completion.

Development-generated material is a migration input only. Extract stable facts
to their public owner, independently verify them in current source, then remove
or retain the private artifact according to its own lifecycle. Never publish the
artifact itself to preserve traceability.

## Status, disagreement, and evidence language

Use the exact [status ontology](../reference/glossary.md). Name the venue,
product, environment, and concrete command/report/stream surface. Avoid bare
“supported,” an unqualified “market stream,” or “certified” without a candidate,
date, scope, target, zero-skip result, and terminal assertion.

When a static capability row, dynamic configuration, concrete method, test, and
existing page disagree, treat the mismatch as a defect. Document the narrowest
current behavior proven by code and tests, correct the owning source, and do not
average conflicting claims or select the broadest one. Demo/Testnet evidence
never implies production readiness.

A public validation summary contains only the candidate/date, named
Demo/Testnet environment and product, exact target, zero-skip result, scoped
terminal assertion, and material limitation. Do not paste raw logs or account
state.

## Review checklist

- The page has one clear owner and links to other owners instead of duplicating
  their inventories.
- Every relative link and local heading anchor resolves from both language
  trees.
- Every command, environment variable, API identifier, path, URL, and numeric
  default exists in current source.
- Runnable examples are complete and safe; excerpts are labeled and link to
  repository source.
- Status language distinguishes implementation, capability declarations,
  external evidence, and production readiness.
- English/Chinese headings, tables, fences, warnings, links, and exact technical
  tokens match their mirror contract.
- No secret, raw evidence, private path, development plan, task identifier, or
  temporary implementation history entered the public tree.
- `git diff --check`, `make test-capabilities`, and tests relevant to changed
  examples or behavior pass.

See [Contributing an adapter](adapters.md) for implementation-specific matrix and
verification obligations.
