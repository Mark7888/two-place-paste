#!/bin/sh
# Install or update TwoPlacePaste on a Mac, without the Gatekeeper detour.
#
#   curl -fsSL https://github.com/Mark7888/two-place-paste/releases/latest/download/install-macos.sh | sh
#
# A browser marks what it downloads as quarantined, and macOS then refuses to
# open an app that is not notarised until it is approved in System Settings →
# Privacy & Security. curl sets no quarantine flag, so an app this script
# downloads and installs opens straight away
# (docs/plans/versioning-releases-and-updates.md §3.5).
#
# Options, from the environment:
#   TPP_VERSION=0.1.0   install that release rather than the latest one
#   TPP_CHANNEL=beta    install the latest Beta build instead (the default
#                       branch's newest build)
#
# The download is checked against the checksum in the build's manifest. That
# protects against a truncated or corrupted download; the manifest's signature
# is checked by the app's own updater, which carries the public key.
#
# POSIX sh, not bash: `curl … | sh` runs whatever /bin/sh is.

set -eu

REPO=Mark7888/two-place-paste
APP_NAME=TwoPlacePaste.app
BUNDLE_ID=com.twoplacepaste.desktop

say() { printf '%s\n' "$*"; }
die() {
	printf 'install-macos.sh: %s\n' "$*" >&2
	exit 1
}

[ "$(uname -s)" = Darwin ] || die "this installer is for macOS"

# --- which build --------------------------------------------------------------
version=${TPP_VERSION:-}
channel=${TPP_CHANNEL:-stable}
if [ -n "$version" ]; then
	base="https://github.com/$REPO/releases/download/v${version#v}"
elif [ "$channel" = beta ]; then
	base="https://github.com/$REPO/releases/download/channel-beta"
elif [ "$channel" = stable ]; then
	base="https://github.com/$REPO/releases/latest/download"
else
	die "TPP_CHANNEL must be stable or beta, not '$channel'"
fi

# An Intel shell running under Rosetta reports x86_64 on Apple Silicon; the
# native build is the one to install there.
arch=$(uname -m)
if [ "$arch" = x86_64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
	arch=arm64
fi
case $arch in
arm64) asset=TwoPlacePaste-macOS-arm64.zip ;;
x86_64) asset=TwoPlacePaste-macOS-x64.zip ;;
*) die "unsupported architecture '$arch'" ;;
esac

# --- download and check ----------------------------------------------------------
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

fetch() {
	curl -fsSL --proto '=https' --tlsv1.2 -o "$2" "$1" || die "could not download $1"
}

say "Downloading $asset…"
fetch "$base/$asset" "$tmp/$asset"
fetch "$base/manifest-desktop.json" "$tmp/manifest.json"

# plutil reads JSON and, since macOS 12, prints a value with `raw`.
want=
i=0
while name=$(plutil -extract "files.$i.name" raw -o - "$tmp/manifest.json" 2>/dev/null); do
	if [ "$name" = "$asset" ]; then
		want=$(plutil -extract "files.$i.sha256" raw -o - "$tmp/manifest.json")
		break
	fi
	i=$((i + 1))
done
[ -n "$want" ] || die "$asset is not listed in the manifest"
got=$(shasum -a 256 "$tmp/$asset" | cut -d' ' -f1)
[ "$got" = "$want" ] || die "checksum mismatch for $asset: got $got, want $want"

ditto -x -k "$tmp/$asset" "$tmp/unpacked"
new="$tmp/unpacked/$APP_NAME"
[ -d "$new" ] || die "$asset does not contain $APP_NAME"
id=$(/usr/libexec/PlistBuddy -c "Print :CFBundleIdentifier" "$new/Contents/Info.plist")
[ "$id" = "$BUNDLE_ID" ] || die "$asset contains '$id', not $BUNDLE_ID"
new_version=$(/usr/libexec/PlistBuddy -c "Print :CFBundleShortVersionString" "$new/Contents/Info.plist")

# --- install -----------------------------------------------------------------------
# /Applications when it is writable without a password, ~/Applications otherwise.
dest=/Applications
if [ ! -w "$dest" ]; then
	dest="$HOME/Applications"
	mkdir -p "$dest"
fi
target="$dest/$APP_NAME"

# The service holds the localhost port; quit it so the new one can bind.
# SIGTERM is a clean shutdown for it.
if pkill -x tppdesktop 2>/dev/null; then
	say "Quitting the running TwoPlacePaste…"
	n=0
	while pgrep -x tppdesktop >/dev/null && [ $n -lt 50 ]; do
		sleep 0.2
		n=$((n + 1))
	done
fi

# Replace the bundle as a whole: never write into an existing one.
if [ -e "$target" ]; then
	mv "$target" "$tmp/previous.app"
fi
if ! ditto "$new" "$target"; then
	[ -e "$tmp/previous.app" ] && mv "$tmp/previous.app" "$target"
	die "could not install into $dest"
fi
xattr -dr com.apple.quarantine "$target" 2>/dev/null || true

say "Installed TwoPlacePaste $new_version in $dest."
open "$target"
