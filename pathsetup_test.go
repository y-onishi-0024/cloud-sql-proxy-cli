package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// 実ホームを使わず、設定の保存と別シェルからの名前解決を検証する。
func pathTestApp(t *testing.T) (*App, string, *bytes.Buffer) {
	t.Helper()
	a, out, _ := testApp(t)
	home := filepath.Join(t.TempDir(), "home 日本語 '$`(x)")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	home, _ = filepath.EvalSymlinks(home)
	a.Env = func(k string) string {
		if k == "HOME" {
			return home
		}
		if k == "SHELL" {
			return "/bin/zsh"
		}
		return ""
	}
	a.Paths.ConfigDir = filepath.Join(home, "config '$`(injection)")
	a.Paths.ToolsDir = filepath.Join(a.Paths.ConfigDir, "tools")
	source := filepath.Join(t.TempDir(), "source-csql")
	if err := os.WriteFile(source, []byte("#!/bin/sh\necho fixture-csql\n"), 0700); err != nil {
		t.Fatal(err)
	}
	a.Executable = func() (string, error) { return source, nil }
	a.TTY = true
	a.In = strings.NewReader("y\n")
	return a, home, out
}

func TestSetupPathPersistsWithoutDuplicating(t *testing.T) {
	for _, shell := range []string{"zsh", "bash"} {
		t.Run(shell, func(t *testing.T) {
			if _, err := exec.LookPath(shell); err != nil {
				t.Skip("対象シェルなし")
			}
			a, home, out := pathTestApp(t)
			rc := ".zshrc"
			if shell == "bash" {
				rc = ".bashrc"
			}
			original := []byte("# 利用者の設定\nexport KEEP_USER_SETTING=ok\n")
			if err := os.WriteFile(filepath.Join(home, rc), original, 0640); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				a.In = strings.NewReader("y\n")
				if code := a.Run(context.Background(), []string{"setup", "--path", "--shell", shell}); code != 0 {
					t.Fatalf("PATH設定失敗: %d\n%s", code, out)
				}
			}
			content, err := os.ReadFile(filepath.Join(home, rc))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.HasPrefix(content, original) || strings.Count(string(content), "# >>> csql PATH >>>") != 1 {
				t.Fatalf("設定破損/重複: %s", content)
			}
			fi, _ := os.Stat(filepath.Join(home, rc))
			if fi.Mode().Perm() != 0640 {
				t.Fatal("既存権限を変更")
			}
			backups, _ := filepath.Glob(filepath.Join(home, rc+".csql-backup-*"))
			if len(backups) != 1 {
				t.Fatalf("バックアップ: %v", backups)
			}
			b, _ := os.ReadFile(backups[0])
			if !bytes.Equal(b, original) {
				t.Fatal("元設定のバックアップ不一致")
			}
			cmd := exec.Command(shell, "-ic", "command -v csql; printf '%s' \"$KEEP_USER_SETTING\"")
			cmd.Env = []string{"HOME=" + home, "ZDOTDIR=" + home, "PATH=/usr/bin:/bin", "TERM=dumb"}
			got, err := cmd.CombinedOutput()
			if err != nil || !bytes.Contains(got, []byte(filepath.Join(a.Paths.ConfigDir, "bin", "csql"))) || !bytes.Contains(got, []byte("ok")) {
				t.Fatalf("新規シェルで解決不可: %s %v", got, err)
			}
		})
	}
}

func TestSetupPathDeclineDoesNotWrite(t *testing.T) {
	for _, mode := range []string{"decline", "non-tty", "context"} {
		t.Run(mode, func(t *testing.T) {
			a, home, _ := pathTestApp(t)
			a.In = strings.NewReader("n\n")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "non-tty" {
				a.TTY = false
			}
			if mode == "context" {
				cancel()
			}
			code := a.Run(ctx, []string{"setup", "--path"})
			if mode == "decline" && code != 0 {
				t.Fatalf("取消code=%d", code)
			}
			if mode != "decline" && code == 0 {
				t.Fatal("非TTY/取消を成功扱い")
			}
			entries, _ := os.ReadDir(home)
			if len(entries) != 0 {
				t.Fatal("確認前に設定を書いた")
			}
			if _, err := os.Stat(filepath.Join(a.Paths.ConfigDir, "bin")); !os.IsNotExist(err) {
				t.Fatal("確認前にコピーした")
			}
		})
	}
}

func TestSetupPathRejectsUnsafeChanges(t *testing.T) {
	for _, mode := range []string{"symlink", "parent-link", "bad-marker", "fish-owned", "unknown-binary", "unsafe-dir", "concurrent-edit", "lock"} {
		t.Run(mode, func(t *testing.T) {
			a, home, out := pathTestApp(t)
			rc := filepath.Join(home, ".zshrc")
			shell := "zsh"
			switch mode {
			case "symlink":
				target := filepath.Join(home, "target")
				os.WriteFile(target, []byte("original"), 0600)
				os.Symlink(target, rc)
			case "parent-link":
				real := filepath.Join(home, "real")
				os.Mkdir(real, 0700)
				link := filepath.Join(home, "link")
				os.Symlink(real, link)
				baseEnv := a.Env
				a.Env = func(k string) string {
					if k == "ZDOTDIR" {
						return link
					}
					return baseEnv(k)
				}
			case "bad-marker":
				os.WriteFile(rc, []byte(pathStart+"\nuser command\n"), 0600)
			case "fish-owned":
				shell = "fish"
				rc = filepath.Join(home, ".config", "fish", "conf.d", "csql.fish")
				os.MkdirAll(filepath.Dir(rc), 0700)
				os.WriteFile(rc, []byte("echo user setting\n"), 0600)
			case "unknown-binary":
				p := filepath.Join(a.Paths.ConfigDir, "bin", "csql")
				os.MkdirAll(filepath.Dir(p), 0700)
				os.WriteFile(p, []byte("unrelated"), 0700)
			case "unsafe-dir":
				a.Paths.ConfigDir += "\nmalicious"
			case "concurrent-edit":
				os.WriteFile(rc, []byte("original\n"), 0600)
				a.In = &editingReader{edit: func() { os.WriteFile(rc, []byte("edited by user\n"), 0600) }}
			case "lock":
				os.MkdirAll(filepath.Join(a.Paths.ConfigDir, ".path-lock"), 0700)
			}
			before, _ := os.ReadFile(rc)
			code := a.Run(context.Background(), []string{"setup", "--path", "--shell", shell})
			if code == 0 {
				t.Fatalf("危険な変更を許可: %s", out)
			}
			after, _ := os.ReadFile(rc)
			if mode == "concurrent-edit" {
				if string(after) != "edited by user\n" {
					t.Fatal("利用者編集を上書き")
				}
			} else if !bytes.Equal(before, after) {
				t.Fatal("拒否時に元設定を変更")
			}
		})
	}
}

type editingReader struct {
	edit func()
	done bool
}

func (r *editingReader) Read(p []byte) (int, error) {
	if !r.done {
		r.edit()
		r.done = true
		return copy(p, "y\n"), nil
	}
	return 0, io.EOF
}

// 同じ版の再実行と版更新が、利用者のファイルを上書きしないことを確認する。
func TestSetupPathBinaryUpdateOwnership(t *testing.T) {
	a, _, out := pathTestApp(t)
	src, _ := a.Executable()
	if code := a.Run(context.Background(), []string{"setup", "--path"}); code != 0 {
		t.Fatalf("初回: %s", out)
	}
	os.WriteFile(src, []byte("#!/bin/sh\necho new-version\n"), 0700)
	a.In = strings.NewReader("y\n")
	if code := a.Run(context.Background(), []string{"setup", "--path"}); code != 0 {
		t.Fatalf("版更新: %s", out)
	}
	dest := filepath.Join(a.Paths.ConfigDir, "bin", "csql")
	b, _ := os.ReadFile(dest)
	if !bytes.Contains(b, []byte("new-version")) {
		t.Fatal("版更新なし")
	}
	os.WriteFile(dest, []byte("user replaced"), 0700)
	a.In = strings.NewReader("y\n")
	if code := a.Run(context.Background(), []string{"setup", "--path"}); code == 0 {
		t.Fatal("所有権のない版を上書き")
	}
}

func managedPathFixture(t *testing.T, a *App, name, version string) string {
	t.Helper()
	rel := version + "/" + name
	if name == "gcloud" {
		rel = version + "/google-cloud-sdk/bin/gcloud"
		if a.OS == "windows" {
			rel += ".cmd"
		}
	}
	if name == "cloud-sql-proxy" && a.OS == "windows" {
		rel += ".exe"
	}
	p := filepath.Join(a.Paths.ToolsDir, name, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0700)
	text := "#!/bin/sh\necho 'cloud-sql-proxy version " + version + " --address --port --private-ip --structured-logs --run-connection-test'\n"
	if name == "gcloud" {
		text = "#!/bin/sh\necho 'Google Cloud SDK " + version + "'\n"
	}
	if err := os.WriteFile(p, []byte(text), 0700); err != nil {
		t.Fatal(err)
	}
	if a.OS == "windows" && name == "gcloud" {
		sdk := filepath.Dir(filepath.Dir(p))
		os.MkdirAll(filepath.Join(sdk, "lib"), 0700)
		os.WriteFile(filepath.Join(sdk, "lib", "gcloud.py"), []byte("fixture"), 0600)
		os.MkdirAll(filepath.Join(sdk, "platform", "bundledpython"), 0700)
		os.WriteFile(filepath.Join(sdk, "platform", "bundledpython", "python.exe"), []byte("fixture"), 0700)
	}
	if err := writeJSON(filepath.Join(a.Paths.ToolsDir, name, "installed.json"), installedTool{version, rel}); err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(p)
}

func TestSetupPathManagedToolsAndPrecedence(t *testing.T) {
	a, home, out := pathTestApp(t)
	a.Runner = osRunner{}
	g := managedPathFixture(t, a, "gcloud", "589.0.0")
	p := managedPathFixture(t, a, "cloud-sql-proxy", "2.24.1")
	oldBin := filepath.Join(home, "existing")
	os.Mkdir(oldBin, 0700)
	os.WriteFile(filepath.Join(oldBin, "gcloud"), []byte("#!/bin/sh\necho existing\n"), 0700)
	for _, shell := range []string{"zsh", "bash"} {
		if _, err := exec.LookPath(shell); err != nil {
			continue
		}
		a.In = strings.NewReader("y\n")
		if code := a.Run(context.Background(), []string{"setup", "--path", "--shell", shell}); code != 0 {
			t.Fatalf("設定: %s", out)
		}
		cmd := exec.Command(shell, "-ic", "command -v csql; command -v gcloud; command -v cloud-sql-proxy")
		cmd.Env = []string{"HOME=" + home, "ZDOTDIR=" + home, "TERM=dumb", "PATH=" + oldBin + ":/usr/bin:/bin"}
		got, err := cmd.CombinedOutput()
		if err != nil || !bytes.Contains(got, []byte(filepath.Join(oldBin, "gcloud"))) || !bytes.Contains(got, []byte(filepath.Join(p, "cloud-sql-proxy"))) {
			t.Fatalf("既存優先/追加解決: %s %v", got, err)
		}
	}
	newer := managedPathFixture(t, a, "gcloud", "590.0.0")
	a.In = strings.NewReader("y\n")
	if code := a.Run(context.Background(), []string{"setup", "--path"}); code != 0 {
		t.Fatalf("更新: %s", out)
	}
	cmd := exec.Command("zsh", "-ic", "command -v gcloud; printf '%s' \"$PATH\"")
	cmd.Env = []string{"HOME=" + home, "ZDOTDIR=" + home, "TERM=dumb", "PATH=/usr/bin:/bin"}
	got, err := cmd.CombinedOutput()
	if err != nil || bytes.Contains(got, []byte(g)) || !bytes.Contains(got, []byte(newer)) {
		t.Fatalf("管理版の旧PATH更新: %s %v", got, err)
	}
}

type memoryUserPath struct {
	value                userPathValue
	failSave, failNotify bool
}

func (s *memoryUserPath) Read() (userPathValue, error) { return s.value, nil }
func (s *memoryUserPath) Save(old, next userPathValue) error {
	if s.failSave {
		return fmt.Errorf("保存失敗")
	}
	if s.value != old {
		return fmt.Errorf("競合")
	}
	s.value = next
	return nil
}
func (s *memoryUserPath) Notify() error {
	if s.failNotify {
		return fmt.Errorf("通知失敗")
	}
	return nil
}

func TestSetupPathWindowsPreservesUserPath(t *testing.T) {
	a, _, out := pathTestApp(t)
	a.OS = "windows"
	existing := filepath.Join(a.Paths.ConfigDir, "bin")
	store := &memoryUserPath{value: userPathValue{Value: `%USERPROFILE%\Existing;` + strings.ToUpper(existing) + `\`, Kind: 2, Exists: true}}
	a.UserPath = store
	a.Runner = runnerFunc(func(_ context.Context, e Executable, args []string) ([]byte, error) {
		if strings.HasSuffix(e.Path, "python.exe") {
			return []byte("Google Cloud SDK 589.0.0"), nil
		}
		return []byte("cloud-sql-proxy version 2.24.1 --address --port --private-ip --structured-logs --run-connection-test"), nil
	})
	g := managedPathFixture(t, a, "gcloud", "589.0.0")
	p := managedPathFixture(t, a, "cloud-sql-proxy", "2.24.1")
	for i := 0; i < 2; i++ {
		a.In = strings.NewReader("y\n")
		if code := a.Run(context.Background(), []string{"setup", "--path"}); code != 0 {
			t.Fatalf("Windows設定: %s", out)
		}
	}
	if store.value.Kind != 2 || !strings.HasPrefix(store.value.Value, `%USERPROFILE%\Existing;`) || strings.Count(store.value.Value, g) != 1 || strings.Count(store.value.Value, p) != 1 {
		t.Fatalf("元値/種類/重複: %+v", store.value)
	}
	next := managedPathFixture(t, a, "gcloud", "590.0.0")
	a.In = strings.NewReader("y\n")
	if code := a.Run(context.Background(), []string{"setup", "--path"}); code != 0 {
		t.Fatalf("更新: %s", out)
	}
	if strings.Contains(store.value.Value, g) || !strings.Contains(store.value.Value, next) || !strings.Contains(store.value.Value, strings.ToUpper(existing)) {
		t.Fatalf("所有する旧版だけ更新できない: %+v", store.value)
	}
	backups, _ := filepath.Glob(filepath.Join(a.Paths.ConfigDir, "windows-path.csql-backup-*"))
	if len(backups) != 2 {
		t.Fatalf("必要な変更だけバックアップ: %v", backups)
	}
	var old userPathValue
	if err := readJSONFile(backups[0], &old); err != nil || !old.Exists || old.Kind != 2 {
		t.Fatalf("元の型を保存できない: %+v %v", old, err)
	}
}

func TestSetupPathWindowsFailuresArePartial(t *testing.T) {
	for _, mode := range []string{"save", "notify", "concurrent"} {
		t.Run(mode, func(t *testing.T) {
			a, _, out := pathTestApp(t)
			a.OS = "windows"
			store := &memoryUserPath{value: userPathValue{Value: "original", Kind: 1, Exists: true}, failSave: mode == "save", failNotify: mode == "notify"}
			a.UserPath = store
			if mode == "concurrent" {
				a.In = &editingReader{edit: func() { store.value.Value = "user changed" }}
			}
			code := a.Run(context.Background(), []string{"setup", "--path"})
			if code == 0 {
				t.Fatal("失敗を全体成功扱い")
			}
			if strings.Contains(out.String(), "PATH設定を保存しました。") {
				t.Fatal("全体成功と表示")
			}
			if mode == "save" && store.value.Value != "original" {
				t.Fatal("保存失敗時に元値を変更")
			}
			if mode == "concurrent" && store.value.Value != "user changed" {
				t.Fatal("確認中の変更を上書き")
			}
			if mode == "notify" && !strings.Contains(store.value.Value, filepath.Join(a.Paths.ConfigDir, "bin")) {
				t.Fatal("通知失敗と保存失敗の混同")
			}
		})
	}
}

func TestSetupPathPreviewDoesNotPrintExistingSettings(t *testing.T) {
	a, home, out := pathTestApp(t)
	os.WriteFile(filepath.Join(home, ".zshrc"), []byte("export PRIVATE_SETTING=do-not-print\n"), 0600)
	a.In = strings.NewReader("n\n")
	if code := a.Run(context.Background(), []string{"setup", "--path"}); code != 0 {
		t.Fatal(code)
	}
	if strings.Contains(out.String(), "PRIVATE_SETTING") {
		t.Fatal("変更案へ無関係な既存設定を出力")
	}
}

func TestSetupPathRejectsChangedToolRecord(t *testing.T) {
	a, home, _ := pathTestApp(t)
	a.Runner = osRunner{}
	managedPathFixture(t, a, "gcloud", "589.0.0")
	a.In = &editingReader{edit: func() { managedPathFixture(t, a, "gcloud", "590.0.0") }}
	if code := a.Run(context.Background(), []string{"setup", "--path"}); code == 0 {
		t.Fatal("確認した版と異なる管理記録を許可")
	}
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); !os.IsNotExist(err) {
		t.Fatal("未確認の版で設定を書いた")
	}
}

func TestSetupPathRejectsMalformedStartMarker(t *testing.T) {
	a, home, _ := pathTestApp(t)
	os.WriteFile(filepath.Join(home, ".zshrc"), []byte(pathStart+" user data\n"+pathEnd+"\n"), 0600)
	if code := a.Run(context.Background(), []string{"setup", "--path"}); code == 0 {
		t.Fatal("不正な開始マーカーを所有設定と扱った")
	}
}

func TestSetupPathCLICombinedConfirmations(t *testing.T) {
	for _, mode := range []string{"accept", "decline-install", "decline-path", "install-fail"} {
		t.Run(mode, func(t *testing.T) {
			a, home, out := pathTestApp(t)
			a.In = strings.NewReader("y\ny\n")
			if mode == "decline-install" {
				a.In = strings.NewReader("n\ny\n")
			}
			if mode == "decline-path" {
				a.In = strings.NewReader("y\nn\n")
			}
			body := []byte("#!/bin/sh\necho 'cloud-sql-proxy version 2.24.1 --address --port --private-ip --structured-logs --run-connection-test'\n")
			hash := sha256.Sum256(body)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
			defer srv.Close()
			a.AllowTestHTTP = true
			a.Runner = osRunner{}
			a.Artifacts = []Artifact{{Name: "cloud-sql-proxy", Version: "2.24.1", URL: srv.URL, SHA256: hex.EncodeToString(hash[:]), Format: "binary"}}
			if mode == "install-fail" {
				a.Artifacts[0].SHA256 = strings.Repeat("0", 64)
			}
			code := a.Run(context.Background(), []string{"setup", "--install", "--path"})
			if (mode == "install-fail") != (code != 0) {
				t.Fatalf("結果code=%d: %s", code, out)
			}
			_, rcErr := os.Stat(filepath.Join(home, ".zshrc"))
			_, toolErr := os.Stat(filepath.Join(a.Paths.ToolsDir, "cloud-sql-proxy", "installed.json"))
			if (mode == "accept") != (rcErr == nil) {
				t.Fatalf("PATH確認の結果不一致: %s", out)
			}
			if (mode == "accept" || mode == "decline-path") != (toolErr == nil) {
				t.Fatal("導入取消/失敗でも管理版が公開された")
			}
		})
	}
}

func TestSetupPathCLIArgumentValidation(t *testing.T) {
	for _, args := range [][]string{{"setup", "--shell", "zsh"}, {"setup", "--path", "--shell", "sh"}, {"setup", "--path", "--shell", ""}} {
		a, home, _ := pathTestApp(t)
		if code := a.Run(context.Background(), args); code != 2 {
			t.Fatalf("不正引数code=%d args=%v", code, args)
		}
		files, _ := os.ReadDir(home)
		if len(files) != 0 {
			t.Fatal("引数検証前に書き込み")
		}
	}
	a, _, _ := pathTestApp(t)
	a.OS = "windows"
	if code := a.Run(context.Background(), []string{"setup", "--path", "--shell", "zsh"}); code != 2 {
		t.Fatal("Windowsでshell指定を受理")
	}
}

func TestSetupPathFishConfigAndUnsupportedShell(t *testing.T) {
	a, home, out := pathTestApp(t)
	for i := 0; i < 2; i++ {
		a.In = strings.NewReader("y\n")
		if code := a.Run(context.Background(), []string{"setup", "--path", "--shell", "fish"}); code != 0 {
			t.Fatalf("fish設定: %s", out)
		}
	}
	config := filepath.Join(home, ".config", "fish", "conf.d", "csql.fish")
	b, err := os.ReadFile(config)
	if err != nil || strings.Count(string(b), pathStart) != 1 || !strings.Contains(string(b), "fish_add_path --append --path") {
		t.Fatalf("fish専用設定: %s %v", b, err)
	}
	if p, err := exec.LookPath("fish"); err == nil {
		cmd := exec.Command(p, "-ic", "command -s csql")
		cmd.Env = []string{"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"), "TERM=dumb", "PATH=/usr/bin:/bin"}
		got, err := cmd.CombinedOutput()
		if err != nil || !bytes.Contains(got, []byte(filepath.Join(a.Paths.ConfigDir, "bin", "csql"))) {
			t.Fatalf("fishの名前解決: %s %v", got, err)
		}
	} else {
		t.Log("fish実行環境なし。設定保存のみ検証")
	}
	base := a.Env
	a.Env = func(k string) string {
		if k == "SHELL" {
			return "/bin/unsupported"
		}
		return base(k)
	}
	a.In = strings.NewReader("y\n")
	if code := a.Run(context.Background(), []string{"setup", "--path"}); code == 0 {
		t.Fatal("未対応シェルを成功扱い")
	}
}

func TestSetupPathCancellationWhileWaiting(t *testing.T) {
	a, home, _ := pathTestApp(t)
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	a.In = r
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- a.Run(ctx, []string{"setup", "--path"}) }()
	cancel()
	select {
	case code := <-done:
		if code != 130 {
			t.Fatalf("取消code=%d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("入力待ちを取消できない")
	}
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); !os.IsNotExist(err) {
		t.Fatal("取消後に設定")
	}
}

func TestSetupPathRechecksUnchangedBinary(t *testing.T) {
	a, _, out := pathTestApp(t)
	if code := a.Run(context.Background(), []string{"setup", "--path"}); code != 0 {
		t.Fatalf("初回: %s", out)
	}
	target := filepath.Join(a.Paths.ConfigDir, "bin", "csql")
	a.In = &editingReader{edit: func() { os.WriteFile(target, []byte("concurrent replacement"), 0700) }}
	if code := a.Run(context.Background(), []string{"setup", "--path"}); code == 0 {
		t.Fatal("変更予定がなくても確認中の置換を見逃した")
	}
}

type callbackWriter struct {
	io.Writer
	after func(string)
}

func (w callbackWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.after(string(p))
	return n, err
}
func TestSetupPathReportsPartialFileChanges(t *testing.T) {
	a, home, out := pathTestApp(t)
	profile := filepath.Join(home, ".bash_profile")
	os.WriteFile(profile, []byte("# original\n"), 0600)
	a.Out = callbackWriter{Writer: out, after: func(s string) {
		if strings.Contains(s, "設定済み: "+filepath.Join(home, ".bashrc")) {
			os.WriteFile(profile, []byte("# concurrent user edit\n"), 0600)
		}
	}}
	code := a.Run(context.Background(), []string{"setup", "--path", "--shell", "bash"})
	if code == 0 || strings.Contains(out.String(), "PATH設定を保存しました。") {
		t.Fatal("部分完了を全体成功と表示")
	}
	rc, _ := os.ReadFile(filepath.Join(home, ".bashrc"))
	b, _ := os.ReadFile(profile)
	if !bytes.Contains(rc, []byte(pathStart)) || string(b) != "# concurrent user edit\n" {
		t.Fatal("適用済み/未適用の状態不一致")
	}
}

func TestSetupPathShellStartupSelection(t *testing.T) {
	for _, mode := range []string{"zsh-zdotdir", "bash-login"} {
		t.Run(mode, func(t *testing.T) {
			a, home, out := pathTestApp(t)
			base := a.Env
			shell := "bash"
			target := filepath.Join(home, ".bash_login")
			if mode == "zsh-zdotdir" {
				shell = "zsh"
				dir := filepath.Join(home, "zsh custom")
				os.Mkdir(dir, 0700)
				target = filepath.Join(dir, ".zshrc")
				a.Env = func(k string) string {
					if k == "ZDOTDIR" {
						return dir
					}
					return base(k)
				}
			}
			os.WriteFile(target, []byte("# retained\n"), 0600)
			if code := a.Run(context.Background(), []string{"setup", "--path", "--shell", shell}); code != 0 {
				t.Fatalf("対象設定: %s", out)
			}
			b, _ := os.ReadFile(target)
			if !bytes.Contains(b, []byte(pathStart)) {
				t.Fatal("対象の起動ファイルを設定しなかった")
			}
			cmd := exec.Command(shell, "-lic", "command -v csql")
			cmd.Env = []string{"HOME=" + home, "TERM=dumb", "PATH=/usr/bin:/bin"}
			if mode == "zsh-zdotdir" {
				cmd.Env = append(cmd.Env, "ZDOTDIR="+filepath.Dir(target))
			}
			got, err := cmd.CombinedOutput()
			if err != nil || !bytes.Contains(got, []byte(filepath.Join(a.Paths.ConfigDir, "bin", "csql"))) {
				t.Fatalf("ログインシェルから呼べない: %s %v", got, err)
			}
			if mode == "bash-login" {
				if _, err := os.Stat(filepath.Join(home, ".bash_profile")); !os.IsNotExist(err) {
					t.Fatal("既存bash_loginを隠すprofileを作成")
				}
			}
		})
	}
}

func TestSetupPathWindowsKeepsPreexistingExpandedEntry(t *testing.T) {
	a, _, out := pathTestApp(t)
	a.OS = "windows"
	originalEnv := a.Env
	a.Env = func(k string) string {
		if k == "BINROOT" {
			return a.Paths.ConfigDir
		}
		return originalEnv(k)
	}
	original := `%BINROOT%\bin`
	store := &memoryUserPath{value: userPathValue{Value: original, Kind: 2, Exists: true}}
	a.UserPath = store
	for i := 0; i < 2; i++ {
		a.In = strings.NewReader("y\n")
		if code := a.Run(context.Background(), []string{"setup", "--path"}); code != 0 {
			t.Fatalf("展開値保持: %s", out)
		}
	}
	if store.value.Value != original {
		t.Fatalf("展開済みの既存PATHを置換/重複: %+v", store.value)
	}
}

func TestSetupPathRejectsLiteralPercentInExpandablePath(t *testing.T) {
	a, home, out := pathTestApp(t)
	a.OS = "windows"
	a.Paths.ConfigDir = filepath.Join(home, "config %CSQL_ALIAS%")
	a.Paths.ToolsDir = filepath.Join(a.Paths.ConfigDir, "tools")
	store := &memoryUserPath{value: userPathValue{Value: "original", Kind: 2, Exists: true}}
	a.UserPath = store
	if code := a.Run(context.Background(), []string{"setup", "--path"}); code == 0 {
		t.Fatalf("展開で配置先が変わる登録を成功扱い: %s", out)
	}
	if store.value.Value != "original" {
		t.Fatal("拒否前にユーザーPATHを変更")
	}
	if _, err := os.Stat(filepath.Join(a.Paths.ConfigDir, "bin")); !os.IsNotExist(err) {
		t.Fatal("拒否前にバイナリを配置")
	}
}

func TestSetupPathWindowsDoesNotExpandREGSZ(t *testing.T) {
	a, _, out := pathTestApp(t)
	a.OS = "windows"
	base := a.Env
	a.Env = func(k string) string {
		if k == "BINROOT" {
			return a.Paths.ConfigDir
		}
		return base(k)
	}
	original := `%BINROOT%\bin`
	store := &memoryUserPath{value: userPathValue{Value: original, Kind: 1, Exists: true}}
	a.UserPath = store
	if code := a.Run(context.Background(), []string{"setup", "--path"}); code != 0 {
		t.Fatalf("REG_SZ設定: %s", out)
	}
	if store.value.Value != original+";"+filepath.Join(a.Paths.ConfigDir, "bin") || store.value.Kind != 1 {
		t.Fatalf("展開しない既存値を実ディレクトリと誤認: %+v", store.value)
	}
}

func TestSetupPathRepairsIdenticalBinaryPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unixの実行権限")
	}
	a, _, out := pathTestApp(t)
	if code := a.Run(context.Background(), []string{"setup", "--path"}); code != 0 {
		t.Fatalf("初回: %s", out)
	}
	dest := filepath.Join(a.Paths.ConfigDir, "bin", "csql")
	if err := os.Chmod(dest, 0644); err != nil {
		t.Fatal(err)
	}
	a.In = strings.NewReader("y\n")
	if code := a.Run(context.Background(), []string{"setup", "--path"}); code != 0 {
		t.Fatalf("権限修復: %s", out)
	}
	fi, _ := os.Stat(dest)
	if fi.Mode().Perm() != 0700 {
		t.Fatalf("実行権限なしのまま成功: %o", fi.Mode().Perm())
	}
	cmd := exec.Command(dest)
	if b, err := cmd.CombinedOutput(); err != nil || !bytes.Contains(b, []byte("fixture-csql")) {
		t.Fatalf("再利用版を実行不能: %s %v", b, err)
	}
}

func TestSetupPathWindowsPreservesEquivalentUserAddedEntry(t *testing.T) {
	a, _, out := pathTestApp(t)
	a.OS = "windows"
	store := &memoryUserPath{value: userPathValue{Value: "existing", Kind: 1, Exists: true}}
	a.UserPath = store
	a.Runner = runnerFunc(func(_ context.Context, e Executable, args []string) ([]byte, error) {
		return []byte("cloud-sql-proxy version 2.24.1 --address --port --private-ip --structured-logs --run-connection-test"), nil
	})
	old := managedPathFixture(t, a, "cloud-sql-proxy", "2.24.1")
	if code := a.Run(context.Background(), []string{"setup", "--path"}); code != 0 {
		t.Fatalf("初回: %s", out)
	}
	userSpelling := strings.ToUpper(old) + `\`
	// 利用者が別表記で追加した項目を原文・順序のまま保持する。
	store.value.Value += ";" + userSpelling
	newer := managedPathFixture(t, a, "cloud-sql-proxy", "2.25.0")
	a.In = strings.NewReader("y\n")
	if code := a.Run(context.Background(), []string{"setup", "--path"}); code != 0 {
		t.Fatalf("更新: %s", out)
	}
	if !strings.Contains(store.value.Value, ";"+userSpelling) || !strings.HasSuffix(store.value.Value, ";"+newer) {
		t.Fatalf("ユーザー追加項目を削除: %s", store.value.Value)
	}
}

func TestSetupPathWindowsRejectsAmbiguousOwnedPosition(t *testing.T) {
	for _, position := range []string{"prepend", "append"} {
		t.Run(position, func(t *testing.T) {
			a, _, out := pathTestApp(t)
			a.OS = "windows"
			store := &memoryUserPath{value: userPathValue{Value: "existing", Kind: 1, Exists: true}}
			a.UserPath = store
			a.Runner = runnerFunc(func(_ context.Context, e Executable, args []string) ([]byte, error) {
				return []byte("cloud-sql-proxy version 2.24.1 --address --port --private-ip --structured-logs --run-connection-test"), nil
			})
			old := managedPathFixture(t, a, "cloud-sql-proxy", "2.24.1")
			if code := a.Run(context.Background(), []string{"setup", "--path"}); code != 0 {
				t.Fatalf("初回: %s", out)
			}
			if position == "prepend" {
				store.value.Value = old + ";" + store.value.Value
			} else {
				store.value.Value += ";" + old
			}
			before := store.value
			managedPathFixture(t, a, "cloud-sql-proxy", "2.25.0")
			out.Reset()
			a.In = strings.NewReader("y\n")
			if code := a.Run(context.Background(), []string{"setup", "--path"}); code == 0 {
				t.Fatal("所有位置が不明でも変更した")
			}
			if store.value != before {
				t.Fatalf("利用者の優先順位を変更: before=%s after=%s", before.Value, store.value.Value)
			}
			if strings.Contains(out.String(), "この内容でPATHを設定しますか") {
				t.Fatal("曖昧さの検出が確認後")
			}
		})
	}
}
