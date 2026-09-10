# Public market monitor

> English is canonical. [简体中文](../zh-CN/guides/gateway-monitor.md)

`cmd/gateway-monitor` is an independent Go application. It consumes the public
Gateway BBO stream through `clients/gateway`; it needs no Trader, browser, exchange
credentials, application database, or second venue connection. A running Gateway
and its capability-listener service token are required. Gateway itself retains
its existing storage and startup requirements.

## Run

Start Gateway using the [service guide](exchange-gateway.md). Supply its service
token as `GATEWAY_TOKEN`, then use [gateway-market](gateway-client.md) to select
exact `instrument_id` values from the public catalog. On Linux or macOS:

```sh
mkdir -p ./monitor-state
go run ./cmd/gateway-monitor \
  -gateway http://127.0.0.1:18080 \
  -instrument 'binance:live:spot:BTC-USDT' \
  -state ./monitor-state/btc.json \
  -window 1m -change-percent 5 -min-samples 5 \
  -max-gap 10s -cooldown 5m
```

Repeat `-instrument` for multiple instruments (maximum 64); one rule applies to
all selected instruments, with independent windows and cooldowns. `-duration 2m`
bounds a run; the default runs until Ctrl-C or SIGTERM. The state parent directory
must already exist. The command does not load `.env`, accept venue keys, create
accounts, or issue orders. It outputs JSON lines locally; no external delivery
channel is configured.

| Flag | Default | Meaning |
| --- | --- | --- |
| `-window` | `1m` | Maximum lookback, whole seconds from `2s` to `1h` |
| `-change-percent` | `5` | Absolute change threshold, greater than 0 through 100; at most 8 decimal places |
| `-min-samples` | `5` | Distinct UTC seconds with valid quotes, from 2 through window seconds + 1 |
| `-max-gap` | `10s` | Maximum source quote age/interval, from `1s` through window |
| `-cooldown` | `5m` | Minimum interval between price alerts for each instrument, `1s` through `24h` |
| `-state` | required | Local checkpoint path, exclusive to one process |
| `-duration` | `0` | Stop after this duration; `0` means continuous |

## Price and data semantics

The price is `(best bid + best ask) / 2`, calculated with decimals. It is neither
a trade price nor a mark price. The change is `100 * (current / reference - 1)`.
The reference is the earliest retained quote within the lookback window. Both
rises and falls can trigger. The comparison uses unrounded decimals; the output
percentage is rounded to 8 places.

Only the first valid quote in each UTC second is retained as a reference sample;
every valid incoming quote is evaluated, including changes within that second.
The window is a maximum age, not a promise that a full window has accumulated:
evaluation begins after `min-samples` distinct seconds. Each event reports the
actual reference and observation timestamps, prices, and sample count. This is a
window-start return rule, not rolling-peak drawdown or a historical backfill.

Gateway coalesces BBO fanout to at most one delta per 50 ms. The monitor checks
every quote it receives, not every native venue tick. Increasing BBO sequence
jumps are valid; explicit gaps/generation changes still require recovery.

Malformed wire data (including nonpositive prices or negative quantities) is a
client protocol error and stops the stream. Among protocol-valid quotes, zero
top-of-book quantities, crossed bid/ask, timestamps in the future, age exceeding
`max-gap`, or timestamps no newer than the last valid quote are ignored.
Clocks must be synchronized. Ignored data and WebSocket pongs never
refresh the data watermark. Checks run before each message and on a timer at most
one second apart. An unchanged price with a newer timestamp remains valid data.

Gateway sequence gaps or disconnects interrupt immediately. Source silence beyond
`max-gap` also interrupts even if the socket is alive. Interruptions clear the
window. The first newer, fresh quote emits recovery and starts a new window; it
cannot compare against prices from before the interruption.

## Local events and restart

| `kind` | Meaning |
| --- | --- |
| `state` | Startup/insufficient data, or return to normal |
| `price_change` | Threshold met with enough samples and cooldown elapsed |
| `interrupted` | Source timeout, resynchronization, or fatal stream termination |
| `recovered` | Fresh data returned; the price rule still needs a new window |

`state` is `insufficient_data`, `normal`, `triggered`, or `interrupted`.
Recovery is a transition event whose state is `insufficient_data`. Price cooldown
is shared by rises and falls for one instrument, survives restart and interruption,
and permits another alert on a fresh qualifying quote after it expires. It does
not suppress interruption/recovery or normal-state diagnostics.

The checkpoint stores window samples, freshness, cooldown, instance/sequence IDs,
and a bounded pending output batch. It stores no credentials. Samples are saved
at most one second apart; alerts are saved before output. Files use mode 0600,
atomic replacement and filesystem sync. A local OS lock prevents two processes
from using the same path and is released on process death. Keep the `.lock` file;
do not delete it while the monitor is running. Use a local filesystem with working
file locks and rename/sync semantics.

A short restart can retain a fresh saved window. A stale checkpoint emits an
interruption and rebuilds it; no missing quotes are invented. A crash may lose up
to one second of samples that had no pending event. Corrupt, future-dated or
incompatible state causes startup failure. Changing the Gateway URL, instrument
set or rule requires a new state path; token changes and run-duration changes do
not invalidate it.

An event's `id` remains identical when pending output is replayed. A crash after
writing a JSON line but before acknowledging it in the checkpoint can duplicate
that line after restart. A killed partial write can leave a partial final line.
Consumers must validate complete JSON lines and deduplicate IDs. A successful
stdout write does not prove durable receipt by another process or external
delivery. Storage/output failure stops the command; it does not continue silently.
Authentication or protocol errors end the stream and exit nonzero. Network loss
uses the client's bounded reconnection policy.

## Verification

```sh
make test-monitor
go test -race -short ./services/monitor ./cmd/gateway-monitor ./clients/gateway
```

Replay covers abrupt drops/rises, intra-second movement, duplicate/out-of-order/
stale/future quotes, crossed books, silence, cooldown, restart, pending output,
and incompatible checkpoints. Local WebSocket tests cover disconnect/reconnect,
generation reset, a silent live connection, authentication failure, cancellation,
and subscription release. Public-market availability is a separate runtime check.
