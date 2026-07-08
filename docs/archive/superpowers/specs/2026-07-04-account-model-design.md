# Obsolete Design Note

This early account-model design is obsolete. It was superseded by the
2026-07-07 account-mode simplification plan and the current implementation in
`core/model/account.go`, `runtime/risk`, `runtime/portfolio`, and the adapter
account-state reporters.

The old proposal treated a shared exchange account-mode envelope as part of the
portable runtime model. That design was removed. The runtime now consumes a
reported account-state safety envelope: logical account id, venue, account type,
balances, margins, reported flag, event id, event timestamp, and init timestamp.
Product support is expressed by adapter/runtime capabilities, while exchange
account configuration details remain adapter or SDK preflight concerns.
