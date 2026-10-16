//go:build windows

package main

import "os/exec"

// 親は入力を読まず待つ。子のCtrl+C終了処理をCommandContextのKillで妨げない。
func restartSelfUpdate(a *App, path string) error {
	cmd := exec.Command(path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = a.In, a.Out, a.Err
	return cmd.Run()
}
