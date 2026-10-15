//go:build !windows

package main

import (
	"context"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func terminationSignals() []os.Signal  { return []os.Signal{os.Interrupt, syscall.SIGTERM} }
func prepareProxy(cmd *exec.Cmd)       {}
func gracefulStop(p *os.Process) error { return p.Signal(syscall.SIGTERM) }

// ファイルを削除しないことで、待機者が別inodeを排他する競合を防ぐ。
func lockProfiles(ctx context.Context, f *os.File) (func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }, nil
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
