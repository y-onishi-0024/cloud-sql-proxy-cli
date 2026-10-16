package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// OSのクリップボードに触れず、実際に起動した子のargvとstdinを観測する。
func runClipboardFixture() int {
	if os.Getenv("CSQL_TEST_CLIPBOARD_MODE") == "wait" {
		time.Sleep(10 * time.Second)
	}
	if os.Getenv("CSQL_TEST_CLIPBOARD_MODE") == "fail" {
		fmt.Fprintln(os.Stderr, "fixture copy error")
		return 7
	}
	data, _ := io.ReadAll(os.Stdin)
	record, _ := json.Marshal(struct {
		Args []string
		Text string
	}{os.Args[1:], string(data)})
	if err := os.WriteFile(os.Getenv("CSQL_TEST_CLIPBOARD"), record, 0600); err != nil {
		return 8
	}
	return 0
}
func clipboardFixture(t *testing.T) (*App, string) {
	t.Helper()
	a := NewApp()
	file := filepath.Join(t.TempDir(), "copied.json")
	t.Setenv("CSQL_TEST_CLIPBOARD", file)
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	a.LookPath = func(string) (string, error) { return exe, nil }
	return a, file
}
func TestClipboardBackendExecution(t *testing.T) {
	text := "'/日本語 path/it'\"'\"'s/gcloud' auth login\n$(do-not-execute) \"double\""
	for _, tc := range []struct {
		os, wayland, tool string
		args              []string
	}{
		{"darwin", "", "pbcopy", nil},
		{"windows", "", "powershell.exe", nil},
		{"linux", "display", "wl-copy", nil},
		{"linux", "", "xclip", []string{"-selection", "clipboard", "-in"}},
	} {
		t.Run(tc.os+tc.wayland, func(t *testing.T) {
			a, file := clipboardFixture(t)
			a.OS = tc.os
			a.Env = func(key string) string {
				if key == "WAYLAND_DISPLAY" {
					return tc.wayland
				}
				return ""
			}
			original := a.LookPath
			a.LookPath = func(name string) (string, error) {
				if name != tc.tool {
					t.Fatalf("別のバックエンド: %s", name)
				}
				return original(name)
			}
			if err := a.copyText(context.Background(), text); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var record struct {
				Args []string
				Text string
			}
			if err = json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			if record.Text != text {
				t.Fatalf("stdinが変化: %q", record.Text)
			}
			if tc.os == "windows" {
				if len(record.Args) != 5 || record.Args[0] != "-NoLogo" || record.Args[1] != "-NoProfile" || record.Args[2] != "-NonInteractive" || record.Args[3] != "-Command" || !strings.Contains(record.Args[4], "UTF8Encoding") || !strings.Contains(record.Args[4], "ReadToEnd") || !strings.Contains(record.Args[4], "Set-Clipboard") {
					t.Fatalf("PowerShell固定引数: %q", record.Args)
				}
			} else if strings.Join(record.Args, "|") != strings.Join(tc.args, "|") {
				t.Fatalf("argv: %q", record.Args)
			}
			if strings.Contains(strings.Join(record.Args, " "), "do-not-execute") {
				t.Fatal("入力が実行コードに混入")
			}
		})
	}
	// 優先側が未導入なら、利用可能なもう一方を使う。
	t.Run("linux-fallback", func(t *testing.T) {
		a, file := clipboardFixture(t)
		a.OS = "linux"
		a.Env = func(string) string { return "display" }
		original := a.LookPath
		a.LookPath = func(name string) (string, error) {
			if name == "wl-copy" {
				return "", exec.ErrNotFound
			}
			return original(name)
		}
		if err := a.copyText(context.Background(), text); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(file); err != nil {
			t.Fatal(err)
		}
	})
}
func TestClipboardFailures(t *testing.T) {
	for _, mode := range []string{"missing", "fail", "wait", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			a, file := clipboardFixture(t)
			a.OS = "darwin"
			t.Setenv("CSQL_TEST_CLIPBOARD_MODE", mode)
			if mode == "missing" {
				a.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			started := time.Now()
			err := a.copyText(ctx, "gcloud auth login")
			if err == nil {
				t.Fatal("失敗が成功扱い")
			}
			if time.Since(started) > 3*time.Second {
				t.Fatal("コピーの待機上限を超えた")
			}
			if _, e := os.Stat(file); !os.IsNotExist(e) {
				t.Fatal("拒否/失敗で出力が残った")
			}
		})
	}
}
