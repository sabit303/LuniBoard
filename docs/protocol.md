# Protocol

The protocol specification is maintained next to its schemas and examples:
**[protocol/protocol.md](../protocol/protocol.md)**.

Quick reference:

* Discovery: DNS-SD `_luniboard._tcp`, TXT `id`, `v`, `fp`, `os`, `name` (hints only).
* Transport: HTTPS with mutual TLS; trust = pinned SHA-256 certificate fingerprints.
* Pairing: `POST /v1/pair` during a user-opened window, one-time code, explicit approval,
  6-digit verification code derived from both fingerprints.
* Sessions: `wss://…/v1/ws`, subprotocol `luniboard.v1`, JSON envelopes `hello`, `clipboard.event`,
  `clipboard.ack`, `error`.
* Semantics: last-writer-wins, dedupe by `event_id`, origin/echo loop prevention, 2-minute expiry,
  no offline replay.
