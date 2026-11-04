# Development

## Layout

```
protocol/            specification, JSON Schemas, example messages
daemon/              Go module: daemon + CLI
  cmd/luniboard/   entry point
  pkg/protocol/              wire types (public)
  internal/…                 core packages (see docs/architecture.md)
  adapters/clipboard/…       OS clipboard adapters (wayland, x11, mock, cmdutil)
  adapters/service/linux/    systemd user service
  tests/integration/         full daemons in-process with mock clipboards
  tests/e2e/                 the real binary driven through the CLI (build tag e2e)
packaging/           service files and packaging recipes
docs/                documentation
android/, ios/       mobile apps (not started yet; see docs/platform-compatibility.md)
```

## Requirements

Go ≥ 1.23. Optional for manual testing: `wl-clipboard`, `xclip`.

## Common tasks

```sh
make build        # bin/luniboard
make test         # unit + integration tests with the race detector
make e2e          # builds the binary and drives two daemons through the CLI
make vet          # go vet
make cross        # cross-compile linux/amd64, linux/arm64, windows/amd64, darwin/arm64
```

Opt-in tests that touch the real system (they overwrite your clipboard / use the network):

```sh
cd daemon
LUNIBOARD_TEST_REAL_CLIPBOARD=1 go test -run Real ./adapters/...   # wl-clipboard / xclip / xsel
LUNIBOARD_TEST_MDNS=1 go test -run Live ./internal/discovery/      # multicast DNS on the host
LUNIBOARD_TEST_LOG=1 go test -v ./tests/integration/               # integration tests with debug logs
```

## Running several daemons on one machine

`--home DIR` (or `LUNIBOARD_HOME`) keeps config, identity, socket and logs under one directory:

```sh
mkdir -p /tmp/a/config /tmp/b/config
printf '[network]\nport = 47901\n' > /tmp/a/config/config.toml
printf '[network]\nport = 47902\n[clipboard]\nbackend = "none"\n' > /tmp/b/config/config.toml
luniboard --home /tmp/a start
luniboard --home /tmp/b start
luniboard --home /tmp/a pair            # copy the 'pair connect' line
luniboard --home /tmp/b pair connect 'luniboard://pair?…'
```

Keep socket paths short (Unix sockets are limited to ~108 bytes).

## Testing strategy

| Level | Where | What |
|-------|-------|------|
| Unit | `pkg/protocol` | validation, codec, pairing URI, verification code; **schema contract tests** against `protocol/schemas` and `protocol/messages` |
| Unit | `internal/synchronization` | engine on an in-memory network: fan-out, loops, echo, dedupe, expiry, stale, clock skew, pause, exclusions, permissions, size limits, reconnect, relay mesh, write failures |
| Unit | `internal/transport` | real TLS/WebSocket: pairing over the network, pin mismatch, unpaired rejection, revocation, reconnect after restart, duplicate-connection tie-break, identity mismatch, invalid/oversized messages, rate limiting |
| Unit | adapters, clipboard service, config, device, history, pairing, security, discovery | fake command runner for adapters, backend detection matrix, watch/poll fallback |
| Integration | `tests/integration` | complete daemons via the local API: desktop pairing, a protocol-level mobile client (share, receive, duplicate), multi-device fan-out, selective send, permissions, relay, revocation, restart without stale replay, pause, history, limits, single instance |
| End-to-end | `tests/e2e` | the compiled binary: start, pair, approve, send, history, unpair, stop |

## Conventions

* Platform-specific code lives only in `adapters/` (and build-tagged files for OS calls).
* `internal/synchronization` is the single implementation of sync policy. Clients (CLI, GUI,
  mobile) never duplicate it.
* Errors are wrapped with context; logs are structured (`log/slog`) and never include clipboard text.
* Every protocol change updates `pkg/protocol`, `protocol/protocol.md`, the schemas and examples
  together — the contract tests enforce consistency.
