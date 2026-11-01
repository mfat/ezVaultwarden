// Command cache downloads and verifies this tree's pinned components into a
// folder, shaped like a version folder, so the end-to-end tests can reuse it.
package main

import (
	"context"
	"fmt"
	"os"
	"runtime"

	"github.com/mfat/ezVaultwarden/internal/fetch"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: cache <folder>")
		os.Exit(2)
	}
	if err := fill(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func fill(dest string) error {
	ctx := context.Background()
	url, sum, err := fetch.CaddyRelease(runtime.GOARCH)
	if err != nil {
		return err
	}
	staged := dest + ".partial"
	if err := os.RemoveAll(staged); err != nil {
		return err
	}
	if err := os.MkdirAll(staged, 0o755); err != nil {
		return err
	}
	if err := fetch.ExtractVaultwarden(ctx, fetch.VaultwardenImage, runtime.GOARCH, staged, nil); err != nil {
		return err
	}
	if err := fetch.ExtractCaddy(ctx, url, sum, staged, nil); err != nil {
		return err
	}
	return os.Rename(staged, dest)
}
