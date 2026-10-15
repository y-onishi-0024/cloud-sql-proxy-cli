package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

type osRunner struct{ Prepare func(*exec.Cmd) }

func (r osRunner) Run(ctx context.Context, e Executable, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, e.Path, append(append([]string{}, e.PrefixArgs...), args...)...)
	if r.Prepare != nil {
		r.Prepare(cmd)
	}
	var stdout, stderr bytes.Buffer
	// 出力容量を制限し、プロセスの終了待ちにも上限を持たせる。
	cmd.Stdout = &limitedWriter{Writer: &stdout, Remaining: 4 << 20}
	cmd.Stderr = &limitedWriter{Writer: &stderr, Remaining: 4 << 20}
	cmd.WaitDelay = 2e9
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w\n%s", filepath.Base(e.Path), err, stderr.String())
	}
	return stdout.Bytes(), nil
}

type limitedWriter struct {
	io.Writer
	Remaining int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if len(p) > w.Remaining {
		return 0, fmt.Errorf("外部出力が上限を超えました")
	}
	n, e := w.Writer.Write(p)
	w.Remaining -= n
	return n, e
}

type Check struct {
	Name, Path, Version, State, Detail string
	Sources                            []Check
}
type Diagnosis struct {
	Checks       []Check
	Account, ADC string
	Ready        bool
}
type installedTool struct{ Version, RelativePath string }
type dependencyError struct {
	kind string
	error
}

func (e dependencyError) Unwrap() error { return e.error }

func (a *App) run(ctx context.Context, e Executable, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, a.CheckTimeout)
	defer cancel()
	return a.Runner.Run(ctx, e, args)
}
func (a *App) toolExecutable(ctx context.Context, path, name string) (Executable, error) {
	e := Executable{Path: path}
	if a.OS == "windows" && name == "gcloud" && (strings.EqualFold(filepath.Ext(path), ".cmd") || strings.EqualFold(filepath.Ext(path), ".bat")) {
		root := filepath.Dir(filepath.Dir(path))
		script := filepath.Join(root, "lib", "gcloud.py")
		if _, err := os.Stat(script); err != nil {
			return e, fmt.Errorf("gcloud.py が見つかりません")
		}
		python := filepath.Join(root, "platform", "bundledpython", "python.exe")
		if _, err := os.Stat(python); err != nil {
			var err error
			python, err = a.python(ctx)
			if err != nil {
				return e, err
			}
		}
		e = Executable{Path: python, PrefixArgs: []string{script}}
	}
	return e, nil
}
func (a *App) python(ctx context.Context) (string, error) {
	names := []string{"python3", "python"}
	if p := a.Env("CLOUDSDK_PYTHON"); p != "" {
		names = append([]string{p}, names...)
	}
	for _, name := range names {
		p, err := a.LookPath(name)
		if err != nil {
			continue
		}
		out, err := a.run(ctx, Executable{Path: p}, "--version")
		if err != nil {
			continue
		}
		m := regexp.MustCompile(`Python 3\.(\d+)\.`).FindStringSubmatch(string(out))
		if len(m) == 2 {
			var minor int
			fmt.Sscan(m[1], &minor)
			if minor >= 10 && minor <= 15 {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("Python 3.10〜3.15 が必要です。公式gcloud導入手順を確認してください")
}
func (a *App) checkPath(ctx context.Context, path, name string) (Executable, string, error) {
	e, err := a.toolExecutable(ctx, path, name)
	if err != nil {
		return e, "", err
	}
	out, err := a.run(ctx, e, "--version")
	if err != nil {
		return e, "", err
	}
	v := cleanText(strings.TrimSpace(string(out)))
	if name == "cloud-sql-proxy" {
		if !regexp.MustCompile(`\bversion 2\.\d+\.\d+\b`).MatchString(v) {
			return e, v, dependencyError{"非対応", fmt.Errorf("Proxy v2が必要です")}
		}
		help, err := a.run(ctx, e, "--help")
		if err != nil {
			return e, v, err
		}
		for _, flag := range []string{"--address", "--port", "--private-ip", "--structured-logs", "--run-connection-test"} {
			if !bytes.Contains(help, []byte(flag)) {
				return e, v, dependencyError{"非対応", fmt.Errorf("必要なオプション %s がありません", flag)}
			}
		}
	} else if !regexp.MustCompile(`Google Cloud SDK \d+\.\d+\.\d+`).MatchString(v) {
		return e, v, dependencyError{"非対応", fmt.Errorf("gcloudの版を識別できません")}
	}
	return e, v, nil
}
func (a *App) resolve(ctx context.Context, name string) (Executable, error) {
	e, _, err := a.resolveDetail(ctx, name)
	return e, err
}
func (a *App) resolveDetail(ctx context.Context, name string) (Executable, string, error) {
	var failures []error
	var failedExe Executable
	var failedVersion string
	if path, err := a.LookPath(name); err == nil {
		e, v, err := a.checkPath(ctx, path, name)
		if err == nil {
			return e, v, nil
		}
		failures = append(failures, err)
		failedExe, failedVersion = e, v
	}
	metaPath := filepath.Join(a.Paths.ToolsDir, name, "installed.json")
	var m installedTool
	if readJSONFile(metaPath, &m) == nil && m.Version != "" && safeRelative(m.RelativePath) {
		path := filepath.Join(a.Paths.ToolsDir, name, m.RelativePath)
		e, v, err := a.checkPath(ctx, path, name)
		if err == nil {
			return e, v, nil
		}
		failures = append(failures, err)
		failedExe, failedVersion = e, v
	}
	if len(failures) > 0 {
		return failedExe, failedVersion, fmt.Errorf("%s を起動できない、または非対応です: %w。csql setup を実行してください", name, errors.Join(failures...))
	}
	return Executable{}, "", dependencyError{"未導入・PATH未設定", fmt.Errorf("%s が未導入、またはPATH未設定です。csql setup を実行してください", name)}
}
func (a *App) Diagnose(ctx context.Context) Diagnosis {
	d := Diagnosis{Ready: true, Account: "要確認", ADC: "要確認"}
	for _, name := range []string{"gcloud", "cloud-sql-proxy"} {
		e, v, err := a.resolveDetail(ctx, name)
		c := Check{Name: name, Path: e.Path, Version: v, State: "準備済み"}
		c.Sources = a.sourceChecks(ctx, name)
		if err != nil {
			c.State = "起動不能"
			var de dependencyError
			if errors.As(err, &de) {
				c.State = de.kind
			}
			if errors.Is(err, context.DeadlineExceeded) {
				c.State = "時間切れ"
			}
			c.Detail = err.Error()
			d.Ready = false
		}
		d.Checks = append(d.Checks, c)
	}
	if g, err := a.resolve(ctx, "gcloud"); err == nil {
		out, err := a.run(ctx, g, "auth", "list", "--filter=status:ACTIVE", "--format=json", "--quiet")
		var accounts []struct{ Account string }
		if err == nil && json.Unmarshal(out, &accounts) == nil {
			if len(accounts) > 0 {
				d.Account = "アクティブアカウントあり（有効性未検証）"
			} else {
				d.Account = "未ログイン"
				d.Ready = false
			}
		} else {
			d.Ready = false
		}
	}
	credential := a.Env("GOOGLE_APPLICATION_CREDENTIALS")
	if credential == "" {
		base := a.gcloudDir()
		credential = filepath.Join(base, "application_default_credentials.json")
	}
	if info, err := os.Stat(credential); err == nil && info.Mode().IsRegular() {
		d.ADC = "認証ファイルあり（有効性未検証）"
	} else {
		d.ADC = "要確認（ファイル未確認。環境のADC利用も未検証）"
		d.Ready = false
	}
	return d
}
func (a *App) gcloudDir() string {
	if d := a.Env("CLOUDSDK_CONFIG"); d != "" {
		return d
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(a.Env("APPDATA"), "gcloud")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "gcloud")
}
func (a *App) printDiagnosis(d Diagnosis) {
	fmt.Fprintf(a.stdout(), "環境: %s/%s\n", a.OS, a.Arch)
	for _, c := range d.Checks {
		fmt.Fprintf(a.stdout(), "%s: %s\n", c.Name, c.State)
		if c.Path != "" {
			fmt.Fprintf(a.stdout(), "  %s\n  %s\n", c.Path, c.Version)
		}
		if c.Detail != "" {
			fmt.Fprintf(a.stdout(), "  %s\n", c.Detail)
		}
		for _, source := range c.Sources {
			fmt.Fprintf(a.stdout(), "  [%s] %s\n", source.Name, source.State)
			if source.Path != "" {
				fmt.Fprintf(a.stdout(), "    %s\n", source.Path)
			}
			if source.Version != "" {
				fmt.Fprintf(a.stdout(), "    %s\n", source.Version)
			}
			if source.Detail != "" {
				fmt.Fprintf(a.stdout(), "    %s\n", source.Detail)
			}
		}
	}
	fmt.Fprintf(a.stdout(), "gcloud認証: %s\nADC: %s\nIAM・DBログイン・ネットワーク: 未検証\n", d.Account, d.ADC)
}

func (a *App) sourceChecks(ctx context.Context, name string) []Check {
	makeCheck := func(source, path string, err error) Check {
		c := Check{Name: source, Path: path, State: "準備済み"}
		if err == nil {
			_, c.Version, err = a.checkPath(ctx, path, name)
		}
		if err != nil {
			c.State = "起動不能"
			var de dependencyError
			if errors.As(err, &de) {
				c.State = de.kind
			}
			if errors.Is(err, os.ErrNotExist) {
				c.State = "未導入"
			}
			if errors.Is(err, context.DeadlineExceeded) {
				c.State = "時間切れ"
			}
			c.Detail = err.Error()
		}
		return c
	}
	path, err := a.LookPath(name)
	checks := []Check{makeCheck("PATH", path, err)}
	var meta installedTool
	err = readJSONFile(filepath.Join(a.Paths.ToolsDir, name, "installed.json"), &meta)
	path = ""
	if err == nil {
		if meta.Version == "" || !safeRelative(meta.RelativePath) {
			err = fmt.Errorf("管理版の記録が不正です")
		} else {
			path = filepath.Join(a.Paths.ToolsDir, name, meta.RelativePath)
		}
	}
	return append(checks, makeCheck("管理領域", path, err))
}

func secureDir(path string) error {
	// 既存のリンクを追わず、管理ディレクトリをユーザー専用にする。
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for p := abs; ; p = filepath.Dir(p) {
		if fi, e := os.Lstat(p); e == nil && fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("管理領域にシンボリックリンクがあります: %s", p)
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	if err := os.MkdirAll(abs, 0700); err != nil {
		return err
	}
	return os.Chmod(abs, 0700)
}
func writeJSON(path string, value any) error {
	if err := secureDir(filepath.Dir(path)); err != nil {
		return err
	}
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("リンク先へ書き込めません")
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".csql-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
func safeRelative(s string) bool {
	return s != "" && !filepath.IsAbs(s) && !strings.Contains(s, "\\") && !strings.Contains(s, ":") && filepath.Clean(s) != ".." && !strings.HasPrefix(filepath.Clean(s), ".."+string(filepath.Separator))
}
func readJSONFile(path string, value any) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || fi.Size() > 4<<20 {
		return fmt.Errorf("管理JSONが不正です")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(io.LimitReader(f, 4<<20)).Decode(value)
}
