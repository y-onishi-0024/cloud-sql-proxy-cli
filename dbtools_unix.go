//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// アプリの終了は利用者へ任せる。実行中の別ウィンドウも保守的に拒否する。
func dbToolRunning(ctx context.Context, t dbToolTarget) (bool, error) {
	cmd := exec.CommandContext(ctx, "/bin/ps", "-axo", "comm=")
	var out, errs dbToolBuffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	if err := cmd.Run(); err != nil {
		return false, err
	}
	if out.exceeded || errs.exceeded {
		return false, fmt.Errorf("プロセス一覧が上限を超えています")
	}
	for _, line := range strings.Split(strings.ToLower(out.String()), "\n") {
		if t.Tool == "dbeaver" && strings.Contains(line, "dbeaver") {
			return true, nil
		}
		if t.Tool == "pgadmin" && strings.Contains(line, "pgadmin") {
			return true, nil
		}
	}
	return false, nil
}
func readA5Settings() ([]byte, error) {
	return nil, fmt.Errorf("A5直接登録はWindowsで利用してください")
}
func startA5Registration(dbToolTarget, string, string) (bool, error) {
	return false, fmt.Errorf("A5直接登録はWindowsで利用してください")
}

func dbToolPrivate(path string, dir bool) error {
	if dir {
		return os.Chmod(path, 0700)
	}
	return os.Chmod(path, 0600)
}
