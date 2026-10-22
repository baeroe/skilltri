//go:build !unix

package daemon

// tryLock is a no-op where flock is not available (skilltri targets macOS and Linux).
func tryLock(string) (func(), error) { return func() {}, nil }
