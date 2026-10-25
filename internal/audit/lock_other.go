//go:build !unix

package audit

import "os"

// lock is a no-op where flock is not available (skilltri targets macOS and Linux).
func lock(*os.File) (func(), error) { return func() {}, nil }
