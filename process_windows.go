//go:build windows

package main

import (
	"context"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func terminationSignals() []os.Signal { return []os.Signal{os.Interrupt} }
func prepareProxy(cmd *exec.Cmd)      { cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000200} }

// 所有するProxy専用グループへCTRL_BREAKを送り、他のプロセスへ波及させない。
func gracefulStop(p *os.Process) error {
	r, _, e := syscall.NewLazyDLL("kernel32.dll").NewProc("GenerateConsoleCtrlEvent").Call(1, uintptr(p.Pid))
	if r == 0 {
		return fmt.Errorf("CTRL_BREAK: %w", e)
	}
	return nil
}

// OSのファイルロックはハンドル解放・プロセス終了時にも解除される。
func lockProfiles(ctx context.Context, f *os.File) (func(), error) {
	var overlapped windows.Overlapped
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
		if err == nil {
			return func() { _ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped) }, nil
		}
		if err != windows.ERROR_LOCK_VIOLATION {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
