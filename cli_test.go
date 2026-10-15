package main

import (
	"bytes"
	"context"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type runnerFunc func(context.Context, Executable, []string) ([]byte, error)

func (f runnerFunc) Run(c context.Context, e Executable, args []string) ([]byte, error) {
	return f(c, e, args)
}

func TestDoctorShowsPATHAndManagedVersions(t *testing.T) {
	a, out, _ := testApp(t)
	a.LookPath = func(n string) (string, error) { return "/path/" + n, nil }
	managed := filepath.Join(a.Paths.ToolsDir, "cloud-sql-proxy", "2.24.1", "proxy")
	if err := writeJSON(filepath.Join(a.Paths.ToolsDir, "cloud-sql-proxy", "installed.json"), installedTool{"2.24.1", "2.24.1/proxy"}); err != nil {
		t.Fatal(err)
	}
	a.Runner = runnerFunc(func(_ context.Context, e Executable, args []string) ([]byte, error) {
		if e.Path == "/path/cloud-sql-proxy" {
			return []byte("cloud-sql-proxy version 1.0.0"), nil
		}
		if e.Path == managed {
			if args[0] == "--help" {
				return []byte("--address --port --private-ip --structured-logs --run-connection-test"), nil
			}
			return []byte("cloud-sql-proxy version 2.24.1"), nil
		}
		if args[0] == "--version" {
			return []byte("Google Cloud SDK 589.0.0"), nil
		}
		return []byte(`[]`), nil
	})
	a.printDiagnosis(a.Diagnose(context.Background()))
	for _, want := range []string{"/path/cloud-sql-proxy", "非対応", managed, "PATH", "管理領域"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("診断で環境差が消えました: %s", out)
		}
	}
}

func TestDoctorPreservesFailureDetails(t *testing.T) {
	for _, mode := range []string{"unsupported", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			a, _, _ := testApp(t)
			a.CheckTimeout = 10 * time.Millisecond
			a.LookPath = func(n string) (string, error) {
				if n == "cloud-sql-proxy" {
					return "/old/cloud-sql-proxy", nil
				}
				return "", os.ErrNotExist
			}
			a.Runner = runnerFunc(func(c context.Context, _ Executable, _ []string) ([]byte, error) {
				if mode == "timeout" {
					<-c.Done()
					return nil, c.Err()
				}
				return []byte("cloud-sql-proxy version 1.0.0"), nil
			})
			d := a.Diagnose(context.Background())
			c := d.Checks[1]
			want := "非対応"
			if mode == "timeout" {
				want = "時間切れ"
			}
			if c.State != want || c.Path != "/old/cloud-sql-proxy" {
				t.Fatalf("原因とパスが失われた: %+v", c)
			}
		})
	}
}

func TestSelectionAndNonTTY(t *testing.T) {
	rows := []Instance{{Name: "db", Project: "alpha"}, {Name: "db-prod", Project: "beta"}}
	a, _, _ := testApp(t)
	if _, err := a.SelectInstance(context.Background(), rows); err == nil {
		t.Fatal("非TTYで暗黙選択")
	}
	var model tea.Model = selector{rows: rows}
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("prod")})
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	picked := model.(selector).picked
	if picked == nil || picked.Project != "beta" {
		t.Fatalf("絞り込み選択: %+v", picked)
	}
	model = selector{rows: rows}
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if model.(selector).picked.Project != "beta" {
		t.Fatal("上下キーの選択")
	}
	model = selector{rows: rows}
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !model.(selector).cancelled {
		t.Fatal("取消されません")
	}
}

func testApp(t *testing.T) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	a := NewApp()
	a.Paths = Paths{filepath.Join(root, "config"), filepath.Join(root, "cache"), filepath.Join(root, "tools")}
	a.In = strings.NewReader("")
	a.Out = out
	a.Err = errOut
	a.Env = func(string) string { return "" }
	a.LookPath = func(name string) (string, error) { return "", os.ErrNotExist }
	a.Runner = runnerFunc(func(context.Context, Executable, []string) ([]byte, error) {
		return nil, fmt.Errorf("想定外の外部実行")
	})
	return a, out, errOut
}

func TestCLIWithoutDependencies(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"setup"}, {"status"}} {
		a, _, errs := testApp(t)
		if code := a.Run(context.Background(), args); code != 0 {
			t.Fatalf("%v code=%d: %s", args, code, errs)
		}
	}
	a, _, errs := testApp(t)
	if code := a.Run(context.Background(), []string{"connect", "db"}); code != 1 || !strings.Contains(errs.String(), "csql setup") {
		t.Fatalf("不足時の案内: %d %s", code, errs)
	}
}

func TestCLIBadArguments(t *testing.T) {
	for _, args := range [][]string{{"stop"}, {"stop", "--all", "id"}, {"db", "--port", "0"}, {"connect", "a", "b"}, {"--unknown"}} {
		a, _, _ := testApp(t)
		if got := a.Run(context.Background(), args); got != 2 {
			t.Errorf("%v code=%d", args, got)
		}
	}
}

func TestDoctorMissingAndUnusable(t *testing.T) {
	a, out, _ := testApp(t)
	if got := a.Run(context.Background(), []string{"doctor"}); got != 1 || !strings.Contains(out.String(), "gcloud") {
		t.Fatalf("診断: %d %s", got, out)
	}
}
