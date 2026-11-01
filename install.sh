#!/bin/sh
# Installs or updates ezvaultwarden. Needs no root.
#
#   curl -fsSL https://raw.githubusercontent.com/mfat/ezVaultwarden/main/install.sh | sh
#
# EZVAULTWARDEN_INSTALL_DIR  where to put the binary (default ~/.local/bin, or
#                            /usr/local/bin for root, which is already on its PATH)
# EZVAULTWARDEN_VERSION      a release tag such as v0.1.0 (default: latest)

# Everything is inside main, so a download cut off halfway runs nothing.
main() {
	set -eu

	repo="https://github.com/mfat/ezVaultwarden/releases"
	version="${EZVAULTWARDEN_VERSION:-latest}"
	if [ "$version" = latest ]; then
		base="$repo/latest/download"
	else
		base="$repo/download/$version"
	fi
	base="${EZVAULTWARDEN_BASE_URL:-$base}"
	default_dir="$HOME/.local/bin"
	if [ "$(id -u)" = 0 ]; then
		default_dir=/usr/local/bin
	fi
	dir="${EZVAULTWARDEN_INSTALL_DIR:-$default_dir}"

	os="$(uname -s)"
	if [ "$os" != Linux ]; then
		fail "ezVaultwarden runs natively on Linux. Support for $os is planned."
	fi
	case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) fail "there is no build for $(uname -m) yet." ;;
	esac
	command -v curl >/dev/null || fail "curl is needed."
	command -v sha256sum >/dev/null || fail "sha256sum is needed."

	asset="ezvaultwarden_linux_$arch"
	tmp="$(mktemp -d)"
	trap 'rm -rf "$tmp"' EXIT INT TERM

	echo "Downloading ezVaultwarden ($arch)..."
	curl -fsSL --retry 3 -o "$tmp/$asset" "$base/$asset" ||
		fail "couldn't download $base/$asset"
	curl -fsSL --retry 3 -o "$tmp/checksums.txt" "$base/checksums.txt" ||
		fail "couldn't download the checksums."

	want="$(grep "  $asset\$" "$tmp/checksums.txt" | cut -d' ' -f1)"
	got="$(sha256sum "$tmp/$asset" | cut -d' ' -f1)"
	if [ -z "$want" ] || [ "$want" != "$got" ]; then
		fail "the download doesn't match its checksum, so nothing was installed."
	fi

	chmod 755 "$tmp/$asset"
	old="$(installed_version "$dir/ezvaultwarden")"
	place "$tmp/$asset" "$dir"
	# An older install ran from here, and its timers still call this copy.
	if [ -x "$HOME/.local/bin/ezvaultwarden" ] && [ "$dir" != "$HOME/.local/bin" ]; then
		[ -n "$old" ] || old="$(installed_version "$HOME/.local/bin/ezvaultwarden")"
		place "$tmp/$asset" "$HOME/.local/bin"
	fi
	new="$(installed_version "$dir/ezvaultwarden")"

	if [ -z "$old" ]; then
		echo "✓ Installed ezVaultwarden $new to $dir"
	elif [ "$old" = "$new" ]; then
		echo "✓ ezVaultwarden $new is already the latest"
	else
		echo "✓ Updated ezVaultwarden from $old to $new"
	fi

	run=ezvaultwarden
	case ":$PATH:" in
	*":$dir:"*) ;;
	*)
		run="$dir/ezvaultwarden"
		echo "⚠ $dir isn't on your PATH, so use the full path below."
		;;
	esac
	data="${EZVAULTWARDEN_HOME:-$HOME/.local/share/ezvaultwarden}"
	if [ -f "$data/state.json" ]; then
		echo "Next, to update your vault too, run: $run update"
	else
		echo "Next, run: $run init"
	fi
}

# Same folder then rename, so a running copy is never half overwritten.
place() {
	mkdir -p "$2"
	cp "$1" "$2/.ezvaultwarden.new"
	mv -f "$2/.ezvaultwarden.new" "$2/ezvaultwarden"
}

installed_version() {
	if [ -x "$1" ]; then
		"$1" version 2>/dev/null | cut -d' ' -f2
	fi
}

fail() {
	echo "✗ $1" >&2
	exit 1
}

main "$@"
