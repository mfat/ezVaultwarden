package fetch

import "fmt"

// Tested versions. Each release pins exact content by digest, so a tag moving
// upstream can never change what users install.
const (
	VaultwardenVersion = "1.37.4"
	CaddyVersion       = "2.11.7"
	LegoVersion        = "5.6.0"
)

// VersionID names the folder holding this exact set of components.
func VersionID() string { return VaultwardenVersion + "+caddy" + CaddyVersion }

// VaultwardenImage is the 1.37.4-alpine index, whose binaries are static.
var VaultwardenImage = Image{
	Registry: "https://registry-1.docker.io",
	AuthURL:  "https://auth.docker.io/token?service=registry.docker.io",
	Repo:     "vaultwarden/server",
	Digest:   "sha256:19ea2b669945d842dadc3d500f2d6a795866a003330f59a47b9634658bae98bb",
}

var caddySHA512 = map[string]string{
	"amd64": "a7a433a1b133efc3c8d10eb0b99d52a24b5ef5c322dc77f5282182b1c0402139ab83f3a99f0c52409df77d20123fb0b523edad8a66d8f5e49136197bf61ef0e7",
	"arm64": "3db36ba90c7a6e8dda40ee3dd71fa08844c76b5fb08f61b31e5e78d2ed38e71c51dc7baed875e50d1ca1279196e84302967237386ae87c91ae9f2aaceada682e",
}

// Only fetched by expose lan, so it stays out of VersionID.
var legoSHA256 = map[string]string{
	"amd64": "f02fc11b6f56b97e387fa7caa0268f86f5a336953b22458e6430e4651fea439a",
	"arm64": "34d87a6552d69390c4a4560237045c47713a8e148d899e16db0e97040894ed1f",
}

// LegoRelease returns the download URL and checksum for arch.
func LegoRelease(arch string) (url, sha256sum string, err error) {
	sha256sum, ok := legoSHA256[arch]
	if !ok {
		return "", "", fmt.Errorf("no lego build pinned for %s", arch)
	}
	url = fmt.Sprintf("https://github.com/go-acme/lego/releases/download/v%[1]s/lego_v%[1]s_linux_%[2]s.tar.gz",
		LegoVersion, arch)
	return url, sha256sum, nil
}

// CaddyRelease returns the download URL and checksum for arch.
func CaddyRelease(arch string) (url, sha512sum string, err error) {
	sha512sum, ok := caddySHA512[arch]
	if !ok {
		return "", "", fmt.Errorf("no Caddy build pinned for %s", arch)
	}
	url = fmt.Sprintf("https://github.com/caddyserver/caddy/releases/download/v%[1]s/caddy_%[1]s_linux_%[2]s.tar.gz",
		CaddyVersion, arch)
	return url, sha512sum, nil
}
