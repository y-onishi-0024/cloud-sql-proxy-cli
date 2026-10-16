//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// TUI復元後の同じ端末へ、元の接続引数なしで新版を引き継ぐ。
func restartSelfUpdate(a *App, path string) error {
	in, okIn := a.In.(*os.File)
	out, okOut := a.Out.(*os.File)
	errOut, okErr := a.Err.(*os.File)
	if !okIn || !okOut || !okErr || in.Fd() != 0 || out.Fd() != 1 || errOut.Fd() != 2 {
		return fmt.Errorf("端末を引き継げません。%s を実行してください", shellQuote(path))
	}
	return syscall.Exec(path, []string{path}, os.Environ())
}
