// Command ezvaultwarden sets up and looks after a personal Vaultwarden server.
package main

import (
	"os"

	"github.com/mfat/ezVaultwarden/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
