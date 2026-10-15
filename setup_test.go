package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSetupConfirmAndIntegrity(t *testing.T) {
	for _, mode := range []string{"success", "cancel", "non-tty", "bad-hash", "traversal", "start-fail"} {
		t.Run(mode, func(t *testing.T) {
			a, _, _ := testApp(t)
			a.TTY = true
			a.In = strings.NewReader("y\n")
			if mode == "cancel" {
				a.In = strings.NewReader("n\n")
			}
			if mode == "non-tty" {
				a.TTY = false
			}
			var body bytes.Buffer
			gz := gzip.NewWriter(&body)
			tw := tar.NewWriter(gz)
			name := "google-cloud-sdk/bin/gcloud"
			if mode == "traversal" {
				name = "../outside"
			}
			content := []byte("fake executable")
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			tw.Write(content)
			tw.Close()
			gz.Close()
			hash := sha256.Sum256(body.Bytes())
			var requested atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requested.Add(1); w.Write(body.Bytes()) }))
			defer srv.Close()
			a.AllowTestHTTP = true
			a.Artifacts = []Artifact{{Name: "gcloud", Version: "589.0.0", File: "gcloud.tar.gz", URL: srv.URL, SHA256: hex.EncodeToString(hash[:]), Format: "tar.gz"}}
			if mode == "bad-hash" {
				a.Artifacts[0].SHA256 = strings.Repeat("0", 64)
			}
			a.Runner = runnerFunc(func(_ context.Context, e Executable, args []string) ([]byte, error) {
				if mode == "start-fail" {
					return nil, fmt.Errorf("起動失敗")
				}
				if _, err := os.Stat(e.Path); err != nil {
					return nil, err
				}
				return []byte("Google Cloud SDK 589.0.0"), nil
			})
			err := a.Setup(context.Background(), true)
			meta := filepath.Join(a.Paths.ToolsDir, "gcloud", "installed.json")
			_, statErr := os.Stat(meta)
			if mode == "success" {
				if err != nil || statErr != nil {
					t.Fatalf("導入結果 err=%v file=%v", err, statErr)
				}
			} else {
				if statErr == nil {
					t.Fatalf("失敗・取消時に管理版を公開")
				}
				if mode != "cancel" && err == nil {
					t.Fatal("失敗を検出していません")
				}
			}
			if (mode == "cancel" || mode == "non-tty") && requested.Load() != 0 {
				t.Fatal("確認前に取得しました")
			}
			if _, err := os.Stat(filepath.Join(a.Paths.ToolsDir, "outside")); err == nil {
				t.Fatal("展開ルート外へ書込み")
			}
		})
	}
}

func TestSetupWindowsManagedGcloudUsesPython(t *testing.T) {
	a, _, _ := testApp(t)
	a.OS = "windows"
	a.Arch = "amd64"
	a.TTY = true
	a.In = strings.NewReader("y\n")
	var body bytes.Buffer
	z := zip.NewWriter(&body)
	for _, name := range []string{"google-cloud-sdk/bin/gcloud.cmd", "google-cloud-sdk/lib/gcloud.py", "google-cloud-sdk/platform/bundledpython/python.exe"} {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(0755)
		w, err := z.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte("fixture"))
	}
	z.Close()
	sum := sha256.Sum256(body.Bytes())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body.Bytes()) }))
	defer srv.Close()
	a.AllowTestHTTP = true
	a.Artifacts = []Artifact{{Name: "gcloud", Version: "589.0.0", URL: srv.URL, SHA256: hex.EncodeToString(sum[:]), Format: "zip"}}
	a.Runner = runnerFunc(func(_ context.Context, e Executable, args []string) ([]byte, error) {
		if !strings.HasSuffix(filepath.ToSlash(e.Path), "platform/bundledpython/python.exe") || len(e.PrefixArgs) != 1 || !strings.HasSuffix(filepath.ToSlash(e.PrefixArgs[0]), "lib/gcloud.py") {
			return nil, fmt.Errorf("シェルを介さないSDK/Python起動になっていません: %+v", e)
		}
		if _, err := os.Stat(e.Path); err != nil {
			return nil, err
		}
		return []byte("Google Cloud SDK 589.0.0"), nil
	})
	if err := a.Setup(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.resolve(context.Background(), "gcloud"); err != nil {
		t.Fatalf("管理版を再解決できません: %v", err)
	}
	var meta installedTool
	if err := readJSONFile(filepath.Join(a.Paths.ToolsDir, "gcloud", "installed.json"), &meta); err != nil || strings.Contains(meta.RelativePath, "\\") {
		t.Fatalf("保存パス: %+v %v", meta, err)
	}
}

func TestSetupArchiveAndPrerequisiteBoundaries(t *testing.T) {
	for _, mode := range []string{"zip", "zip-drive", "escape-link", "oversize", "python-missing", "locked", "partial"} {
		t.Run(mode, func(t *testing.T) {
			a, _, _ := testApp(t)
			a.TTY = true
			a.In = strings.NewReader("y\n")
			var b bytes.Buffer
			format := "tar.gz"
			if strings.HasPrefix(mode, "zip") {
				format = "zip"
				z := zip.NewWriter(&b)
				name := "google-cloud-sdk/bin/gcloud"
				if mode == "zip-drive" {
					name = "C:/escape"
				}
				h := &zip.FileHeader{Name: name, Method: zip.Deflate}
				h.SetMode(0755)
				w, _ := z.CreateHeader(h)
				w.Write([]byte("fixture"))
				z.Close()
			} else {
				gz := gzip.NewWriter(&b)
				tarw := tar.NewWriter(gz)
				h := &tar.Header{Name: "google-cloud-sdk/bin/gcloud", Mode: 0755, Typeflag: tar.TypeReg, Size: 7}
				if mode == "escape-link" {
					h.Typeflag = tar.TypeSymlink
					h.Linkname = "../../../outside"
					h.Size = 0
				}
				if mode == "oversize" {
					h.Size = downloadLimit + 1
				}
				tarw.WriteHeader(h)
				if h.Typeflag == tar.TypeReg && mode != "oversize" {
					tarw.Write([]byte("fixture"))
				}
				tarw.Close()
				gz.Close()
			}
			hash := sha256.Sum256(b.Bytes())
			var requests atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.Write(b.Bytes()) }))
			defer s.Close()
			a.AllowTestHTTP = true
			a.Artifacts = []Artifact{{Name: "gcloud", Version: "589.0.0", URL: s.URL, SHA256: hex.EncodeToString(hash[:]), Format: format, PythonRequired: mode == "python-missing"}}
			if mode == "partial" {
				a.Artifacts = append(a.Artifacts, Artifact{Name: "cloud-sql-proxy", Version: "2.24.1", URL: s.URL, SHA256: strings.Repeat("0", 64), Format: "binary"})
			}
			a.Runner = runnerFunc(func(_ context.Context, e Executable, _ []string) ([]byte, error) {
				if _, err := os.Stat(e.Path); err != nil {
					return nil, err
				}
				return []byte("Google Cloud SDK 589.0.0"), nil
			})
			lock := filepath.Join(a.Paths.ToolsDir, ".install-lock")
			if mode == "locked" {
				secureDir(a.Paths.ToolsDir)
				if err := os.Mkdir(lock, 0700); err != nil {
					t.Fatal(err)
				}
			}
			err := a.Setup(context.Background(), true)
			_, metaErr := os.Stat(filepath.Join(a.Paths.ToolsDir, "gcloud", "installed.json"))
			success := mode == "zip" || mode == "partial"
			if (metaErr == nil) != success {
				t.Fatalf("管理版の公開状態: %s err=%v meta=%v", mode, err, metaErr)
			}
			if mode == "zip" && err != nil || mode != "zip" && err == nil {
				t.Fatalf("結果: %s err=%v", mode, err)
			}
			if (mode == "python-missing" || mode == "locked") && requests.Load() != 0 {
				t.Fatal("前提を満たさず取得しました")
			}
			if mode == "locked" {
				if _, err := os.Stat(lock); err != nil {
					t.Fatal("他のインストールのロックを削除しました")
				}
			} else {
				if _, err := os.Stat(lock); !os.IsNotExist(err) {
					t.Fatal("自身のロックが残りました")
				}
			}
		})
	}
}

func TestSetupCancellationWhileWaiting(t *testing.T) {
	a, _, _ := testApp(t)
	a.TTY = true
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	a.In = r
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Setup(ctx, true) }()
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("取消結果: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("確認入力待ちから取消できません")
	}
}

func TestSetupReusesExistingCommands(t *testing.T) {
	a, _, _ := testApp(t)
	a.TTY = true
	a.LookPath = func(n string) (string, error) { return "/existing/" + n, nil }
	a.Runner = runnerFunc(func(_ context.Context, e Executable, args []string) ([]byte, error) {
		if strings.Contains(e.Path, "proxy") {
			if args[0] == "--help" {
				return []byte("--address --port --private-ip --structured-logs --run-connection-test"), nil
			}
			return []byte("cloud-sql-proxy version 2.24.1"), nil
		}
		return []byte("Google Cloud SDK 589.0.0"), nil
	})
	if err := a.Setup(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.Paths.ToolsDir); !os.IsNotExist(err) {
		t.Fatal("既存導入を再利用せず書込みました")
	}
}
