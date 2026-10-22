//go:build unix

package daemon

import (
	"context"
	"errors"
	"os"
	"syscall"
)

// tryLock takes a non-blocking exclusive flock on path (created 0600). It returns
// ErrAlreadyRunning when another process holds it; the lock lasts until unlock or process exit.
func tryLock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrAlreadyRunning
		}
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// waitLock takes a blocking exclusive flock on path, so a service-started daemon queues behind
// a shim-started one. It returns ctx's error when ctx ends first; the blocked flock then goes
// on in the background and releases the lock as soon as it gets it.
func waitLock(ctx context.Context, path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	fd := int(f.Fd())
	got := make(chan error, 1)
	go func() {
		for {
			err := syscall.Flock(fd, syscall.LOCK_EX)
			if err != syscall.EINTR {
				got <- err
				return
			}
		}
	}()
	unlock := func() {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		f.Close()
	}
	select {
	case err := <-got:
		if err != nil {
			f.Close()
			return nil, err
		}
		return unlock, nil
	case <-ctx.Done():
		go func() {
			if err := <-got; err == nil {
				unlock()
			} else {
				f.Close()
			}
		}()
		return nil, ctx.Err()
	}
}
