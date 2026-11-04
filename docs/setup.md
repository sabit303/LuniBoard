# Setup (Linux)

The daemon runs on any mainstream distribution (tested on Arch Linux; Ubuntu, Fedora and Debian use
the same code paths) and any desktop: GNOME, KDE Plasma, XFCE, Hyprland, Sway, i3, … It does not use
desktop-specific APIs. See [platform-compatibility.md](platform-compatibility.md) for what has been
verified.

## 1. Install the clipboard tools

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

## 2. Build and install

Requires Go ≥ 1.23.

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

## 3. Firewall

Allow inbound **TCP 47823** (or your `network.port`) and **UDP 5353** (mDNS) on your LAN, e.g.

```sh
sudo ufw allow from 192.168.0.0/16 to any port 47823 proto tcp
sudo ufw allow from 192.168.0.0/16 to any port 5353 proto udp
sudo firewall-cmd --add-port=47823/tcp --add-service=mdns --permanent && sudo firewall-cmd --reload
```

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

`luniboard config init` writes the defaults to `~/.config/luniboard/config.toml`;
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
| `clipboard.backend` | `auto` | `auto`, `wayland`, `x11`, `none` (headless: received text only goes to history) |
| `clipboard.poll_interval` | `750ms` | Polling period when change notifications are unavailable |
| `clipboard.skip_sensitive` | `true` | Ignore content marked secret by password managers |
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

## Uninstall

```sh
luniboard service uninstall
rm ~/.local/bin/luniboard
rm -r ~/.config/luniboard ~/.local/share/luniboard ~/.local/state/luniboard
```
