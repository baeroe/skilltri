//go:build !unix

package config

// lockFile is a no-op where flock is not available (tussh targets macOS and Linux).
func lockFile(string) (func(), error) { return func() {}, nil }
