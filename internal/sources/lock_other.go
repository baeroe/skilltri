//go:build !unix

package sources

// lockFile is a no-op where flock is not available (skilltri targets macOS and Linux).
func lockFile(string) (func(), error) { return func() {}, nil }
