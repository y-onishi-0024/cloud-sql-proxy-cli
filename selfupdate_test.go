package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 正式な配布物の欠落や別OSの選択で、意図しないバイナリを取得しない。
func TestSelfUpdateReleaseContract(t *testing.T) {
	for _, mode := range []string{"new", "same", "older", "draft", "prerelease", "tag", "missing", "duplicate", "digest", "size", "oversize", "state", "unsupported", "http"} {
		t.Run(mode, func(t *testing.T) {
			a, _, _ := testApp(t)
			a.OS, a.Arch = "darwin", "arm64"
			asset := map[string]any{"name": "csql-0.8.0-darwin-arm64", "size": 123, "state": "uploaded", "digest": "sha256:" + strings.Repeat("a", 64), "browser_download_url": "https://untrusted.invalid/payload"}
			release := map[string]any{"tag_name": "v0.8.0", "assets": []any{asset}}
			switch mode {
			case "same":
				release["tag_name"] = "v" + version
			case "older":
				release["tag_name"] = "v0.1.0"
			case "draft":
				release["draft"] = true
			case "prerelease":
				release["prerelease"] = true
			case "tag":
				release["tag_name"] = "v0.8.0\x1b"
			case "missing":
				release["assets"] = []any{}
			case "duplicate":
				release["assets"] = []any{asset, asset}
			case "digest":
				asset["digest"] = "sha256:bad"
			case "size":
				asset["size"] = 0
			case "oversize":
				asset["size"] = int64(256<<20) + 1
			case "state":
				asset["state"] = "new"
			case "unsupported":
				a.OS = "plan9"
			}
			body, _ := json.Marshal(release)
			a.Env = func(string) string { return "1" } // 明示更新は起動時通知の無効化とは分ける。
			a.HTTP = &http.Client{Transport: updateTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != "https://api.github.com/repos/y-onishi-0024/cloud-sql-proxy-cli/releases/latest" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Fatal("信頼境界が変わった")
				}
				code := 200
				if mode == "http" {
					code = 403
				}
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
			})}
			got, err := a.fetchSelfUpdate(context.Background())
			switch mode {
			case "new":
				if err != nil || got == nil || got.Name != asset["name"] || got.URL != "https://github.com/y-onishi-0024/cloud-sql-proxy-cli/releases/download/v0.8.0/csql-0.8.0-darwin-arm64" {
					t.Fatalf("公式新版を選べない: %+v %v", got, err)
				}
			case "same", "older":
				if err != nil || got != nil {
					t.Fatalf("変更不要の版を更新: %+v %v", got, err)
				}
			default:
				if err == nil {
					t.Fatal(fmt.Sprintf("不正な配布物を受理: %s", mode))
				}
			}
		})
	}
}

// 一時管理領域の旧版と記録を用意し、利用者の実インストールへ触れない。
func selfUpdateTestTarget(t *testing.T, a *App) selfUpdateTarget {
	t.Helper()
	path := filepath.Join(a.Paths.ConfigDir, "bin", "csql")
	if a.OS == "windows" {
		path += ".exe"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	old := []byte("previous csql binary")
	if err := os.WriteFile(path, old, 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(old)
	if err := writeJSON(filepath.Join(a.Paths.ConfigDir, "binary-install.json"), binaryReceipt{hex.EncodeToString(sum[:])}); err != nil {
		t.Fatal(err)
	}
	a.Executable = func() (string, error) { return path, nil }
	target, err := a.inspectSelfUpdateTarget()
	if err != nil {
		t.Fatal(err)
	}
	return target
}

// 準備が失敗した場合は旧版を維持し、検証不能なpayloadを起動しない。
func TestSelfUpdatePreparation(t *testing.T) {
	exe, _ := os.Executable()
	binary, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"ok", "hash", "short", "extra", "oversize", "http", "redirect", "format", "cpu", "version", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			a, _, _ := testApp(t)
			a.OS, a.Arch = runtime.GOOS, runtime.GOARCH
			target := selfUpdateTestTarget(t, a)
			t.Setenv("CSQL_TEST_SELF_UPDATE_VERSION", "0.8.0")
			if mode == "version" {
				t.Setenv("CSQL_TEST_SELF_UPDATE_VERSION", "0.9.0")
			}
			data := binary
			if mode == "format" {
				data = []byte("not an executable")
			}
			sum := sha256.Sum256(data)
			digest := hex.EncodeToString(sum[:])
			if mode == "hash" {
				digest = strings.Repeat("0", 64)
			}
			offer := selfUpdateOffer{Tag: "v0.8.0", Name: "csql-0.8.0-" + a.OS + "-" + a.Arch, URL: "https://github.com/y-onishi-0024/cloud-sql-proxy-cli/releases/download/v0.8.0/payload", SHA256: digest, Size: int64(len(data))}
			if mode == "cpu" {
				if a.Arch == "arm64" {
					a.Arch = "amd64"
				} else {
					a.Arch = "arm64"
				}
			}
			a.HTTP = &http.Client{Transport: updateTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "github.com" {
					t.Error("許可していないhostへ取得した")
				}
				body := data
				header := make(http.Header)
				code := 200
				length := int64(len(data))
				switch mode {
				case "short":
					body = data[:len(data)-1]
				case "extra":
					body = append(append([]byte{}, data...), 0)
				case "oversize":
					length = downloadLimit + 1
				case "http":
					code = 404
				case "redirect":
					code = 302
					header.Set("Location", "https://untrusted.invalid/file")
				}
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(string(body))), Header: header, ContentLength: length}, nil
			})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			p, err := a.prepareSelfUpdate(ctx, offer, target, nil)
			if mode == "ok" {
				if err != nil || p == nil {
					t.Fatalf("準備失敗: %v", err)
				}
				staged, e := os.ReadFile(p.Payload)
				if e != nil || !strings.EqualFold(fmt.Sprintf("%x", sha256.Sum256(staged)), digest) {
					t.Fatal("取得したpayloadが異なる")
				}
				if e = p.Close(); e != nil {
					t.Fatal(e)
				}
			} else if err == nil {
				if p != nil {
					p.Close()
				}
				t.Fatal("検証不能な更新を受理")
			}
			contents, _ := os.ReadFile(target.Path)
			if string(contents) != "previous csql binary" {
				t.Fatal("準備中に旧版を変更")
			}
			dirs, _ := filepath.Glob(filepath.Join(a.Paths.ConfigDir, "bin", ".csql-update-*"))
			if len(dirs) != 0 {
				t.Fatal("中間ファイルが残存", dirs)
			}
		})
	}
}

func selfUpdatePrepared(t *testing.T, a *App) *preparedSelfUpdate {
	t.Helper()
	a.OS, a.Arch = runtime.GOOS, runtime.GOARCH
	target := selfUpdateTestTarget(t, a)
	exe, _ := os.Executable()
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	offer := selfUpdateOffer{Tag: "v0.8.0", Name: "payload", URL: "https://github.com/y-onishi-0024/cloud-sql-proxy-cli/releases/download/v0.8.0/payload", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
	t.Setenv("CSQL_TEST_SELF_UPDATE_VERSION", "0.8.0")
	a.HTTP = &http.Client{Transport: updateTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data)), Header: make(http.Header)}, nil
	})}
	p, err := a.prepareSelfUpdate(context.Background(), offer, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

// 配置の各失敗点と競合から、旧版/管理記録を壊さず復帰できる。
func TestSelfUpdateApplyContract(t *testing.T) {
	for _, mode := range []string{"ok", "lock", "changed", "new-rename", "old-rename", "version", "receipt", "rollback", "receipt-restore", "windows-new-rename", "windows-version", "managed", "receipt-mismatch", "link"} {
		t.Run(mode, func(t *testing.T) {
			a, _, _ := testApp(t)
			p := selfUpdatePrepared(t, a)
			preserved := []string{filepath.Join(a.Paths.ConfigDir, "profiles.json"), filepath.Join(a.Paths.ToolsDir, "installed.json"), filepath.Join(filepath.Dir(a.Paths.ConfigDir), ".zshrc")}
			for _, path := range preserved {
				os.MkdirAll(filepath.Dir(path), 0700)
				os.WriteFile(path, []byte("keep setting"), 0600)
			}
			if mode == "managed" {
				a.Executable = func() (string, error) { return "/elsewhere/csql", nil }
				if _, err := a.inspectSelfUpdateTarget(); err == nil {
					t.Fatal("管理外へ書込み可能")
				}
				return
			}
			if mode == "receipt-mismatch" {
				writeJSON(p.Target.Receipt.Path, binaryReceipt{"invalid"})
				if _, err := a.inspectSelfUpdateTarget(); err == nil {
					t.Fatal("管理記録不一致を受理")
				}
				return
			}
			if mode == "link" {
				os.Rename(p.Target.Path, p.Target.Path+"-real")
				if err := os.Symlink(p.Target.Path+"-real", p.Target.Path); err != nil {
					t.Fatal(err)
				}
				if _, err := a.inspectSelfUpdateTarget(); err == nil {
					t.Fatal("リンクを受理")
				}
				return
			}
			if mode == "lock" {
				if err := os.Mkdir(filepath.Join(a.Paths.ConfigDir, ".path-lock"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "changed" {
				os.WriteFile(p.Target.Path, []byte("other update"), 0700)
			}
			if err := p.Acquire(context.Background()); mode == "lock" || mode == "changed" {
				if err == nil {
					t.Fatal("競合を受理")
				}
				return
			} else if err != nil {
				t.Fatal(err)
			}
			original := append([]byte{}, p.Target.Binary.Before...)
			receipt := append([]byte{}, p.Target.Receipt.Before...)
			if mode == "old-rename" || strings.HasPrefix(mode, "windows-") {
				a.OS = "windows"
			}
			a.SelfUpdateOps = &selfUpdateFileOps{
				Rename: func(old, next string) error {
					if (mode == "new-rename" || mode == "windows-new-rename") && old == p.Payload || mode == "old-rename" && old == p.Target.Path || mode == "rollback" && strings.Contains(filepath.Base(old), "previous") {
						return errors.New("fixture rename failure")
					}
					return os.Rename(old, next)
				}, WriteReceipt: func(path string, value binaryReceipt) error {
					if mode == "receipt-restore" {
						os.Remove(path)
						os.Mkdir(path, 0700)
						return errors.New("fixture receipt storage failure")
					}
					if mode == "receipt" || mode == "rollback" {
						os.WriteFile(path, []byte("partial receipt"), 0600)
						return errors.New("fixture receipt failure")
					}
					return writeJSON(path, value)
				},
			}
			if mode == "version" {
				t.Setenv("CSQL_TEST_SELF_UPDATE_VERSION", "0.9.0")
			}
			err := a.applySelfUpdate(context.Background(), p)
			if mode == "ok" {
				if err != nil {
					t.Fatal(err)
				}
				data, _ := os.ReadFile(p.Target.Path)
				sum := sha256.Sum256(data)
				var r binaryReceipt
				readJSONFile(p.Target.Receipt.Path, &r)
				if r.SHA256 != hex.EncodeToString(sum[:]) || r.SHA256 != p.Offer.SHA256 {
					t.Fatal("新版と記録が不一致")
				}
				matches, _ := filepath.Glob(filepath.Join(p.StageDir, "previous-csql*"))
				if len(matches) != 1 {
					t.Fatal("旧版バックアップがない")
				}
				old, _ := os.ReadFile(matches[0])
				if !bytes.Equal(old, original) {
					t.Fatal("旧版が異なる")
				}
			} else {
				if err == nil {
					t.Fatal("配置失敗を成功と報告")
				}
				if mode == "receipt-restore" {
					if !strings.Contains(err.Error(), "旧版: "+p.Target.Path) || !strings.Contains(err.Error(), filepath.Join(p.StageDir, "previous-receipt.json")) {
						t.Fatal("実在する旧版と退避した管理記録を案内しない", err)
					}
					old, _ := os.ReadFile(p.Target.Path)
					if !bytes.Equal(old, original) {
						t.Fatal("旧版の正式パスへの復元が未完了")
					}
					if _, e := a.inspectSelfUpdateTarget(); e == nil || !strings.Contains(e.Error(), filepath.Join(p.StageDir, "previous-receipt.json")) {
						t.Fatal("次回の更新でも復旧先が不明", e)
					}
					return
				}
				if mode == "rollback" {
					if !strings.Contains(err.Error(), "復元") || !strings.Contains(err.Error(), p.StageDir) {
						t.Fatal("具体的な復旧先がない", err)
					}
					return
				}
				data, _ := os.ReadFile(p.Target.Path)
				gotReceipt, _ := os.ReadFile(p.Target.Receipt.Path)
				if !bytes.Equal(data, original) || !bytes.Equal(gotReceipt, receipt) {
					t.Fatal("旧版/記録を復元できない", err)
				}
			}
			for _, path := range preserved {
				b, _ := os.ReadFile(path)
				if string(b) != "keep setting" {
					t.Fatal("csql以外の設定を変更", path)
				}
			}
			p.Close()
			if _, err := os.Stat(filepath.Join(a.Paths.ConfigDir, ".path-lock")); !os.IsNotExist(err) {
				t.Fatal("更新ロックが残存")
			}
		})
	}
}

func TestSelfUpdateCLI(t *testing.T) {
	for _, mode := range []string{"yes", "decline", "latest", "http", "noninteractive", "help", "args"} {
		t.Run(mode, func(t *testing.T) {
			a, out, _ := testApp(t)
			p := selfUpdatePrepared(t, a)
			p.Close()
			exe, _ := os.Executable()
			data, _ := os.ReadFile(exe)
			sum := sha256.Sum256(data)
			asset := map[string]any{"name": "csql-0.8.0-" + a.OS + "-" + a.Arch, "size": len(data), "state": "uploaded", "digest": "sha256:" + hex.EncodeToString(sum[:])}
			release := map[string]any{"tag_name": "v0.8.0", "assets": []any{asset}}
			if mode == "latest" {
				release["tag_name"] = "v" + version
			}
			body, _ := json.Marshal(release)
			calls := 0
			a.HTTP = &http.Client{Transport: updateTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				status := 200
				payload := data
				if r.URL.Host == "api.github.com" {
					payload = body
				}
				if mode == "http" {
					status = 403
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(payload)), ContentLength: int64(len(payload)), Header: make(http.Header)}, nil
			})}
			a.TTY = mode != "noninteractive"
			a.In = strings.NewReader("n\n")
			if mode == "yes" {
				a.In = strings.NewReader("y\n")
			}
			args := []string{"update"}
			if mode == "help" {
				args = append(args, "--help")
			}
			if mode == "args" {
				args = append(args, "extra")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			code := a.Run(ctx, args)
			switch mode {
			case "yes":
				if code != 0 || !strings.Contains(out.String(), "0.8.0") {
					t.Fatal("更新できない", code, out)
				}
			case "decline", "latest", "help":
				if code != 0 {
					t.Fatal("安全な取消/照会で失敗", code, out)
				}
			default:
				if code == 0 {
					t.Fatal("無効な更新を受理")
				}
			}
			if mode == "help" || mode == "args" || mode == "noninteractive" {
				if calls != 0 {
					t.Fatal("無効な呼出しで通信")
				}
			}
			contents, _ := os.ReadFile(p.Target.Path)
			if mode == "yes" {
				if !bytes.Equal(contents, data) {
					t.Fatal("更新先が異なる")
				}
			} else if string(contents) != "previous csql binary" {
				t.Fatal("未承認で配置")
			}
		})
	}
}

// UI用にも本物の取得/検証を通し、HTTP待機だけを境界で制御する。
func selfUpdateUIFixture(t *testing.T, a *App) (chan struct{}, chan struct{}, *atomic.Bool) {
	t.Helper()
	p := selfUpdatePrepared(t, a)
	p.Close()
	exe, _ := os.Executable()
	data, _ := os.ReadFile(exe)
	sum := sha256.Sum256(data)
	asset := map[string]any{"name": "csql-0.8.0-" + a.OS + "-" + a.Arch, "size": len(data), "state": "uploaded", "digest": "sha256:" + hex.EncodeToString(sum[:])}
	body, _ := json.Marshal(map[string]any{"tag_name": "v0.8.0", "assets": []any{asset}})
	gate, started := make(chan struct{}), make(chan struct{})
	var startedOnce sync.Once
	fail := &atomic.Bool{}
	a.HTTP = &http.Client{Transport: updateTransport(func(r *http.Request) (*http.Response, error) {
		payload := body
		if r.URL.Host != "api.github.com" {
			startedOnce.Do(func() { close(started) })
			select {
			case <-gate:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
			if fail.Load() {
				return nil, errors.New("fixture offline")
			}
			payload = data
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(payload)), ContentLength: int64(len(payload)), Header: make(http.Header)}, nil
	})}
	return gate, started, fail
}

// 再起動後だけがstdinを読み、古い接続引数を引き継がない。
func runSelfUpdateRestartFixture() int {
	if os.Getenv("CSQL_TEST_SELF_UPDATE_RESTART") == "parent" {
		os.Setenv("CSQL_TEST_SELF_UPDATE_RESTART", "child")
		a := NewApp()
		if err := restartSelfUpdate(a, os.Getenv("CSQL_TEST_SELF_UPDATE_PATH")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	b, _ := io.ReadAll(os.Stdin)
	data, _ := json.Marshal(map[string]any{"args": os.Args[1:], "input": string(b)})
	fmt.Fprintln(os.Stdout, string(data))
	if os.Getenv("CSQL_TEST_SELF_UPDATE_EXIT") == "1" {
		return 7
	}
	return 0
}
func TestSelfUpdateRestartContract(t *testing.T) {
	exe, _ := os.Executable()
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			cmd := exec.Command(exe, "--profile", "old-profile")
			cmd.Env = append(os.Environ(), "CSQL_TEST_SELF_UPDATE_RESTART=parent", "CSQL_TEST_SELF_UPDATE_PATH="+exe)
			if failed {
				cmd.Env = append(cmd.Env, "CSQL_TEST_SELF_UPDATE_EXIT=1")
			}
			cmd.Stdin = strings.NewReader("child input")
			out, err := cmd.Output()
			if (err != nil) != failed {
				t.Fatal("子の終了結果が異なる", err)
			}
			var r struct {
				Args  []string
				Input string
			}
			if json.Unmarshal(out, &r) != nil || len(r.Args) != 0 || r.Input != "child input" {
				t.Fatalf("端末/引数の引継ぎが異なる: %s", out)
			}
		})
	}
}
