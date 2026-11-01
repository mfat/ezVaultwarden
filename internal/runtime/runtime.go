// Package runtime starts and stops the services. It is the only part that
// differs between native, Docker and Podman; everything above it is shared.
package runtime

import "io"

// Runtime runs the Vaultwarden and Caddy services.
type Runtime interface {
	// Name is how the runtime is recorded in the state file.
	Name() string
	// Available explains why this runtime cannot be used here, or returns nil.
	Available() error
	// Install registers the services and starts them on boot.
	Install(self string) error
	// Start and Stop act on both services. StopVault and StartVault leave the
	// front end up, which is all a backup needs.
	Start() error
	Stop() error
	StartVault() error
	StopVault() error
	RestartVault() error
	// Running reports whether both services are up.
	Running() bool
	// Logs writes the last n lines to w.
	Logs(w io.Writer, n int) error
	// Remove stops and deletes the services, leaving data alone.
	Remove() error
}
