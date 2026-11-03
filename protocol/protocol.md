# Universal Clipboard Protocol — version 1

Status: **implemented** by the Go daemon (`daemon/pkg/protocol`, `daemon/internal/transport`,
`daemon/internal/pairing`, `daemon/internal/synchronization`). Machine-readable schemas live in
[`schemas/v1`](schemas/v1); examples in [`messages`](messages). The contract tests in
`daemon/pkg/protocol/schema_test.go` keep code, schemas and examples in sync.

The key words MUST, SHOULD and MAY are used as in RFC 2119.

## 1. Overview

Devices on the same LAN exchange clipboard content directly. There is no server or relay.

* **Listening devices** (desktop daemons) accept HTTPS connections on a configurable TCP port
  (default `47823`) and advertise themselves with DNS-SD.
* **Client-only devices** (Android, iOS) never listen; they connect to listening devices.
* Two listening devices both dial each other; duplicate connections are resolved deterministically (§6.4).

Every connection is TLS with **both** sides presenting a certificate. Trust is established once by
pairing (§5) and is bound to the SHA-256 fingerprint of each device's certificate.

## 2. Identity

Each device generates, on first start, a long-lived key pair and a self-signed X.509 certificate.

* Key: ECDSA P-256 RECOMMENDED (Ed25519 and RSA ≥ 2048 are accepted).
* `device_id`: random string matching `^[A-Za-z0-9._-]{1,64}$` (the daemon uses a UUIDv4).
* **Fingerprint**: lowercase hex SHA-256 of the DER-encoded certificate (64 characters).

The fingerprint is the identity that peers pin. Regenerating the certificate requires re-pairing.

## 3. Discovery (DNS-SD / mDNS)

Listening devices advertise service type `_uclip._tcp` in domain `local.` with TXT keys:

| key    | value                                   |
|--------|-----------------------------------------|
| `id`   | device_id                               |
| `v`    | highest supported protocol version      |
| `fp`   | certificate fingerprint                 |
| `os`   | `linux`, `windows`, `macos`, `android`, `ios`, `other` |
| `name` | display name                            |

Discovery is a **hint only**. A discovered device is never trusted because of its advertisement;
connections are only accepted from, and made to, certificates whose fingerprint was pinned during
pairing. A TXT `fp` that differs from the pinned fingerprint MUST be ignored.

## 4. Transport

* TLS 1.2 or 1.3; TLS 1.2 limited to ECDHE + AEAD suites. ALPN `http/1.1`.
* The server requests and **requires** a client certificate. It does not validate chains; it
  authorizes by fingerprint per endpoint.
* The client MUST verify that the server certificate's fingerprint equals the pinned value
  (from the pairing invitation, or the registry after pairing).

### Endpoints

| Method/path      | Who may call                     | Purpose                         |
|------------------|----------------------------------|---------------------------------|
| `POST /v1/pair`  | any certificate, only while a pairing window is open | pairing request (§5) |
| `GET /v1/ws`     | paired certificates only (401 otherwise) | WebSocket session (§6) |

Errors use HTTP status codes with a JSON body `{"error":{"code":"…","message":"…"}}`
([`http-error.schema.json`](schemas/v1/http-error.schema.json)). Codes: `bad_request`,
`unauthorized`, `unsupported_version`, `pairing_closed`, `invalid_code`, `rate_limited`,
`too_large`, `internal`, `identity_mismatch`, `duplicate_connection`.

## 5. Pairing

1. The user starts pairing on a listening device. It opens a **pairing window** (default 2 min)
   and generates a one-time **pairing code**: 8 characters from `23456789ABCDEFGHJKLMNPQRSTUVWXYZ`
   (40 bits), displayed as `XXXX-XXXX`.
2. The device shows an **invitation URI**, as text and as a QR code:

   ```
   uclip://pair?v=1&id=<device_id>&name=<name>&os=<platform>&fp=<fingerprint>&addr=<host:port>[&addr=…]&code=<code>
   ```

   All values are URL-encoded; `addr` repeats (1–16 entries, IPv6 as `[addr]:port`).
3. The requester connects to one of the addresses over TLS, presenting its own certificate and
   **pinning `fp`** from the invitation, and sends `POST /v1/pair` with a
   [`PairRequest`](schemas/v1/pair-request.schema.json):

   ```json
   {"protocol_version":1,"device":{"id":"…","name":"Pixel 8","platform":"android"},"code":"K7M2-Q9XR","listen_port":0}
   ```

   `listen_port` is the requester's own listener port, or `0` for client-only devices. Codes are
   compared case-insensitively ignoring `-` and spaces.
4. The listening device checks: window open, code correct (each wrong code consumes one of
   `max_attempts`, default 5; the window closes when exhausted), not pairing with itself, and
   at most one request pending. It then holds the request open and asks its user to **approve**.
5. Both devices display the **verification code** (SAS) and the user confirms they match:

   ```
   n   = first 4 bytes (big-endian uint32) of SHA-256("uclip-sas-v1:" + min(fpA,fpB) + ":" + max(fpA,fpB)) mod 1,000,000
   SAS = n as 6 decimal digits, shown as "123 456"
   ```

   When the invitation came from a QR code the server fingerprint is already pinned, so the SAS is
   a second check. When the user typed the code (fingerprint not pinned out-of-band) the SAS is
   what defeats a man in the middle: an attacker cannot make both sides compute the same value.
6. The response is a [`PairResponse`](schemas/v1/pair-response.schema.json) with `status`
   `approved`, `rejected` or `expired`. On approval both sides store the peer's device info and
   fingerprint; the listening device also stores `remote_ip:listen_port` when `listen_port > 0`.
   The window closes after one successful pairing.

Requests are rate limited per source IP. Unpairing (revocation) is local: the device deletes the
fingerprint and closes any open session; the peer's next connection receives `401`.

## 6. Sessions

### 6.1 Establishment

The client opens `wss://host:port/v1/ws` with WebSocket subprotocol **`uclip.v1`** (required).
Each side immediately sends a `hello` envelope and reads the peer's `hello` within 10 s.

All WebSocket messages are UTF-8 JSON text frames wrapped in an
[envelope](schemas/v1/envelope.schema.json):

```json
{"type":"<message type>","protocol_version":1,"data":{…}}
```

Receivers MUST ignore unknown `type`s and unknown fields (forward compatibility). Binary frames are
not used in v1.

### 6.2 `hello`

```json
{"device":{…},"min_protocol_version":1,"max_protocol_version":1,"time":1793730000000,
 "capabilities":["clipboard.send","clipboard.receive"],"max_payload_bytes":1048576,"listen_port":47823}
```

* `device.id` MUST equal the device ID stored for the certificate at pairing; otherwise the
  session is closed with an `error` (`identity_mismatch`).
* The session uses the highest common version; none ⇒ `unsupported_version`.
* `time` (sender's Unix ms) lets the receiver estimate the clock offset for the connection.
* Senders only push `clipboard.event`s to peers advertising `clipboard.receive`, and never
  larger than the peer's `max_payload_bytes`.
* `listen_port` lets a listening device learn a peer's current address from inbound connections.

### 6.3 Messages

| type              | data schema                                               |
|-------------------|-----------------------------------------------------------|
| `hello`           | [`hello.schema.json`](schemas/v1/hello.schema.json)       |
| `clipboard.event` | [`clipboard-event.schema.json`](schemas/v1/clipboard-event.schema.json) |
| `clipboard.ack`   | [`ack.schema.json`](schemas/v1/ack.schema.json)           |
| `error`           | [`error.schema.json`](schemas/v1/error.schema.json)       |

**Clipboard event**

```json
{
  "event_id": "6f1c2a7e-9b3d-4e2f-8a61-0c5d9e7b4a10",
  "origin_device_id": "3b8f6a52-1c4e-4d7a-9e0b-7f2a5c6d8e91",
  "content_type": "text",
  "timestamp": 1793730000000,
  "protocol_version": 1,
  "payload": {"text": "Hello from Android", "mime_type": "text/plain", "size": 18,
              "sha256": "08874a21993ffe9aadc64d5fc93e62711d60c05f5792b74c693ef2ccb00188e7"}
}
```

* `timestamp` is in **Unix milliseconds** (millisecond precision is needed for ordering).
* `content_type` is `url` iff the trimmed text is a single absolute `http`/`https` URL, otherwise
  `text`. Receivers reject `url` events whose text is not such a URL.
* `payload.size` MUST equal the UTF-8 byte length of `text`, and `sha256` its digest.
* `text` MUST be non-empty valid UTF-8, at most the receiver's `max_payload_bytes`
  (absolute maximum 16 MiB).
* Every event MUST be acknowledged with exactly one `clipboard.ack`:

| status      | meaning |
|-------------|---------|
| `applied`   | written to the receiver's clipboard (or stored, on a headless receiver) |
| `duplicate` | `event_id` already processed |
| `stale`     | the receiver already holds newer clipboard content |
| `expired`   | older than the receiver's maximum event age |
| `ignored`   | receiver policy: paused, receiving disabled, excluded content type, or the event originated at the receiver |
| `rejected`  | invalid, too large (`too_large`), not permitted (`not_permitted`), rate limited (`rate_limited`), clipboard unavailable |

### 6.4 Duplicate connections

Two listening devices may connect to each other simultaneously. Each side keeps the session
**initiated by the device with the lexicographically smaller `device_id`** and closes the other
with WebSocket close code **4001**. A new session from the same initiator replaces the old one.

### 6.5 Liveness, limits and close codes

* WebSocket ping every 30 s; no pong within ~16 s closes the session.
* Message size limit: `6 × max_payload_bytes + 16 KiB` (JSON escaping); larger frames close the
  connection with 1009.
* Events are rate limited per session (default 5/s, burst 20) — excess events are acknowledged
  `rejected`/`rate_limited`. A flood of any messages closes with **4029**. Twenty invalid
  messages close with 1002.
* Close codes: 1001 shutdown/ping timeout, 1008 policy (handshake failure, revoked), 4001
  duplicate connection, 4029 rate limit.

## 7. Synchronization semantics

The clipboard is treated as a **last-writer-wins register**. There is no queue of offline events.

1. **Deduplication** — receivers remember processed `event_id`s for at least 10 minutes and
   acknowledge repeats as `duplicate`.
2. **Origin tracking / loop prevention** — events whose `origin_device_id` is the receiver are
   `ignored`. Events are never sent back to their origin or to the peer they came from.
3. **Echo suppression** — after writing a received event to the local clipboard, a device MUST NOT
   treat the resulting clipboard change as a new local copy. The daemon remembers the SHA-256 of
   the content it last saw or wrote and ignores clipboard notifications for identical content.
4. **Expiry** — after clock-offset correction (`timestamp - offset` for events whose origin is
   the directly connected peer), events older than `max_event_age` (default 2 min) are `expired`;
   events more than 5 min in the future are `rejected`.
5. **Ordering** — an event older than the receiver's latest clipboard state by more than
   `ordering_tolerance` (default 2 s) is `stale` and MUST NOT overwrite the clipboard.
6. **Reconnection** — no replay of history. When a peer connects, the sender MAY offer its single
   current clipboard event if it is younger than `max_event_age` and that peer has not received it.
7. **Relay** (optional, off by default) — a device MAY forward events it applied to its other
   peers, unchanged (same `event_id` and `origin_device_id`). Rules 1–2 bound propagation.
8. **Manual sends** (share sheet, in-app send, CLI `send`) are ordinary events. They are sent to
   the selected devices, work while automatic synchronization is paused, and do not change the
   sender's own clipboard.

## 8. Versioning

* `protocol_version` appears in envelopes, events and pairing messages.
* Additive changes (new optional fields, new message types, new capabilities) do not change the
  version; receivers ignore what they do not understand.
* Incompatible changes increase the version. Implementations advertise a supported range in
  `hello` and pairing requests carry their version (`unsupported_version` on mismatch).

## 9. Planned extensions (not part of v1)

* **Images and files** (Phase 8): events will carry metadata only (`content_type`
  `image`/`file`, size, digest, a transfer id); the bytes are streamed over a separate
  authenticated HTTPS request (`GET /v1/transfers/{id}`) rather than inside WebSocket JSON, with
  size limits and digest verification.
* **Rich text**: `text/html` alongside a plain-text fallback.
