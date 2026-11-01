#!/bin/sh
# Installs ezvaultwarden for the current user. Needs no root.
#
#   curl -fsSL https://raw.githubusercontent.com/mfat/ezVaultwarden/main/install.sh | sh
#
# EZVAULTWARDEN_INSTALL_DIR  where to put the binary (default ~/.local/bin)
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
	dir="${EZVAULTWARDEN_INSTALL_DIR:-$HOME/.local/bin}"

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

	mkdir -p "$dir"
	chmod 755 "$tmp/$asset"
	# Same folder then rename, so a running copy is never half overwritten.
	cp "$tmp/$asset" "$dir/.ezvaultwarden.new"
	mv -f "$dir/.ezvaultwarden.new" "$dir/ezvaultwarden"

	echo "✓ Installed to $dir/ezvaultwarden"
	case ":$PATH:" in
	*":$dir:"*)
		echo "Next, run: ezvaultwarden init"
		;;
	*)
		echo "⚠ $dir isn't on your PATH. Run it directly:"
		echo "  $dir/ezvaultwarden init"
		;;
	esac
}

fail() {
	echo "✗ $1" >&2
	exit 1
}

main "$@"
