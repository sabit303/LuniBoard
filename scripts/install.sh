#!/bin/sh
# LuniBoard installer for Linux.
#
# Installs the latest released daemon for the current user, adds it to PATH,
# starts it at login and (if a firewall is active) offers to open its port for
# the local network. Run it again to update; run with --uninstall to remove.
#
#   curl -fsSL https://raw.githubusercontent.com/sabit303/LuniBoard/main/scripts/install.sh | sh
#   sh install.sh [--version 0.1.0] [--uninstall] [--no-service] [--yes]
#
# Environment overrides (mainly for testing):
#   LUNIBOARD_INSTALL_DIR  where the binary goes (default ~/.local/bin)
#   LUNIBOARD_REPO         GitHub owner/repo     (default sabit303/LuniBoard)
#   LUNIBOARD_API_URL      releases API URL      (default https://api.github.com/repos/$REPO/releases)

set -eu

REPO=${LUNIBOARD_REPO:-sabit303/LuniBoard}
API_URL=${LUNIBOARD_API_URL:-https://api.github.com/repos/$REPO/releases}
INSTALL_DIR=${LUNIBOARD_INSTALL_DIR:-$HOME/.local/bin}
VERSION=""
UNINSTALL=0
SERVICE=1
ASSUME_YES=0

# ---------------------------------------------------------------- output ----

if [ -t 1 ]; then
	BOLD=$(printf '\033[1m'); GREEN=$(printf '\033[32m'); YELLOW=$(printf '\033[33m'); RED=$(printf '\033[31m'); RESET=$(printf '\033[0m')
else
	BOLD=""; GREEN=""; YELLOW=""; RED=""; RESET=""
fi
info() { printf '%s==>%s %s\n' "$BOLD$GREEN" "$RESET" "$*"; }
warn() { printf '%s==> Warning:%s %s\n' "$BOLD$YELLOW" "$RESET" "$*" >&2; }
die() { printf '%s==> Error:%s %s\n' "$BOLD$RED" "$RESET" "$*" >&2; exit 1; }

# The script is often piped into sh, so stdin is not the terminal: ask
# questions through /dev/tty when one exists.
have_tty() { (: </dev/tty) 2>/dev/null; }

confirm() { # confirm "question" -> 0 for yes (default yes)
	[ "$ASSUME_YES" = 1 ] && return 0
	have_tty || return 1
	printf '%s [Y/n]: ' "$1" >/dev/tty
	read -r answer </dev/tty || return 1
	case $answer in "" | y | Y | yes | YES) return 0 ;; *) return 1 ;; esac
}

usage() {
	cat <<'EOF'
LuniBoard installer for Linux.

Usage: install.sh [--version X.Y.Z] [--uninstall] [--no-service] [--yes]

  (no options)   install or update to the latest release for this user
  --version V    install a specific version
  --uninstall    stop LuniBoard, remove its firewall rules and the program
  --no-service   only install the program, do not start it at login
  --yes          answer yes to all questions
EOF
	exit 0
}

while [ $# -gt 0 ]; do
	case $1 in
	--version) [ $# -ge 2 ] || die "--version needs a value"; VERSION=${2#v}; shift 2 ;;
	--version=*) VERSION=${1#--version=}; VERSION=${VERSION#v}; shift ;;
	--uninstall) UNINSTALL=1; shift ;;
	--no-service) SERVICE=0; shift ;;
	-y | --yes) ASSUME_YES=1; shift ;;
	-h | --help) usage ;;
	*) die "unknown option: $1 (see --help)" ;;
	esac
done

[ "$(id -u)" -ne 0 ] || die "run this as your normal user, not root (it installs into your home directory and asks for sudo only when needed)"
[ "$(uname -s)" = Linux ] || die "this installer supports Linux only"

BIN=$INSTALL_DIR/luniboard

# ------------------------------------------------------------- uninstall ----

if [ "$UNINSTALL" = 1 ]; then
	if [ -x "$BIN" ]; then
		info "Removing firewall rules added by LuniBoard (may ask for your password)"
		if have_tty; then "$BIN" firewall remove </dev/tty || warn "could not remove firewall rules"; else "$BIN" firewall remove || true; fi
		info "Stopping and removing the background service"
		"$BIN" service uninstall || true
		rm -f "$BIN"
		info "Removed $BIN"
	else
		warn "LuniBoard is not installed in $INSTALL_DIR"
	fi
	echo "Your settings and pairings are kept in ~/.config/luniboard and ~/.local/share/luniboard."
	echo "Delete them with: rm -r ~/.config/luniboard ~/.local/share/luniboard ~/.local/state/luniboard"
	exit 0
fi

# ------------------------------------------------------------ downloader ----

if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL --retry 3 -o "$2" "$1"; }
	fetch_stdout() { curl -fsSL --retry 3 -H 'Accept: application/vnd.github+json' "$1"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -q -O "$2" "$1"; }
	fetch_stdout() { wget -q -O - --header='Accept: application/vnd.github+json' "$1"; }
else
	die "curl or wget is required"
fi
command -v tar >/dev/null 2>&1 || die "tar is required"

if command -v sha256sum >/dev/null 2>&1; then
	sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
	sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
	die "sha256sum is required to verify the download"
fi

case $(uname -m) in
x86_64 | amd64) ARCH=amd64 ;;
aarch64 | arm64) ARCH=arm64 ;;
*) die "unsupported CPU architecture: $(uname -m) (supported: x86_64, aarch64)" ;;
esac

# -------------------------------------------------------- find a release ----

info "Looking up the latest LuniBoard release"
releases=$(fetch_stdout "$API_URL?per_page=20") || die "could not reach GitHub ($API_URL)"

# Every asset URL, newest release first (the API orders releases by date).
# Pre-releases are included because early versions are published as such.
urls=$(printf '%s\n' "$releases" | grep -o '"browser_download_url"[[:space:]]*:[[:space:]]*"[^"]*"' | sed 's/.*"\([^"]*\)"$/\1/' || true)

if [ -n "$VERSION" ]; then
	tar_url=$(printf '%s\n' "$urls" | grep "/luniboard-$VERSION-linux-$ARCH\.tar\.gz\$" | head -n 1 || true)
	[ -n "$tar_url" ] || die "version $VERSION has no download for linux-$ARCH"
else
	tar_url=$(printf '%s\n' "$urls" | grep "/luniboard-[^/]*-linux-$ARCH\.tar\.gz\$" | head -n 1 || true)
	[ -n "$tar_url" ] || die "no release with a linux-$ARCH download found at https://github.com/$REPO/releases"
	VERSION=$(printf '%s\n' "$tar_url" | sed "s#.*/luniboard-\(.*\)-linux-$ARCH\.tar\.gz\$#\1#")
fi
sums_url=$(printf '%s\n' "$urls" | grep "/luniboard-$VERSION-SHA256SUMS\$" | head -n 1 || true)

if [ -x "$BIN" ]; then
	current=$("$BIN" version 2>/dev/null | awk '{print $2}' || true)
	if [ "$current" = "$VERSION" ]; then
		info "LuniBoard $VERSION is already installed; reinstalling"
	else
		info "Updating LuniBoard ${current:-?} -> $VERSION"
	fi
else
	info "Installing LuniBoard $VERSION ($ARCH)"
fi

# ------------------------------------------------- download and verify ----

tmp=$(mktemp -d 2>/dev/null || mktemp -d -t luniboard)
trap 'rm -rf "$tmp"' EXIT
trap 'exit 130' INT TERM
archive=$tmp/luniboard.tar.gz

fetch "$tar_url" "$archive" || die "download failed: $tar_url"

if [ -n "$sums_url" ]; then
	fetch "$sums_url" "$tmp/SHA256SUMS" || die "download failed: $sums_url"
	expected=$(grep " luniboard-$VERSION-linux-$ARCH\.tar\.gz\$" "$tmp/SHA256SUMS" | cut -d' ' -f1 || true)
	[ -n "$expected" ] || die "checksum for luniboard-$VERSION-linux-$ARCH.tar.gz missing from SHA256SUMS"
	actual=$(sha256 "$archive")
	[ "$expected" = "$actual" ] || die "checksum mismatch (expected $expected, got $actual); not installing"
	info "Checksum verified"
else
	warn "this release has no SHA256SUMS file; the download could not be verified"
	confirm "Install anyway?" || die "aborted"
fi

tar -xzf "$archive" -C "$tmp" || die "could not unpack the archive"
src=$tmp/luniboard-$VERSION-linux-$ARCH/luniboard
[ -f "$src" ] || die "unexpected archive layout (missing $src)"

# ---------------------------------------------------------------- install ----

mkdir -p "$INSTALL_DIR"
# Replace via rename so a running daemon keeps its old binary until restarted.
cp "$src" "$INSTALL_DIR/.luniboard.new"
chmod 755 "$INSTALL_DIR/.luniboard.new"
mv -f "$INSTALL_DIR/.luniboard.new" "$BIN"
info "Installed $BIN ($("$BIN" version))"

# -------------------------------------------------------------- shell PATH ----

case ":$PATH:" in
*":$INSTALL_DIR:"*) ;;
*)
	case ${SHELL:-} in
	*/bash | bash) shell_rc=$HOME/.bashrc ;;
	*/zsh | zsh) shell_rc=${ZDOTDIR:-$HOME}/.zshrc ;;
	*) shell_rc=$HOME/.profile ;;
	esac
	# Keep custom directory names literal while expanding PATH when the shell starts.
	escaped_install_dir=$(printf '%s\n' "$INSTALL_DIR" | sed 's/[\\$`"]/\\&/g')
	path_line="case \":\$PATH:\" in *:\"$escaped_install_dir\":*) ;; *) export PATH=\"$escaped_install_dir:\$PATH\" ;; esac"
	if grep -Fqx "$path_line" "$shell_rc" 2>/dev/null ||
		(mkdir -p "$(dirname "$shell_rc")" &&
			printf '\n# Added by the LuniBoard installer\n%s\n' "$path_line" >> "$shell_rc"); then
		info "PATH configured in $shell_rc. Open a new terminal to use luniboard."
	else
		warn "could not update $shell_rc. Add this line manually:
      export PATH=\"$escaped_install_dir:\$PATH\""
	fi
	;;
esac

# ------------------------------------------------------- clipboard tools ----

missing=""
if [ -n "${WAYLAND_DISPLAY:-}" ] || [ "${XDG_SESSION_TYPE:-}" = wayland ]; then
	if ! command -v wl-copy >/dev/null 2>&1 || ! command -v wl-paste >/dev/null 2>&1; then
		missing="$missing wl-clipboard"
	fi
fi
if [ -n "${DISPLAY:-}" ] && ! command -v xclip >/dev/null 2>&1 && ! command -v xsel >/dev/null 2>&1; then
	missing="$missing xclip"
fi
if [ -z "${WAYLAND_DISPLAY:-}${DISPLAY:-}" ] && ! command -v wl-copy >/dev/null 2>&1 && ! command -v xclip >/dev/null 2>&1; then
	missing=" wl-clipboard xclip" # not run from a desktop session: install both to be safe
fi

if [ -n "$missing" ]; then
	install_cmd=""
	if command -v pacman >/dev/null 2>&1; then install_cmd="sudo pacman -S --needed --noconfirm$missing"
	elif command -v apt-get >/dev/null 2>&1; then install_cmd="sudo apt-get install -y$missing"
	elif command -v dnf >/dev/null 2>&1; then install_cmd="sudo dnf install -y$missing"
	elif command -v zypper >/dev/null 2>&1; then install_cmd="sudo zypper install -y$missing"
	fi
	warn "LuniBoard needs$missing to access the clipboard."
	if [ -n "$install_cmd" ] && have_tty && confirm "Install it now with '$install_cmd'?"; then
		# shellcheck disable=SC2086
		$install_cmd </dev/tty || warn "installation failed; install$missing yourself"
	else
		warn "install$missing with your package manager, then run: systemctl --user restart luniboard"
	fi
fi

# --------------------------------------------------------- background run ----

if [ "$SERVICE" = 1 ]; then
	if command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then
		info "Starting LuniBoard now and at every login"
		# Interactive so it can offer to open the firewall (asks for your password).
		if have_tty; then "$BIN" service install </dev/tty; else "$BIN" service install; fi
	else
		warn "systemd user services are not available; starting LuniBoard for this session only"
		"$BIN" start || warn "could not start the daemon; run: luniboard daemon"
		echo "Add 'luniboard start' to your desktop's autostart to run it at login."
		"$BIN" firewall status || true
	fi
fi

echo
info "Done! Next, connect your phone:"
echo "      luniboard pair"
echo "    Other commands: luniboard status | luniboard devices | luniboard --help"
echo "    Update: run this installer again.  Remove: sh install.sh --uninstall"
