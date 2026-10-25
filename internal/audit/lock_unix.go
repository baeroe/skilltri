//go:build unix

package audit

import (
	"os"
	"syscall"
)

// lock takes an exclusive flock on f, so appends from several processes never interleave.
func lock(f *os.File) (func(), error) {
	var err error
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }, nil
}
