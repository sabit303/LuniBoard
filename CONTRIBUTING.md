# Contributing

Thanks for helping! Please read [docs/architecture.md](docs/architecture.md) and
[docs/development.md](docs/development.md) first.

## Ground rules

* **Keep the core platform-independent.** OS-specific code goes into `daemon/adapters/…` behind the
  existing interfaces (`adapters/clipboard.Adapter`/`Watcher`, service adapters).
* **One synchronization implementation.** Sync policy lives only in
  `daemon/internal/synchronization`. Clients (CLI, GUI, mobile) must not duplicate it.
* **Protocol changes** update `daemon/pkg/protocol`, `protocol/protocol.md`, the JSON Schemas and the
  examples in the same change. Incompatible changes bump the protocol version.
* **Security first.** Never add an endpoint that touches clipboard data without authentication;
  never log clipboard content; keep secrets 0600.
* **No placeholders presented as features.** Documentation must state what is verified.

## Workflow

1. `make vet test e2e` must pass (race detector on).
2. Add tests: unit tests for logic, integration tests for cross-component behaviour. Use the mock
   clipboard and fake command runner rather than the real clipboard in automated tests.
3. Update documentation, including `docs/platform-compatibility.md` when you verify a platform.
4. Keep commits focused and describe *why*.

## Testing on real desktops

Platform reports are very welcome. Run the opt-in real-clipboard tests
(`UCLIP_TEST_REAL_CLIPBOARD=1 go test -run Real ./adapters/...`) and a two-device manual test, then
report distribution, desktop environment, session type and results in an issue or PR.
