# Setup

The Linux daemon runs on any mainstream distribution (tested on Arch Linux; Ubuntu, Fedora and Debian use
the same code paths) and any desktop: GNOME, KDE Plasma, XFCE, Hyprland, Sway, i3, … It does not use
desktop-specific APIs. See [platform-compatibility.md](platform-compatibility.md) for what has been
verified.

Windows users can skip the Linux installation steps and use [manual setup](#windows-manual-setup).

## 1. Linux clipboard tools

| Session | Package |
|---------|---------|
| Wayland | `wl-clipboard` (provides `wl-copy`, `wl-paste`) |
| X11     | `xclip` (preferred) or `xsel` |

```sh
sudo pacman -S wl-clipboard xclip        # Arch
sudo apt install wl-clipboard xclip      # Debian / Ubuntu
sudo dnf install wl-clipboard xclip      # Fedora
```

Installing both is harmless; the daemon detects the session type.

## 2. Install on Linux

**Easiest (any distribution):**

```sh
curl -fsSL https://raw.githubusercontent.com/sabit303/LuniBoard/main/scripts/install.sh | sh
```

The [installer](../scripts/install.sh) finds the newest release, downloads the build for your CPU
(x86_64 or aarch64), verifies its SHA-256 checksum, installs `luniboard` to `~/.local/bin` (no root),
offers to install `wl-clipboard`/`xclip` if missing, starts the systemd user service, and offers to
open the firewall. Options: `--version 0.1.0`, `--no-service`, `--yes`, `--uninstall`.
If you prefer to read it first: download it, inspect it, then run `sh install.sh`.

**Arch Linux:** install the AUR package `luniboard-bin` (prebuilt binary, systemd user unit in
`/usr/lib/systemd/user`), then enable it for your user:

```sh
yay -S luniboard-bin
luniboard service install      # enables /usr/lib/systemd/user/luniboard.service
```

**From source** (any distribution, Go ≥ 1.23):

```sh
make build                     # -> bin/luniboard
make install                   # copies to ~/.local/bin (PREFIX=… to change)
luniboard service install   # systemd user service, enabled and started
```

Without systemd, run `luniboard start` from your session's autostart, or
`luniboard daemon` in the foreground.

### Session environment for systemd services

The daemon needs `WAYLAND_DISPLAY` (Wayland) or `DISPLAY`/`XAUTHORITY` (X11). GNOME and KDE export
these to systemd automatically. If you use a window manager that does not (Sway, Hyprland, i3, …):

* Wayland: the daemon finds the compositor socket in `$XDG_RUNTIME_DIR` by itself when exactly one
  exists. Otherwise add to your compositor config, e.g. Hyprland
  `exec-once = dbus-update-activation-environment --systemd WAYLAND_DISPLAY XDG_CURRENT_DESKTOP`,
  Sway `exec dbus-update-activation-environment --systemd WAYLAND_DISPLAY`.
* X11: add `systemctl --user import-environment DISPLAY XAUTHORITY` to `~/.xinitrc` / i3 config.

`luniboard status` shows the detected backend; it keeps retrying every 10 s until a
session appears, so starting before the graphical session is fine.

## 3. Linux firewall

LuniBoard needs inbound **TCP 47823** (your `network.port`) and, for discovery, **UDP 5353** (mDNS)
from your local network. It configures **ufw** and **firewalld** for you:

* the AUR package runs `luniboard firewall allow` when it is installed (and removes its rules on uninstall);
* `luniboard service install` offers to do it if a firewall is active.

Rules only allow the subnet(s) of your current Wi-Fi/Ethernet (e.g. `192.168.0.0/24`), never the
internet, and are tagged `LuniBoard` so they can be removed cleanly.

```sh
luniboard firewall status                 # which firewall is active
luniboard firewall allow                  # open for the current local network (asks for sudo)
luniboard firewall allow --dry-run        # show the exact commands first
luniboard firewall allow --any-private    # all private ranges (laptop used on several trusted networks)
luniboard firewall allow --subnet 192.168.1.0/24 --no-mdns
luniboard firewall remove                 # delete every rule LuniBoard added
```

After joining a different home network, run `luniboard firewall allow` again. For plain
**nftables**/**iptables** setups, `luniboard firewall status` prints the rules to add manually.

## Windows (manual setup)

Build from source in PowerShell with the Go version required by `daemon/go.mod`, then run in your
logged-in desktop session:

```powershell
cd daemon
go build -o luniboard.exe ./cmd/luniboard
.\luniboard.exe config init
.\luniboard.exe daemon
```

`daemon` runs in the foreground. Use `.\luniboard.exe start` instead for a detached process,
`.\luniboard.exe status` to check it, and `.\luniboard.exe stop` to stop it. These are manual
commands, not a Windows service or login-startup registration. Use `.\luniboard.exe` for the
remaining CLI examples below.

The default `clipboard.backend = "auto"` selects `windows`; it can also be set explicitly to
`"windows"`. The native backend syncs text/URLs only, preserves line endings, and polls every
`clipboard.poll_interval` (750 ms by default). No external clipboard tools are needed. Status shows
`windows, poll` once the clipboard is available. Images/files are not supported.

The existing local API uses Unix-domain sockets and requires Windows 10 1803+ or Windows 11.
Keep its socket path below 108 bytes; use a short, private `--home` directory if a long profile path
prevents startup. The daemon relies on the directory's existing Windows ACLs. Data and logs normally
live under `%LOCALAPPDATA%\LuniBoard`.

There is no Windows installer, service/login-startup integration or firewall helper. Do not use the
Linux installer or `service install`/`firewall allow` on Windows. Allow inbound TCP 47823 (or your
configured port) and UDP 5353 for mDNS manually, restricted to trusted local networks.

## 4. Pair devices

```sh
luniboard pair
```

prints a QR code, a pairing code and the addresses, then waits. On the other device scan the QR
code (or enter the code), compare the **verification code** shown on both screens and approve.

Pair two computers:

```sh
# computer A
luniboard pair            # shows: luniboard pair connect 'luniboard://pair?…'
# computer B
luniboard pair connect 'luniboard://pair?…'
# approve on A after comparing the verification codes
```

## 5. Everyday use

```sh
luniboard status                 # health, backend, counters
luniboard devices                # paired devices and connection state
luniboard devices set pixel --send off   # stop pushing to a device
luniboard send "text"            # manual send (also: echo text | luniboard send)
luniboard send --to laptop "x"   # selective send
luniboard history [-n 20] [--full] | history clear
luniboard pause | resume
luniboard unpair <device>        # revoke
luniboard logs [-f]
luniboard stop | start
```

All commands accept `--json`.

## 6. Configuration

`luniboard config init` writes the defaults to `~/.config/luniboard/config.toml` on Linux or
`%APPDATA%\LuniBoard\config.toml` on Windows;
`config show` prints the effective configuration. Unknown keys are rejected. Restart the daemon
after editing.

| Key | Default | Meaning |
|-----|---------|---------|
| `device.name` | host name | Name shown to other devices |
| `network.listen_address` | `""` (all) | IP to bind |
| `network.port` | `47823` | TCP port (`0` = random) |
| `network.max_connections` | `64` | Concurrent inbound connections |
| `discovery.enabled` | `true` | Browse mDNS for paired devices |
| `discovery.advertise` | `true` | Advertise this device via mDNS |
| `discovery.interfaces` | `[]` | Restrict mDNS to these interfaces |
| `clipboard.backend` | `auto` | `auto`, `wayland`, `x11`, `windows`, `none` (headless: received text only goes to history) |
| `clipboard.poll_interval` | `750ms` | Polling period for Windows, X11 or Wayland without change notifications |
| `clipboard.skip_sensitive` | `true` | Ignore password-manager hints and Windows clipboard exclusion metadata |
| `sync.enabled` | `true` | Initial state; `pause`/`resume` change it at runtime |
| `sync.auto_send` | `true` | Send local clipboard changes automatically |
| `sync.receive` | `true` | Apply content received from devices |
| `sync.max_payload_bytes` | `1048576` | Largest text sent or accepted (≤ 16 MiB) |
| `sync.max_event_age` | `2m` | Older events are not applied (no stale replay) |
| `sync.ordering_tolerance` | `2s` | Clock jitter tolerated when ordering events |
| `sync.excluded_content_types` | `[]` | e.g. `["url"]` |
| `sync.relay` | `false` | Forward events between devices that are not paired with each other |
| `history.enabled` | `true` | Keep an in-memory history |
| `history.persist` | `false` | Save history to disk (0600) |
| `history.max_entries` | `50` | History size |
| `pairing.window` | `2m` | How long a pairing window stays open |
| `pairing.max_attempts` | `5` | Wrong codes before the window closes |
| `security.events_per_second` / `event_burst` | `5` / `20` | Per-device incoming event rate limit |
| `logging.level` / `format` / `file` | `info` / `text` / `true` | Logs (never contain clipboard text) |

## Uninstall (Linux)

```sh
luniboard service uninstall
rm ~/.local/bin/luniboard
rm -r ~/.config/luniboard ~/.local/share/luniboard ~/.local/state/luniboard
```
