# Security

Clipboard content routinely contains passwords, tokens and private messages. This document lists
the threats considered, the controls implemented and the known limitations. To report a
vulnerability see [SECURITY.md](../SECURITY.md).

## Threat model

In scope: other hosts on the same LAN (curious or malicious), including active attackers who can
spoof mDNS, ARP-spoof or intercept traffic; other local users on the same computer; malformed or
hostile peers after pairing (a compromised paired device).

Out of scope: a compromised local user account or OS (it can read the clipboard directly), physical
access to an unlocked device.

## Controls

| Risk | Control |
|------|---------|
| Eavesdropping / tampering on the LAN | TLS 1.2+/1.3 only, ECDHE + AEAD suites. Event payloads also carry a SHA-256 digest that receivers verify. |
| Unauthenticated access to the clipboard endpoint | The listener requires a client certificate. `/v1/ws` accepts only fingerprints in the trusted registry (`401` otherwise). There is no unauthenticated endpoint that reads or writes clipboard data. |
| Discovery spoofing | mDNS results are hints. Dialing pins the stored fingerprint; a different certificate is rejected and logged. |
| Unauthorized pairing | Pairing only while the user has opened a window (2 min default), with a 40-bit one-time code, at most 5 wrong attempts per window, per-IP rate limiting, one pending request at a time, and **explicit approval** on the listening device. The window closes after one success. |
| Man-in-the-middle during pairing | QR invitations carry the server fingerprint, which the client pins. For manually entered codes, both devices show a 6-digit verification code derived from both fingerprints; a MITM cannot make them match. |
| Identity confusion | The device ID in `hello` must match the ID stored for the certificate at pairing. |
| Revocation | `unpair` deletes the fingerprint and immediately closes the session; the device gets `401` afterwards. |
| Per-device control | `send` / `receive` permissions per device; `receive` is re-checked for every event. |
| Resource exhaustion | Connection cap (`max_connections`), header/handshake timeouts, WebSocket frame limit derived from `max_payload_bytes` (default 1 MiB, hard cap 16 MiB), per-session event rate limit and message-flood disconnect, bounded dedupe cache and history, bounded command output when reading the clipboard. |
| Replay / stale overwrite | Event IDs are deduplicated; events older than `max_event_age` are rejected; no offline queue. |
| Sync loops | Origin tracking, never sending back to the origin, echo suppression by content hash, dedupe for relayed events. |
| Local API abuse by other users | Unix socket mode 0600 inside a 0700 directory; on Linux the peer UID is also verified with `SO_PEERCRED`. |
| Secrets at rest | Private key, registry and (optional) history are 0600; startup refuses a key readable by others. History persistence is **off by default**; disabling it deletes the file. Logs never contain clipboard text. |
| Password managers | Content flagged with `x-kde-passwordManagerHint` (KeePassXC and others) is not synchronized or recorded (`clipboard.skip_sensitive`, default on). |
| User control | `pause`/`resume`, `sync.excluded_content_types`, `sync.receive`, `sync.auto_send`, `history.enabled`, `history clear`. |
| Proxies | LAN connections never use HTTP proxy settings; redirects are refused. |

## Limitations and residual risks

* **Trust in paired devices.** A paired device can send clipboard content to you (subject to
  `receive`) and receives your content (subject to `send`). With `sync.relay = true`, content from
  devices paired with a peer can reach you through that peer.
* **Key storage.** On Linux the private key is a 0600 file, like SSH keys; it is not stored in the
  Secret Service/TPM. Anyone who can read your files can impersonate the device.
* **Pairing window DoS.** A LAN attacker can burn the 5 attempts and close a pairing window. This
  does not grant access; re-open the window.
* **Verification code.** The 6-digit code only protects manual-code pairing if the user actually
  compares it.
* **Metadata.** mDNS advertises the device name, ID and fingerprint to the LAN. Set
  `discovery.advertise = false` (pairing via QR still works, and remembered addresses are dialed).
* **Clipboard managers.** Text written by the daemon is visible to other applications on the same
  desktop like any copied text.
* **Unaudited.** The code has automated tests, including adversarial transport tests, but has not
  had an external security audit.
