#!/bin/sh
# Install vkstack from a GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/warroyo/vkstack/main/install.sh | sh
#
# Pin a version, or choose where it lands:
#
#   VKSTACK_VERSION=v0.1.0 sh install.sh
#   VKSTACK_INSTALL_DIR=$HOME/bin sh install.sh
#
# POSIX sh on purpose: this gets piped to /bin/sh, which is dash on Debian and Ubuntu,
# so no bashisms. Every download is checksum-verified against the release's own
# checksums.txt before anything is copied onto your PATH.
set -eu

REPO="warroyo/vkstack"
BINARY="vkstack"

say() { printf '%s\n' "$*"; }
die() { printf 'install: %s\n' "$*" >&2; exit 1; }

need() {
	command -v "$1" >/dev/null 2>&1 || die "this script needs $1 on PATH"
}

need uname
need tar
if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL "$1" -o "$2"; }
	fetch_stdout() { curl -fsSL "$1"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -qO "$2" "$1"; }
	fetch_stdout() { wget -qO- "$1"; }
else
	die "this script needs curl or wget"
fi

# --- what are we running on ------------------------------------------------

os=$(uname -s)
case "$os" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	MINGW* | MSYS* | CYGWIN*)
		die "Windows is not covered by this script — download the .zip from https://github.com/$REPO/releases"
		;;
	*) die "unsupported operating system: $os" ;;
esac

arch=$(uname -m)
case "$arch" in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) die "unsupported architecture: $arch (releases cover amd64 and arm64)" ;;
esac

# --- which version --------------------------------------------------------

version="${VKSTACK_VERSION:-${1:-}}"
if [ -z "$version" ]; then
	# Resolve the latest tag without needing jq: the redirect target of /releases/latest
	# ends in the tag. Falling back to the API keeps this working if that ever changes.
	version=$(fetch_stdout "https://api.github.com/repos/$REPO/releases/latest" |
		sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
	[ -n "$version" ] || die "could not determine the latest version — pass one, e.g. VKSTACK_VERSION=v0.1.0"
fi
# Archives are named without the leading v.
bare=$(printf '%s' "$version" | sed 's/^v//')

archive="${BINARY}_${bare}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$version"

# --- where does it go -----------------------------------------------------
#
# Prefer a directory this user already owns. Installing to /usr/local/bin would need
# sudo, and a script piped from the internet should not be reaching for root on its own.
if [ -n "${VKSTACK_INSTALL_DIR:-}" ]; then
	dir="$VKSTACK_INSTALL_DIR"
elif [ -w /usr/local/bin ] && [ -d /usr/local/bin ]; then
	dir=/usr/local/bin
else
	dir="$HOME/.local/bin"
fi
mkdir -p "$dir" || die "cannot create $dir"
[ -w "$dir" ] || die "$dir is not writable — set VKSTACK_INSTALL_DIR to somewhere you own"

# --- download, verify, install --------------------------------------------

tmp=$(mktemp -d)
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT INT TERM

say "vkstack $version ($os/$arch)"
say "  downloading $archive"
fetch "$base/$archive" "$tmp/$archive" || die "download failed: $base/$archive"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || die "could not fetch checksums.txt"

# A checksum that is merely present proves nothing; the archive has to appear in the
# release's own list and match it.
expected=$(sed -n "s/^\([0-9a-f]\{64\}\)  *$archive\$/\1/p" "$tmp/checksums.txt" | head -n 1)
[ -n "$expected" ] || die "$archive is not listed in checksums.txt for $version"

if command -v sha256sum >/dev/null 2>&1; then
	actual=$(sha256sum "$tmp/$archive" | cut -d' ' -f1)
elif command -v shasum >/dev/null 2>&1; then
	actual=$(shasum -a 256 "$tmp/$archive" | cut -d' ' -f1)
else
	die "this script needs sha256sum or shasum to verify the download"
fi
[ "$actual" = "$expected" ] || die "checksum mismatch for $archive
  expected $expected
  got      $actual"
say "  checksum ok"

tar -xzf "$tmp/$archive" -C "$tmp" || die "could not extract $archive"
[ -f "$tmp/$BINARY" ] || die "$BINARY not found inside the archive"
chmod +x "$tmp/$BINARY"

# Move into place via a temporary name in the same directory, so a running copy is
# replaced atomically rather than being truncated under itself.
mv "$tmp/$BINARY" "$dir/$BINARY.new" || die "could not write to $dir"
mv "$dir/$BINARY.new" "$dir/$BINARY"

say "  installed $dir/$BINARY"

# --- what now -------------------------------------------------------------

case ":$PATH:" in
	*":$dir:"*) ;;
	*)
		say ""
		say "note: $dir is not on your PATH. Add it:"
		say "  export PATH=\"$dir:\$PATH\""
		;;
esac

say ""
say "next:"
say "  $BINARY refresh          # populate the local cache once (~1 min, ~40MB)"
say "  $BINARY serve --open     # the stack map, for people"
say "  $BINARY describe         # the agent-facing contract, as JSON"
