# Local API

The daemon exposes a JSON-over-HTTP control API on a Unix domain socket:

* Linux: `$XDG_RUNTIME_DIR/universal-clipboard/daemon.sock` (fallback `/tmp/universal-clipboard-<uid>/`)
* With `--home DIR` / `UCLIP_HOME`: `DIR/run/daemon.sock`

The socket is mode 0600 in a 0700 directory and, on Linux, connections from other UIDs are rejected
(`SO_PEERCRED`). There is no network-reachable control API. The CLI uses this API exclusively, and
a desktop GUI must do the same — it must not reimplement synchronization logic.

Example:

```sh
curl --unix-socket "$XDG_RUNTIME_DIR/universal-clipboard/daemon.sock" http://daemon/v1/status
```

Errors: non-2xx status with `{"error":"message"}` (`400` invalid input, `404` unknown device or
request, `409` the remote device refused pairing, `500` otherwise).

| Method & path | Body | Response |
|---------------|------|----------|
| `GET /v1/status` | — | version, device, fingerprint, port, addresses, paused, clipboard backend status, counters, paths |
| `GET /v1/devices` | — | paired devices with permissions, last seen, addresses, `connected` and live `session` info |
| `PATCH /v1/devices/{id}` | `{"send":bool?,"receive":bool?}` | updated device |
| `DELETE /v1/devices/{id}` | — | revoke and disconnect |
| `GET /v1/discovered` | — | devices seen via mDNS (`paired` flag) |
| `POST /v1/pairing` | — | open a pairing window: `{code, uri, expires_at, fingerprint, addresses, attempts_remaining}` |
| `GET /v1/pairing` | — | `{open, invitation?, pending:[{id, device, fingerprint, verification_code, remote_addr, received_at}]}` |
| `DELETE /v1/pairing` | — | close the window |
| `POST /v1/pairing/requests/{id}/approve` | — | approve a pending request |
| `POST /v1/pairing/requests/{id}/reject` | — | reject it |
| `POST /v1/pairing/connect` | `{"uri":"uclip://pair?…"}` | pair this daemon with another listening device; blocks until the other side decides: `{status, verification_code, device?}` |
| `GET /v1/history?limit=N` | — | history entries, newest first, with per-device delivery status |
| `DELETE /v1/history` | — | clear history (memory and disk) |
| `POST /v1/sync/pause` / `POST /v1/sync/resume` | — | toggle automatic synchronization |
| `POST /v1/send` | `{"text":"…","devices":["id",…]?}` | manual send; waits up to 3 s for acks: `{event_id, results:{id:{status,reason?}}}` |
| `GET /v1/config` | — | `{"toml":"…"}` effective configuration |
| `POST /v1/shutdown` | — | graceful shutdown |

Go clients can use `internal/api.Client` (within this module).
