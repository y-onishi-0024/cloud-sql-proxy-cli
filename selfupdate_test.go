package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
