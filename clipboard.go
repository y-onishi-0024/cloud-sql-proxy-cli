package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// 実行とpipe後処理の予算を分け、取消後の待機を積み上げない。
func (a *App) copyText(ctx context.Context, text string) error {
	ctx, cancel := context.WithTimeout(ctx, 1800*time.Millisecond)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.CopyText != nil {
		err := a.CopyText(ctx, text)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	names := []string{"pbcopy"}
	switch a.OS {
	case "windows":
		names = []string{"powershell.exe"}
	case "linux":
		names = []string{"xclip", "wl-copy"}
		if a.Env != nil && a.Env("WAYLAND_DISPLAY") != "" {
			names = []string{"wl-copy", "xclip"}
		}
	case "darwin":
	default:
		return fmt.Errorf("このOSのコピーに対応していません。表示されたコマンドを手動コピーしてください")
	}
	var path, name string
	for _, candidate := range names {
		if p, err := a.LookPath(candidate); err == nil {
			path, name = p, candidate
			break
		}
	}
	if path == "" {
		return fmt.Errorf("コピー用コマンドが見つかりません（%s）。表示されたコマンドを手動コピーしてください", strings.Join(names, " / "))
	}
	var args []string
	switch name {
	case "powershell.exe":
		// 入力はUTF-8のstdinに限定し、復旧コマンドをPowerShellコードへ埋め込まない。
		args = []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "[Console]::InputEncoding = [System.Text.UTF8Encoding]::new($false); Set-Clipboard -Value ([Console]::In.ReadToEnd())"}
	case "xclip":
		args = []string{"-selection", "clipboard", "-in"}
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = strings.NewReader(text)
	cmd.WaitDelay = 200 * time.Millisecond
	var stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{Writer: io.Discard, Remaining: 16 << 10}
	cmd.Stderr = &limitedWriter{Writer: &stderr, Remaining: 16 << 10}
	err := cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("%sでコピーできませんでした: %w\n%s", name, err, safeLog(stderr.String()))
	}
	return nil
}
