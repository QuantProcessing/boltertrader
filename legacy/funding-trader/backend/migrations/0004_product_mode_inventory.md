# Product-mode cutover inventory

This inventory is the review surface for migration `0004_product_mode.sql`.
The cutover runner executes it only through the `migrate` startup mode and only
after a verified backup proof, the old-process assertion, the application
session fence, the advisory lock, and exclusive `NOWAIT` table locks succeed.

| Table | Classification | Source scope/key | Target scope/key | Cutover policy |
| --- | --- | --- | --- | --- |
| `users` | authoritative principal | intrinsic `environment` | no product field | Drop the intrinsic field; revoke every session. |
| `sessions` | authoritative security | user/session digest | unchanged | Preserve rows and revoke all active sessions/CSRF pairs. |
| `idempotency_records` | authoritative | principal + environment + method + path + key | principal + mode + method + path + key | Preflight every Demo/Testnet merge collision; completed execution responses must contain one exact qualified identity per immutable execution leg. |
| `execution_previews` | transient | principal + environment + id | principal + mode + id | Clear; callers must create a fresh preview. |
| `opportunities` | derived | environment + projection ID | mode + projection ID | Clear and rebuild from qualified market data. |
| `alerts` | derived | environment + projection ID | mode + projection ID | Clear with opportunities and rebuild. |
| `executions` | authoritative | principal + environment + execution ID | principal + mode + execution ID | Preserve IDs, lineage, hashes, state, quantities, and evidence. |
| `execution_legs` | authoritative | execution + role | execution + role with venue + venue environment | Backfill only from one unambiguous payload identity; canonical instrument conflicts abort. |
| `execution_fills` | authoritative | execution/fill identities | unchanged | Preserve IDs, quantities, prices, fees, and foreign keys; embed the immutable leg route in serialized fill evidence. |
| `execution_transitions` | authoritative | principal + environment + execution | principal + mode + execution | Preserve request IDs and append an exact, role-ordered immutable route snapshot. |
| `positions` | authoritative | principal + environment + position ID | principal + mode + position ID | Require the opening execution lineage and exact long/short account, venue, instrument, and native-environment evidence. |
| `activities` | authoritative | principal + environment + activity ID | principal + mode + activity ID | Preserve position/execution links. |
| `audit_events` | authoritative | principal + environment + audit ID | principal + mode + audit ID | Preserve action, outcome, and request ID; execution-related rows receive exact immutable leg routes while authentication-only rows intentionally receive none. |
| `funding_observations` | required history | environment + account + instrument + time | mode + venue environment + account + instrument + time | Derive exact native environment from explicit legacy scope or canonical instrument; conflicts abort. |
| `read_model_events` | transient cursor namespace | boot + environment + principal + sequence | boot + mode + principal + sequence | Clear and force a new snapshot/cursor namespace. |
| `cutover_control` | new recovery control | none | singleton contract state | Records verified backup evidence and the restore-before-reopen boundary. |

The only accepted source-to-target mappings are:

- `demo`, `testnet`, or already-normalized `test` → `test`;
- `mainnet` or `live` → `live`;
- native venue environment remains exactly `demo`, `testnet`, or `live`.

Any unsupported product value, authoritative key collision, missing leg
identity, canonical instrument conflict, row-invariant mismatch, schema drift,
old application session, or concurrent table lock aborts the transaction.
Before commit, the runner independently revalidates mode/native isolation plus
the serialized idempotency, fill, transition, position, and route-audit
evidence. A mismatch rolls the entire transaction back.

The Phase 4 binary intentionally keeps normal traffic closed after a validated
v4 cutover: the legacy serve migration runner recognizes the valid
`cutover_control` state and returns the typed
`ErrProductModeServePending` boundary instead of misreporting legacy schema
drift or opening environment-scoped stores. Operators keep
`FT_STARTUP_MODE=maintenance` until the mode-native private V1 serve cutline is
installed in the next phase.
