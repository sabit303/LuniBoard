# Security policy

Universal Clipboard handles clipboard content, which often includes passwords and other secrets.
We take vulnerabilities seriously.

## Reporting a vulnerability

Please **do not open a public issue**. Report privately through the repository's
"Report a vulnerability" (GitHub Security Advisories) feature. Include affected versions, a
description, reproduction steps and the impact you expect. We aim to acknowledge reports within
7 days and to agree on a disclosure date with you.

## Scope

Particularly relevant: authentication or pairing bypasses, ways for an unpaired or revoked device
to read or write clipboard content, man-in-the-middle attacks, denial of service reachable from the
LAN, local privilege issues with the control socket, and leaks of clipboard content to logs or disk.

The threat model, controls and known limitations are documented in
[docs/security.md](docs/security.md).

## Supported versions

The project is pre-1.0; only the latest commit on the default branch receives fixes.
