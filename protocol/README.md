# Protocol

* [`protocol.md`](protocol.md) — the versioned specification (v1).
* [`schemas/v1`](schemas/v1) — JSON Schemas (draft 2020-12) for every message.
* [`messages`](messages) — example messages:
  * [`clipboard-event.json`](messages/clipboard-event.json) — `clipboard.event` envelope
  * [`acknowledgement.json`](messages/acknowledgement.json) — `clipboard.ack` envelope
  * [`hello.json`](messages/hello.json) — `hello` envelope from a mobile client
  * [`pairing.json`](messages/pairing.json) — `POST /v1/pair` request and response
  * [`device.json`](messages/device.json) — device info, pairing URI and DNS-SD TXT record

`daemon/pkg/protocol` is the reference Go implementation. Its tests validate Go-encoded messages
and all examples against the schemas, so a change to one without the others fails CI.
