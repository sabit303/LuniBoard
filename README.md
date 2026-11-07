# LuniBoard

Copy on one device, paste on another — directly over your local network. No cloud, no account,
no relay. Clipboard content travels only between devices you paired, over mutually authenticated TLS.

> **Status: early development.** The Linux daemon and CLI (Phases 1–2, plus the desktop parts of
> Phases 4–5) are implemented and tested. Windows has a native text-only clipboard backend with
> manual startup; desktop verification is still pending. The Android app, iOS app, macOS clipboard
> adapter, images/files and the desktop GUI are **not implemented yet**. See the
> [roadmap](#roadmap) and [platform compatibility](docs/platform-compatibility.md).

## What works today

* Go daemon for Linux, independent of distribution and desktop environment
  * Wayland via `wl-clipboard` with change notifications (`wl-paste --watch`), polling fallback
  * X11 via `xclip`/`xsel` (polling)
  * automatic session detection, retries until a session is available, headless mode
* Native Windows text clipboard via `CF_UNICODETEXT` (polling), with manual daemon/start usage
* Device identity, mutual-TLS transport with certificate pinning, WebSocket sessions
* Pairing with QR/URI invitations, one-time codes, explicit approval and verification codes
* Bidirectional, multi-device synchronization of text and URLs with duplicate detection, loop and
  echo prevention, expiry, stale-overwrite protection and acknowledgements
* Manual and selective sending, per-device send/receive permissions, pause/resume, excluded
  content types, password-manager content skipped, bounded history (persistence opt-in)
* mDNS/DNS-SD discovery, automatic reconnection, revocation
* CLI (`start`, `stop`, `status`, `devices`, `discover`, `pair`, `unpair`, `send`, `history`,
  `pause`, `resume`, `config`, `logs`, `service`) over a local Unix-socket API
* systemd user service

## Quick start (Linux)

Any distribution — one command (installs the latest release for your user, starts it at login,
offers to install clipboard tools and open the firewall for your local network):

```sh
curl -fsSL https://raw.githubusercontent.com/sabit303/LuniBoard/main/scripts/install.sh | sh
luniboard pair                         # shows a QR code for the phone app
```

Run the same command again to update; `… | sh -s -- --uninstall` removes it.

Arch Linux (AUR):

```sh
yay -S luniboard-bin wl-clipboard      # or paru / makepkg
luniboard service install              # start now and at every login
luniboard pair                         # shows a QR code and pairing code
```

Other distributions, from source (Go ≥ 1.23):

```sh
sudo apt install wl-clipboard xclip    # or dnf / zypper
make install                           # builds and installs to ~/.local/bin
luniboard service install
luniboard pair
```

Pair a second computer with `luniboard pair connect '<uri shown by the first>'`, approve
on the first after comparing the verification code, then copy something. Full instructions:
[docs/setup.md](docs/setup.md).

Windows: use the [manual setup](docs/setup.md#windows-manual-setup). Windows installer, service/login
startup integration and firewall management are not implemented.

## Documentation

* [Setup and configuration](docs/setup.md)
* [Architecture](docs/architecture.md)
* [Protocol specification](protocol/protocol.md) · [schemas](protocol/schemas/v1) · [examples](protocol/messages)
* [Security model](docs/security.md)
* [Platform compatibility and mobile OS limits](docs/platform-compatibility.md)
* [Local API](docs/local-api.md) (for the CLI and a future GUI)
* [Development](docs/development.md)

## Repository

```
protocol/   versioned protocol: spec, JSON Schemas, examples
daemon/     Go daemon + CLI (shared core and platform adapters)
docs/       documentation
packaging/  service files and packaging
desktop/    future desktop GUI (uses the local API)
```

## Roadmap

| Phase | Scope | Status |
|-------|-------|--------|
| 1 | Protocol, Go core, identity, WebSocket transport, event handling, tests | **Done** |
| 2 | Linux MVP: Wayland + X11 adapters, systemd user service, CLI | **Done** (verified on Arch/Hyprland; Ubuntu/GNOME pending) |
| 3 | Android app (Compose, discovery, pairing, share sheet, in-app send, receive) | Not started |
| 4 | Bidirectional sync, dedupe, loop prevention, multi-device | **Done** in the daemon |
| 5 | TLS, authentication, revocation, reconnection, rate limiting, history controls | **Done** in the daemon |
| 6 | Windows and macOS adapters, background services, packaging | In progress: Windows text clipboard implemented; services, packaging and macOS pending |
| 7 | iOS app with Share Extension | Not started |
| 8 | Images and files (HTTP streaming, progress, limits, integrity) | Not started (protocol extension sketched) |
| 9 | Optional desktop GUI | Not started (local API ready) |

## License

[MIT](LICENSE)
