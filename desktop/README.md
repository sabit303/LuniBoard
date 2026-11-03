# Desktop GUI (not implemented)

A desktop GUI is optional and planned for Phase 9. The architecture already supports it:

* The daemon runs without any GUI.
* A GUI must be a client of the documented [local API](../docs/local-api.md) (HTTP/JSON over a
  per-user Unix socket). Everything it needs is available there: status, devices and connection
  state, pairing (invitation URI for a QR code, pending requests with verification codes,
  approve/reject), per-device permissions, pause/resume, manual send, history and configuration.
* The GUI must not contain synchronization logic of its own.

Toolkit choice (e.g. a tray app in Go, GTK, Qt, or Tauri) is open.
