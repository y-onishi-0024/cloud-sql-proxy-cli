//go:build installer

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// GUIの確認境界を追加しても、端末向けの非TTY拒否は保持する。
func TestInstallerConfirmationBoundary(t *testing.T) {
	a, out, _ := testApp(t)
	a.TTY = false
	if _, err := a.confirm(context.Background(), "確認"); err == nil {
		t.Fatal("通常CLIが非TTYで承認された")
	}
	a.ConfirmAction = func(ctx context.Context, prompt string) (bool, error) { return prompt == "GUI確認", nil }
	ok, err := a.confirm(context.Background(), "GUI確認 [y/N]")
	if strings.Contains(out.String(), "[y/N]") {
		t.Fatal("GUIに端末入力の案内を表示")
	}
	if err != nil || !ok {
		t.Fatalf("GUIで明示承認できない: %v %v", ok, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ok, err := a.confirm(ctx, "GUI確認"); ok || err == nil {
		t.Fatal("取消済み要求をGUIで承認")
	}
	a.ConfirmAction = func(ctx context.Context, prompt string) (bool, error) { cancel(); return true, nil }
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	if ok, err := a.confirm(ctx, "GUI確認"); ok || err == nil {
		t.Fatal("回答中の取消を無視")
	}
}

func awaitInstaller(t *testing.T, c *installerController, predicate func(installerSnapshot) bool) installerSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := c.Snapshot()
		if predicate(s) {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("セットアップ状態の時間切れ: %+v", c.Snapshot())
	return installerSnapshot{}
}
func installerFixture(t *testing.T) (*installerController, *App, string, *[]string) {
	t.Helper()
	a, home, _ := pathTestApp(t)
	a.TTY = false
	a.Runner = osRunner{}
	proxy := []byte("#!/bin/sh\necho 'cloud-sql-proxy version 2.24.1 --address --port --private-ip --structured-logs --run-connection-test'\n")
	sum := sha256.Sum256(proxy)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(proxy) }))
	t.Cleanup(srv.Close)
	a.AllowTestHTTP = true
	a.Artifacts = []Artifact{{Name: "cloud-sql-proxy", Version: "2.24.1", URL: srv.URL, SHA256: hex.EncodeToString(sum[:]), Format: "binary"}}
	var extracted []string
	payload := func(ctx context.Context) (string, func(), error) {
		dir, err := os.MkdirTemp("", "csql-installer-fixture-*")
		if err != nil {
			return "", nil, err
		}
		p := filepath.Join(dir, "csql")
		err = os.WriteFile(p, []byte("#!/bin/sh\necho 'csql "+version+"'\n"), 0700)
		extracted = append(extracted, p)
		return p, func() { os.RemoveAll(dir) }, err
	}
	c := newInstallerController(func() *App { return a }, payload)
	t.Cleanup(func() {
		c.Cancel()
		deadline := time.Now().Add(time.Second)
		for c.Snapshot().Active() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	})
	return c, a, home, &extracted
}
func TestInstallerTwoConfirmationsAndCLIPayload(t *testing.T) {
	c, a, home, extracted := installerFixture(t)
	if !c.Start("zsh") || c.Start("zsh") {
		t.Fatal("二重開始を許可")
	}
	first := awaitInstaller(t, c, func(s installerSnapshot) bool { return s.State == installerAwaiting })
	if _, err := os.Stat(a.Paths.ToolsDir); !os.IsNotExist(err) {
		t.Fatal("承認前にツールを書いた")
	}
	if !c.Answer(first.PromptID, true) || c.Answer(first.PromptID, true) {
		t.Fatal("同じ確認への重複回答")
	}
	second := awaitInstaller(t, c, func(s installerSnapshot) bool { return s.State == installerAwaiting && s.PromptID != first.PromptID })
	if c.Answer(first.PromptID, true) {
		t.Fatal("古い確認が次の変更を承認")
	}
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); !os.IsNotExist(err) {
		t.Fatal("PATH承認前に設定した")
	}
	if !c.Answer(second.PromptID, true) {
		t.Fatal("PATH確認への回答失敗")
	}
	done := awaitInstaller(t, c, func(s installerSnapshot) bool { return !s.Active() })
	if done.State != installerComplete {
		t.Fatalf("完了しなかった: %+v", done)
	}
	body, err := os.ReadFile(filepath.Join(a.Paths.ConfigDir, "bin", "csql"))
	if err != nil || !bytes.Contains(body, []byte("echo 'csql ")) {
		t.Fatalf("GUIではなくCLIを配置できない: %s %v", body, err)
	}
	for _, p := range *extracted {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("完了後も一時CLIが残った")
		}
	}
	if c.Start("zsh") {
		t.Fatal("成功済み操作を二重実行")
	}
}
func TestInstallerDeclineAndCancel(t *testing.T) {
	for _, stage := range []string{"decline-install", "cancel-path"} {
		t.Run(stage, func(t *testing.T) {
			c, a, home, files := installerFixture(t)
			c.Start("zsh")
			s := awaitInstaller(t, c, func(s installerSnapshot) bool { return s.State == installerAwaiting })
			if stage == "decline-install" {
				c.Answer(s.PromptID, false)
			} else {
				c.Answer(s.PromptID, true)
				awaitInstaller(t, c, func(v installerSnapshot) bool { return v.State == installerAwaiting && v.PromptID != s.PromptID })
				c.Cancel()
			}
			done := awaitInstaller(t, c, func(s installerSnapshot) bool { return !s.Active() })
			if done.State != installerCancelled {
				t.Fatalf("取消を完了扱い: %+v", done)
			}
			if _, err := os.Stat(filepath.Join(home, ".zshrc")); !os.IsNotExist(err) {
				t.Fatal("取消後にPATH設定")
			}
			if stage == "decline-install" {
				if _, err := os.Stat(a.Paths.ToolsDir); !os.IsNotExist(err) {
					t.Fatal("導入取消後にツール配置")
				}
			}
			for _, p := range *files {
				if _, err := os.Stat(p); !os.IsNotExist(err) {
					t.Fatal("取消後も一時CLIが残った")
				}
			}
		})
	}
}
func TestInstallerFailedPathCanRetry(t *testing.T) {
	c, a, home, files := installerFixture(t)
	rc := filepath.Join(home, ".zshrc")
	os.WriteFile(rc, []byte(pathStart+"\ninvalid\n"), 0600)
	c.Start("zsh")
	first := awaitInstaller(t, c, func(s installerSnapshot) bool { return s.State == installerAwaiting })
	c.Answer(first.PromptID, true)
	failed := awaitInstaller(t, c, func(s installerSnapshot) bool { return !s.Active() })
	if failed.State != installerFailed {
		t.Fatalf("PATH失敗を成功扱い: %+v", failed)
	}
	if _, err := os.Stat(filepath.Join(a.Paths.ToolsDir, "cloud-sql-proxy", "installed.json")); err != nil {
		t.Fatal("部分成功した導入が失われた")
	}
	os.Remove(rc)
	if !c.Start("zsh") {
		t.Fatal("失敗後に再試行できない")
	}
	next := awaitInstaller(t, c, func(s installerSnapshot) bool { return s.State == installerAwaiting })
	if next.PromptID == first.PromptID || c.Answer(first.PromptID, true) {
		t.Fatal("再試行で確認IDを再利用")
	}
	c.Answer(next.PromptID, true)
	if s := awaitInstaller(t, c, func(s installerSnapshot) bool { return !s.Active() }); s.State != installerComplete {
		t.Fatalf("再試行失敗: %+v", s)
	}
	for _, p := range *files {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("再試行後に一時CLIが残った")
		}
	}
}
func TestInstallerCancelsDownload(t *testing.T) {
	c, a, home, _ := installerFixture(t)
	entered := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() }))
	defer srv.Close()
	a.Artifacts[0].URL = srv.URL
	c.Start("zsh")
	s := awaitInstaller(t, c, func(s installerSnapshot) bool { return s.State == installerAwaiting })
	c.Answer(s.PromptID, true)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("取得開始の時間切れ")
	}
	c.Cancel()
	if s := awaitInstaller(t, c, func(s installerSnapshot) bool { return !s.Active() }); s.State != installerCancelled {
		t.Fatalf("実行中の取消失敗: %+v", s)
	}
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); !os.IsNotExist(err) {
		t.Fatal("取消後に次のPATH変更へ進んだ")
	}
}
func TestInstallerPayloadValidation(t *testing.T) {
	body := []byte("normal CLI")
	sum := sha256.Sum256(body)
	for _, mode := range []string{"valid", "hash", "version", "os", "arch", "kind", "missing-manifest", "missing-cli"} {
		t.Run(mode, func(t *testing.T) {
			m := installerPayloadManifest{Kind: "csql-cli", Version: version, OS: runtime.GOOS, Arch: runtime.GOARCH, SHA256: hex.EncodeToString(sum[:])}
			switch mode {
			case "hash":
				m.SHA256 = strings.Repeat("0", 64)
			case "version":
				m.Version = "wrong"
			case "os":
				m.OS = "wrong"
			case "arch":
				m.Arch = "wrong"
			case "kind":
				m.Kind = "setup"
			}
			manifest, _ := json.Marshal(m)
			name := "csql"
			if runtime.GOOS == "windows" {
				name += ".exe"
			}
			files := fstest.MapFS{"manifest.json": &fstest.MapFile{Data: manifest}, name: &fstest.MapFile{Data: body, Mode: 0700}}
			if mode == "missing-manifest" {
				delete(files, "manifest.json")
			}
			if mode == "missing-cli" {
				delete(files, name)
			}
			p, cleanup, err := materializeInstallerPayload(context.Background(), files, runtime.GOOS, runtime.GOARCH)
			if mode != "valid" {
				if cleanup != nil {
					cleanup()
				}
				if err == nil {
					t.Fatal("不正な同梱物を受け入れた")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(p)
			if !bytes.Equal(got, body) {
				t.Fatal("CLI内容不一致")
			}
			cleanup()
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Fatal("一時配置を削除できない")
			}
		})
	}
}
func TestInstallerLogIsBounded(t *testing.T) {
	c := newInstallerController(nil, nil)
	c.Write([]byte(strings.Repeat("x", 2*installerLogLimit) + "\x00日本語"))
	s := c.Snapshot()
	if len(s.Log) > installerLogLimit+3 || strings.ContainsRune(s.Log, '\x00') || !strings.HasSuffix(s.Log, "日本語") {
		t.Fatal("ログの容量/文字列境界が不正")
	}
}
