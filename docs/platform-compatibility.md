# Platform compatibility

This page distinguishes what is **implemented and verified**, **implemented but not yet verified
on that platform**, and **not implemented**. It is updated with every phase.

## Desktop daemon

| Platform / session | Clipboard backend | Change detection | Status |
|--------------------|-------------------|------------------|--------|
| Arch Linux, Hyprland (Wayland) | wl-clipboard | `wl-paste --watch` (data-control) | **Verified** manually: real clipboard both directions, no loops, mDNS discovery, CLI pairing |
| Arch Linux, XWayland (`clipboard.backend = "x11"`) | xclip | polling | **Verified** manually (real xclip read/write and polling) |
| Sway, KDE Plasma (Wayland) | wl-clipboard | `wl-paste --watch` (wlr/ext data-control supported by these compositors) | Implemented, not yet verified |
| GNOME (Wayland) | wl-clipboard | Mutter lacks data-control, so watching fails and the daemon **polls**; it polls through XWayland when that mirrors the Wayland clipboard (checked at runtime), else with `wl-paste` | Implemented, **not verified** — `wl-paste` on Mutter may briefly take focus while polling |
| X11 (GNOME on Xorg, XFCE, i3, …) | xclip / xsel | polling (`clipboard.poll_interval`) | Implemented; adapter verified via XWayland; not verified on a native Xorg session |
| Ubuntu, Fedora, Debian | same as above | same | Distribution-independent code; not yet verified on these distributions |
| Headless Linux | `none` | — | Implemented and covered by automated end-to-end tests |
| Windows | — | — | **Not implemented** (Phase 6). The daemon compiles and runs headless. |
| macOS | — | — | **Not implemented** (Phase 6). The daemon compiles and runs headless. |

Service integration: systemd user service (`luniboard service install`) — implemented;
the generated unit passes `systemd-analyze --user verify` in the test suite, but `service install`
itself has not yet been exercised on a real login session. Windows startup and macOS LaunchAgent
are Phase 6.

## Mobile

| Platform | Status |
|----------|--------|
| Android  | **Not implemented yet** (Phase 3). The daemon side it needs — QR pairing, client-only peers, share-style manual events, receiving — is implemented and exercised by an automated test client (`daemon/tests/integration/mobile_client_test.go`). |
| iOS      | **Not implemented** (Phase 7). |

## Mobile OS restrictions (design constraints for Phases 3 and 7)

**Android**

* Since Android 10, only the default input method or the app in the foreground can **read** the
  clipboard. A normal background app cannot observe clipboard changes. We will not use
  Accessibility Services, root, or other workarounds.
* Outgoing content from Android therefore comes from explicit user actions: the **share sheet**
  (`ACTION_SEND` with `text/plain`), the in-app text field / paste button, and a notification or
  Quick Settings action that brings the app to the foreground to read the clipboard once.
* **Writing** the clipboard (`ClipboardManager.setPrimaryClip`) is allowed from a foreground
  service, so Linux → Android works while the app keeps a connection in a foreground service of
  type `connectedDevice` with a visible notification that the user can stop. Android 12+ shows a
  system toast when the clipboard is accessed; Android 13+ shows a clipboard preview.
* Background network access is limited by Doze; without the foreground service, content is only
  received while the app is open.

**iOS**

* Apps can read `UIPasteboard` only in the foreground; iOS 16+ asks the user for permission
  (unless the paste comes from a system paste control). There is no background clipboard
  monitoring and long-lived background sockets are not allowed.
* Outgoing: Share Extension and in-app send. Incoming: while the app is active (it connects on
  launch/foreground); delivery while suspended is not possible without a push service, which the
  no-cloud requirement excludes.
