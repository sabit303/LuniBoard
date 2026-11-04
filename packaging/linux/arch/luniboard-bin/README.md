# luniboard-bin (AUR)

Installs the prebuilt LuniBoard daemon/CLI from the GitHub release `v$pkgver`.

## Publishing a new version

1. Tag and push: `git tag v0.1.0 && git push origin v0.1.0`. The `Release` workflow builds
   `luniboard-<ver>-linux-{amd64,arm64}.tar.gz` and attaches them to the GitHub release.
   (Locally: `make release VERSION=0.1.0`.)
2. Update `pkgver` here, then replace the `SKIP` checksums: `updpkgsums`.
3. Test: `makepkg -si` in this directory.
4. Regenerate metadata: `makepkg --printsrcinfo > .SRCINFO`.
5. Push `PKGBUILD`, `luniboard.install` and `.SRCINFO` to `ssh://aur@aur.archlinux.org/luniboard-bin.git`.

## Firewall

`post_install` runs `luniboard firewall allow` as root: if ufw or firewalld is active it opens the
LuniBoard port (and UDP 5353) for the machine's current private subnet(s) only, with rules tagged
`LuniBoard`. `pre_remove` runs `luniboard firewall remove`, which deletes only those tagged rules.
Nothing happens when no firewall is active.
