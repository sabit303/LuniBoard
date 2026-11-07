# Architecture

## Goals and constraints

* LAN-first, no cloud, no accounts. Clipboard data only travels between paired devices over
  mutually authenticated TLS.
* One shared, platform-independent core (Go). Platform specifics live behind small interfaces.
* Honest about OS limits: Android and iOS restrict background clipboard access, so mobile clients
  rely on explicit user actions (share sheet, in-app send) for outgoing content.

## Components

```
                           ┌──────────────────────────── luniboard daemon ───────────────────────────┐
  CLI / future GUI ──unix──▶ internal/api  ──▶ internal/application (wiring, lifecycle)                        │
   socket (0600)           │                     │                                                            │
                           │   internal/clipboard ── adapters/clipboard/{wayland,x11,mock}  (wl-clipboard,   │
                           │     detect + watch/poll        ▲                                  xclip, xsel)   │
                           │            │ ObserveLocal      │ WriteText                                      │
                           │            ▼                   │                                                │
                           │   internal/synchronization (Engine: dedupe, loop/echo prevention, LWW, acks)    │
                           │            │ Peers.Send        ▲ HandleIncoming / HandleAck                     │
                           │            ▼                   │                                                │
                           │   internal/transport (HTTPS mTLS listener, /v1/pair, /v1/ws sessions, dialer)   │
                           │        │            │                                                          │
                           │ internal/pairing  internal/device (trusted registry)   internal/discovery (mDNS)│
                           │        internal/security (identity, TLS, limits)   internal/history  storage   │
                           └───────────────────────────────────────────────────────────────────────────────────┘
                                         ▲ wss + https (pinned mTLS)                ▲ DNS-SD _luniboard._tcp
                                         │                                          │
                              Android / iOS apps, other daemons ─────────────────────┘
```

| Package | Responsibility |
|---------|----------------|
| `pkg/protocol` | Wire types, validation, envelope codec, pairing URI, fingerprints, verification codes. No I/O. Public so other Go clients can reuse it. |
| `internal/security` | Device identity (ECDSA P-256 self-signed cert, `0600` key), TLS server/client configs with fingerprint pinning, keyed rate limiter. |
| `internal/device` | Persistent registry of trusted devices and per-device permissions; change notifications (used for revocation). |
| `internal/pairing` | Pairing windows, one-time codes, attempt limits, pending requests awaiting approval; HTTP client for pairing with another device. |
| `internal/transport` | LAN listener (mTLS), `/v1/pair`, `/v1/ws`, hello handshake, session registry with duplicate tie-break, outbound dialer with backoff, ping, rate and size limits. |
| `internal/synchronization` | The only synchronization logic. Turns local clipboard changes into events, applies incoming events, deduplicates, prevents loops and stale overwrites, tracks acknowledgements. Depends only on interfaces. |
| `internal/clipboard` | Chooses the clipboard adapter for the session, watches or polls it, re-detects after failures. |
| `adapters/clipboard/*` | OS clipboard integrations implementing `Adapter` (+ optional `Watcher`). |
| `adapters/service/linux` | systemd user unit generation and control. |
| `internal/discovery` | mDNS advertisement and browsing (hints only). |
| `internal/history` | Bounded history, optional persistence. |
| `internal/api` | Local control API over a Unix socket (server and client). |
| `internal/cli` | Command-line interface; a thin API client except for `daemon`. |
| `internal/application` | Constructs and connects everything; owns startup and graceful shutdown. |

Dependencies point inwards: `synchronization` knows nothing about sockets or operating systems,
`transport` knows nothing about clipboards, adapters know nothing about the network. The engine is
exercised with an in-memory network and mock clipboard in its unit tests; the integration tests run
complete daemons with mock clipboards.

## Core ↔ platform contracts

```go
// adapters/clipboard
type Adapter interface {
    Name() string
    ReadText(ctx context.Context) (Content, error)   // ErrNoText, ErrUnavailable
    WriteText(ctx context.Context, text string) error // must be visible to ReadText on return
}
type Watcher interface {
    Watch(ctx context.Context, notify func()) error   // blocks; error => fall back to polling
}

// internal/synchronization
type Clipboard interface { WriteText(ctx context.Context, text string) error }
type Peers     interface { Connected() []PeerInfo; Send(peerID string, msg []byte) error }
type Devices   interface { Get(id string) (device.Device, bool) }
type History   interface { Add(history.Entry); UpdateDelivery(eventID, deviceID string, s history.DeliveryStatus) }
```

Adding Windows or macOS means implementing `Adapter` (+ `Watcher` where the OS can notify) and
registering it in `internal/clipboard/detect.go`, plus a service adapter. Nothing else changes.

## Data flow

**Local copy → peers.** The clipboard service is notified (`wl-paste --watch`) or polls (X11), then
calls `Engine.ObserveLocal(read)`. Under the engine's write lock the content is read and hashed; if
the hash equals the last known content (our own write, or a repeated notification) nothing happens.
Otherwise an event is created and sent to every connected device whose `send` permission is on.

**Peer event → local clipboard.** A session decodes the envelope and calls
`Engine.HandleIncoming`, which validates, deduplicates, checks permissions, pause state, exclusions,
expiry and ordering, sets the last-known hash, writes the clipboard and returns the ack that the
session sends back.

**Mobile.** Android/iOS are client-only peers: they pair using the QR invitation, keep a WebSocket
to the desktop while permitted, send events on explicit user action, and apply received events when
the OS allows. The same protocol is used; there is no mobile-specific server code.

## Lifecycle

`application.App.Run` binds the API socket (refusing to start if another instance owns it), binds
the LAN port, starts the transport, clipboard monitor and discovery, then waits for a signal or an
API shutdown request. Shutdown cancels contexts, closes the pairing window, closes all sessions with
1001, stops the listener and removes the socket.

## Storage

| File | Content | Mode |
|------|---------|------|
| `$XDG_DATA_HOME/luniboard/identity/key.pem` | private key | 0600 (startup fails if looser) |
| `…/identity/cert.pem`, `identity.json` | certificate, device id | 0644 / 0600 |
| `…/devices.json` | trusted devices, fingerprints, permissions | 0600 |
| `…/history.json` | history, only when `history.persist = true` | 0600 |
| `$XDG_CONFIG_HOME/luniboard/config.toml` | configuration | 0600 |
| `$XDG_STATE_HOME/luniboard/daemon.log` | logs (never clipboard content) | 0600 |
| `$XDG_RUNTIME_DIR/luniboard/daemon.sock` | local API | 0600 in a 0700 dir |

All writes are atomic (temp file + fsync + rename). `--home DIR` / `LUNIBOARD_HOME` relocates everything.
