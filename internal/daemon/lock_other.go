//go:build !unix

package daemon

import "context"

// tryLock is a no-op where flock is not available (skilltri targets macOS and Linux).
func tryLock(string) (func(), error) { return func() {}, nil }

// waitLock is a no-op where flock is not available.
func waitLock(context.Context, string) (func(), error) { return func() {}, nil }
