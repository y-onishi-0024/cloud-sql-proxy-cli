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
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/muesli/termenv"
)

func TestSelectorEditingAndResize(t *testing.T) {
	rows := []Instance{}
	for i := 0; i < 25; i++ {
		rows = append(rows, Instance{Name: fmt.Sprintf("db-%d-長いインスタンス名", i), Project: "long-project-name", Region: "asia-northeast1", DatabaseVersion: "POSTGRES_18"})
	}
	var m tea.Model = selector{rows: rows}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 60, Height: 16})
	view := m.View()
	if len(strings.Split(strings.TrimSuffix(view, "\n"), "\n")) > 16 {
		t.Fatal("画面の高さを超えて描画")
	}
	for _, line := range strings.Split(view, "\n") {
		if ansi.StringWidth(line) > 60 {
			t.Fatalf("画面幅を超えて描画: %q", line)
		}
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("存在しない")})
	if len(m.(selector).filtered()) != 0 {
		t.Fatal("絞り込みできない")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	if m.(selector).query != "" || len(m.(selector).filtered()) != 25 {
		t.Fatal("入力全消去で候補が復元しない")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ab")})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.(selector).query != "b" {
		t.Fatalf("カーソル位置の文字を削除しない: %q", m.(selector).query)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDelete})
	if m.(selector).query != "" {
		t.Fatal("Deleteが残留")
	}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	if !strings.Contains(m.View(), "60列") {
		t.Fatal("狭い画面の操作説明なし")
	}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 90, Height: 25})
	if !strings.Contains(m.View(), "インスタンス") {
		t.Fatal("リサイズから復帰しない")
	}
}

// Bubble Teaの端末判定に使う出力ハンドルがラッパーで消えると、幅・高さが届かない。
func TestSelectionTerminalOutput(t *testing.T) {
	a, _, _ := testApp(t)
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	a.Out = f
	output, ok := a.terminalOutput().(term.File)
	if !ok || output.Fd() != f.Fd() {
		t.Fatal("TUIへ渡す出力から端末ハンドルが失われた")
	}
}

// 実端末の検証専用。gcloud/Proxyをfixtureへ置き換え、GCPには接続しない。
func runUIFixture() int {
	a := NewApp()
	root := os.Getenv("CSQL_TEST_UI_DIR")
	a.Paths = Paths{filepath.Join(root, "config"), filepath.Join(root, "cache"), filepath.Join(root, "tools")}
	// PTYでも利用者の実クリップボードへ触れず、コピー結果を一時領域で観測する。
	a.CopyText = func(ctx context.Context, text string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(root, "copy-fail")); err == nil {
			return fmt.Errorf("fixture copy failure")
		}
		return os.WriteFile(filepath.Join(root, "copied.txt"), []byte(text), 0600)
	}

	exe, _ := os.Executable()
	// このfixtureだけで公開APIと管理領域を代替し、実インストール・外部HTTPへ触れない。
	if os.Getenv("CSQL_TEST_UI_UPDATE") == "1" {
		data, err := os.ReadFile(exe)
		if err != nil {
			fmt.Fprintln(a.Err, err)
			return 1
		}
		sum := sha256.Sum256(data)
		managed := filepath.Join(a.Paths.ConfigDir, "bin", "csql")
		if a.OS == "windows" {
			managed += ".exe"
		}
		if _, err := os.Stat(managed); os.IsNotExist(err) {
			os.MkdirAll(filepath.Dir(managed), 0700)
			os.WriteFile(managed, data, 0700)
			writeJSON(filepath.Join(a.Paths.ConfigDir, "binary-install.json"), binaryReceipt{hex.EncodeToString(sum[:])})
		}
		a.Executable = func() (string, error) { return managed, nil }
		os.Setenv("CSQL_TEST_SELF_UPDATE_VERSION", "0.8.0")
		if os.Getenv("CSQL_TEST_UI_UPDATED") == "1" {
			b, _ := json.Marshal(os.Args[1:])
			os.WriteFile(filepath.Join(root, "updated-args.json"), b, 0600)
		}
		a.HTTP = &http.Client{Transport: updateTransport(func(r *http.Request) (*http.Response, error) {
			payload := data
			code := 200
			if r.URL.Host == "api.github.com" {
				tag := "v0.8.0"
				if os.Getenv("CSQL_TEST_UI_UPDATED") == "1" {
					tag = "v" + version
				}
				name := "csql-0.8.0-" + a.OS + "-" + a.Arch
				if a.OS == "windows" {
					name += ".exe"
				}
				payload, _ = json.Marshal(map[string]any{"tag_name": tag, "assets": []any{map[string]any{"name": name, "size": len(data), "state": "uploaded", "digest": "sha256:" + hex.EncodeToString(sum[:])}}})
			} else {
				os.WriteFile(filepath.Join(root, "update-download-started"), []byte("started"), 0600)
				for {
					if _, err := os.Stat(filepath.Join(root, "update-download-gate")); os.IsNotExist(err) {
						break
					}
					select {
					case <-r.Context().Done():
						return nil, r.Context().Err()
					case <-time.After(20 * time.Millisecond):
					}
				}
				if _, err := os.Stat(filepath.Join(root, "update-fail")); err == nil {
					code = 503
				}
			}
			return &http.Response{StatusCode: code, Body: io.NopCloser(bytes.NewReader(payload)), ContentLength: int64(len(payload)), Header: make(http.Header)}, nil
		})}
		a.RestartSelf = func(path string) error { os.Setenv("CSQL_TEST_UI_UPDATED", "1"); return restartSelfUpdate(a, path) }
	}
	a.LookPath = func(name string) (string, error) {
		if name == "gcloud" {
			return "/fixture/gcloud", nil
		}
		if name == "cloud-sql-proxy" {
			return exe, nil
		}
		return "", os.ErrNotExist
	}
	a.Runner = runnerFunc(func(ctx context.Context, e Executable, args []string) ([]byte, error) {
		text := strings.Join(args, " ")
		if args[0] == "--version" {
			if e.Path == exe {
				return []byte("cloud-sql-proxy version 2.24.1"), nil
			}
			return []byte("Google Cloud SDK 589.0.0"), nil
		}
		if args[0] == "--help" {
			return []byte("--address --port --private-ip --structured-logs --run-connection-test"), nil
		}
		if strings.HasPrefix(text, "config list") {
			return []byte(`{"core":{"account":"fixture@example.invalid"}}`), nil
		}
		if strings.Contains(text, "configurations list") {
			return []byte("fixture"), nil
		}
		if strings.HasPrefix(text, "projects list") {
			return []byte(`[{"projectId":"fixture-dev"},{"projectId":"fixture-prod"}]`), nil
		}
		if strings.HasPrefix(text, "sql instances list") {
			project := "fixture-dev"
			for _, arg := range args {
				if strings.HasPrefix(arg, "--project=") {
					project = strings.TrimPrefix(arg, "--project=")
				}
			}
			var rows []Instance
			for _, name := range []string{"sample-primary-db", "app-primary-database", "app-reporting-database", "warehouse-db"} {
				rows = append(rows, Instance{project, name, "asia-northeast2", project + ":asia-northeast2:" + name, "POSTGRES_18"})
			}
			return json.Marshal(rows)
		}
		return nil, fmt.Errorf("想定外のfixtureコマンド: %v", args)
	})
	return a.Run(context.Background(), os.Args[1:])
}

func TestConnectUITransitions(t *testing.T) {
	a, _, _ := testApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &connectUI{a: a, ctx: ctx, cancelAll: cancel, state: "list", generation: 2, selector: selector{rows: []Instance{testInstance()}}}
	_, _ = m.Update(uiResult{generation: 1, kind: "search", result: DiscoveryResult{Instances: []Instance{{Name: "stale"}}}})
	if m.selector.rows[0].Name != "db" {
		t.Fatal("取消済みの古い検索結果を表示")
	}
	m.openForm(Profile{Instance: testInstance()})
	m.fields[0].value = "dev"
	m.fields[1].value = "app"
	m.fields[2].value = "reader"
	m.fields[3].value = "55437"
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	rows, err := a.LoadProfiles()
	if err != nil || len(rows) != 0 {
		t.Fatal("フォーム取消で保存")
	}
	// 保存Cmdがまだ走っていない間の取消で、副作用を始めない。
	m.openForm(Profile{Instance: testInstance()})
	m.fields[0].value = "dev"
	m.fields[1].value = "app"
	m.fields[2].value = "reader"
	m.fields[3].value = "55437"
	cmd := m.saveForm()
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	result := cmd()
	_, _ = m.Update(result)
	rows, err = a.LoadProfiles()
	if err != nil || len(rows) != 0 {
		t.Fatalf("取消後に保存: %+v %v", rows, err)
	}
}

func TestConnectUIExternalCancellation(t *testing.T) {
	for i := 0; i < 20; i++ {
		a, _, _ := testApp(t)
		a.In = nil
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		done := make(chan error, 1)
		go func() { done <- a.runConnectModel(ctx, connectRequest{}, nil) }()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("取消結果: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("未配送Cmdを待ち続けて終了しない")
		}
	}
	// 実行を開始したworkerも、外部取消後には終了を待ってから戻る。
	a, _, _ := testApp(t)
	a.In = nil
	started, stopped := make(chan struct{}), make(chan struct{})
	a.LookPath = func(string) (string, error) { return "/fixture/gcloud", nil }
	a.Runner = runnerFunc(func(ctx context.Context, _ Executable, _ []string) ([]byte, error) {
		close(started)
		defer close(stopped)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.runConnectModel(ctx, connectRequest{}, nil) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("workerが開始しない")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		select {
		case <-stopped:
		default:
			t.Fatal("戻った後にもworkerが残留")
		}
	case <-time.After(time.Second):
		t.Fatal("実行中の外部取消で終了しない")
	}
}

// Proxy終了と保存/出力が重なっても、その操作の完了まで画面を維持する。
func TestConnectUIDelayedActions(t *testing.T) {
	for _, kind := range []string{"save", "export"} {
		t.Run(kind, func(t *testing.T) {
			a, _, _ := testApp(t)
			p := Profile{Name: "reserved", Instance: testInstance(), Database: "app", Username: "reader", Port: 55439}
			if err := a.AddProfile(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			p.Name = "new-profile"
			m := &connectUI{a: a, ctx: context.Background(), state: "connecting", generation: 1, selectedID: 1, connections: []*uiConnection{{id: 1, attempt: 1, profile: p, progress: &connectionProgress{snapshot: connectionSnapshot{Session: SessionStatus{Port: p.Port}}}, running: true, cancel: func() {}}}}
			var pending tea.Cmd
			if kind == "save" {
				m.openForm(p)
				pending = m.saveForm()
			} else {
				m.beginExport("uri")
				m.fields[0].value = filepath.Join(a.Paths.ConfigDir, "export-uri.txt")
				_, pending = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			}
			m.Update(connectionFinished{id: 1, attempt: 1, err: fmt.Errorf("Proxyが異常終了しました")})
			if m.state != "saving" {
				t.Fatalf("Proxy終了で未完了%sの画面を解除: %s", kind, m.state)
			}
			m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
			if m.state != "saving" || m.generation != 1 {
				t.Fatal("保存/出力中に画面移動・再試行")
			}
			_, cmd := m.Update(pending())
			if m.hasOwnedWork() || cmd != nil {
				t.Fatal("遅い完了結果からProxyを再起動")
			}
			if kind == "save" && (m.saved.Name != p.Name || m.selectedConnection().err == nil) {
				t.Fatalf("保存結果/接続失敗を喪失: %+v", m.saved)
			}
			if kind == "export" && !strings.Contains(m.message, "ファイルへ出力しました") {
				t.Fatalf("出力結果を喪失: %s", m.message)
			}
		})
	}
}

func TestConnectUIActivePortAndNarrowEscape(t *testing.T) {
	a, _, _ := testApp(t)
	progress := &connectionProgress{}
	progress.reset(FailureContext{})
	progress.snapshot.Session.Port = 55438
	m := &connectUI{a: a, ctx: context.Background(), state: "connecting", selectedID: 1, connections: []*uiConnection{{id: 1, attempt: 1, running: true, progress: progress, profile: Profile{Instance: testInstance()}}}}
	m.openForm(m.selectedConnection().effectiveProfile())
	m.fields[0].value, m.fields[1].value, m.fields[2].value, m.fields[3].value = "dev", "app", "reader", "55439"
	if cmd := m.saveForm(); cmd != nil || m.state != "form" || !strings.Contains(m.message, "停止") {
		t.Fatal("稼働中とは異なるPortを保存できる")
	}
	for _, state := range []string{"list", "form"} {
		m = &connectUI{state: state, formBack: "list", selector: selector{width: 48, height: 12}}
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if state == "list" && (cmd == nil || !errors.Is(m.resultErr, ErrCancelled)) {
			t.Fatal("狭い一覧の取消結果が130でない")
		}
		if state == "form" && (cmd != nil || m.state != "list") {
			t.Fatal("狭いフォームのEscが元画面へ戻らない")
		}
	}
}

// E→EnterだけでJSONを保存し、別設定・再出力でも既存ファイルを壊さない。
func TestConnectUIPgAdminDefaultExport(t *testing.T) {
	a, _, _ := testApp(t)
	t.Chdir(filepath.Dir(a.Paths.ConfigDir))
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	p := Profile{Name: "dev", Instance: testInstance(), Database: "業務/db", Username: "reader", Port: 55439}
	m := &connectUI{a: a, ctx: context.Background(), state: "connecting", selectedID: 1, connections: []*uiConnection{{id: 1, attempt: 1, profile: p}}}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.fields[0].value == "" {
		t.Fatal("出力パスが空でEnterだけでは保存できない")
	}
	first := m.fields[0].value
	if !filepath.IsAbs(first) || filepath.Dir(first) != dir || filepath.Ext(first) != ".json" {
		t.Fatalf("保存場所と拡張子: %s", first)
	}
	if _, err := os.Lstat(first); !os.IsNotExist(err) {
		t.Fatal("確認前にファイルを作成")
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enterで出力を開始しない")
	}
	m.Update(cmd())
	b, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	var exported struct {
		Servers map[string]struct {
			MaintenanceDB, Username string
			Port                    int
		}
	}
	if err := json.Unmarshal(b, &exported); err != nil {
		t.Fatal(err)
	}
	server := exported.Servers["1"]
	if server.MaintenanceDB != "業務/db" || server.Username != "reader" || server.Port != 55439 {
		t.Fatalf("接続設定を喪失: %+v", server)
	}
	if !strings.Contains(m.message, first) {
		t.Fatal("保存できたパスが完了案内にない")
	}
	// 再度Eを開くと新しい候補になる。取消時は書き込まない。
	if err := os.Mkdir(filepath.Join(dir, "csql-dev-pgadmin-2.json"), 0700); err != nil {
		t.Fatal(err)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	second := m.fields[0].value
	if second == first {
		t.Fatal("既存ファイルと同じ出力先を提案")
	}
	if fi, err := os.Lstat(second); err == nil && fi.IsDir() {
		t.Fatal("既存ディレクトリを出力先に提案")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if _, err := os.Lstat(second); !os.IsNotExist(err) {
		t.Fatal("取消でも出力")
	}
	// 別設定もパスを入力せず出力でき、最初のファイルを保持する。
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.selectedConnection().profile.Name, m.selectedConnection().profile.Database = "prod", "production"
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	third := m.fields[0].value
	if !strings.Contains(filepath.Base(third), "csql-prod-") {
		t.Fatal("別設定の保存先に切り替わらない")
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("別設定の出力を開始しない")
	}
	m.Update(cmd())
	if _, err := os.Stat(third); err != nil {
		t.Fatal(err)
	}
	actual, _ := os.ReadFile(first)
	if string(actual) != string(b) {
		t.Fatal("別設定の出力で元ファイルを上書き")
	}
	// 出力画面を開いた後の競合も上書きせず失敗し、編集して保存し直せる。
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	collision := m.fields[0].value
	if err := os.WriteFile(collision, []byte("keep-existing"), 0600); err != nil {
		t.Fatal(err)
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(cmd())
	actual, _ = os.ReadFile(collision)
	if string(actual) != "keep-existing" || m.state != "export" {
		t.Fatal("確認後の競合を上書き")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	custom := filepath.Join(dir, "手動の出力先.json")
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(custom)})
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(cmd())
	if _, err := os.Stat(custom); err != nil {
		t.Fatal("出力先を編集して保存できない: ", err)
	}
}

// 詳細を右ペインへ出し、縮小・選択変更・全消去でも情報と操作キーを保つ。
func TestRichListLayout(t *testing.T) {
	rows := []Instance{
		{Project: "sample-project", Name: "first-db", Region: "asia-northeast1", ConnectionName: "sample-project:asia-northeast1:first-db", DatabaseVersion: "POSTGRES_18"},
		{Project: "sample-project", Name: "second-db", Region: "us-central1", ConnectionName: "sample-project:us-central1:second-db", DatabaseVersion: "MYSQL_8"},
	}
	s := selector{rows: rows, width: 120, height: 26}
	view := s.View()
	foundRight := false
	for _, line := range strings.Split(ansi.Strip(view), "\n") {
		if at := strings.Index(line, "POSTGRES_18"); at >= 0 && ansi.StringWidth(line[:at]) > 60 {
			foundRight = true
		}
	}
	if !foundRight {
		t.Fatal("広い端末で選択中のDB詳細が右ペインに表示されない")
	}
	model, _ := s.Update(tea.KeyMsg{Type: tea.KeyDown})
	s = model.(selector)
	if !strings.Contains(s.View(), "MYSQL_8") || strings.Contains(s.View(), "POSTGRES_18") {
		t.Fatal("選択変更が詳細に反映しない")
	}
	for _, size := range [][2]int{{120, 26}, {100, 20}, {80, 24}, {60, 16}, {48, 12}} {
		s.width, s.height = size[0], size[1]
		view = s.View()
		lines := strings.Split(strings.TrimSuffix(view, "\n"), "\n")
		if len(lines) > size[1] {
			t.Fatal("画面の高さを超える")
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size[0] {
				t.Fatal("画面幅を超える")
			}
		}
		if size[0] >= 60 && (!strings.Contains(view, "MYSQL_8") || !strings.Contains(lines[len(lines)-1], "Esc")) {
			t.Fatal("小さい画面で選択詳細/操作ガイドが消える")
		}
	}
	s.width, s.height = 120, 26
	model, _ = s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("該当なし")})
	s = model.(selector)
	if !strings.Contains(s.View(), "一致する候補はありません") {
		t.Fatal("検索0件の案内なし")
	}
	model, _ = s.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	s = model.(selector)
	if !strings.Contains(s.View(), "first-db") {
		t.Fatal("全消去後に候補が戻らない")
	}
}

func TestRichStateLayout(t *testing.T) {
	a, _, _ := testApp(t)
	p := Profile{Name: "dev", Instance: testInstance(), Database: "app", Username: "reader", Port: 55439}
	m := &connectUI{a: a, ctx: context.Background(), selector: selector{width: 120, height: 28}, state: "list", profiles: []Profile{p}, tab: tabProfiles}
	if view := m.View(); !strings.Contains(view, "reader") || !strings.Contains(view, "55439") {
		t.Fatal("設定の詳細がない")
	}
	m.selector.width, m.selector.height = 60, 16
	m.openForm(p)
	if view := m.View(); !strings.Contains(view, "Port") || !strings.Contains(view, "Enter") || !strings.Contains(view, "Esc") {
		t.Fatal("保存フォームの入力/キーが欠落")
	}
	m.beginExport("pgadmin")
	if view := m.View(); !strings.Contains(view, "csql-dev-pgadmin") || !strings.Contains(view, "Enter") || !strings.Contains(view, "Esc") {
		t.Fatal("出力先/キーが隠れる")
	}
	m.state = "error"
	m.diagnostic = &Diagnostic{Cause: "ADCの再認証が必要", Next: "gcloud auth application-default login"}
	if view := m.View(); !strings.Contains(view, "application-default") || !strings.Contains(view, "login") || !strings.Contains(view, "ADC") {
		t.Fatal("装飾で原因・次の操作が隠れる")
	}
	m.state, m.searching, m.tickEpoch, m.tickPending = "searching", true, 2, true
	m.diagnostic = nil
	before := m.View()
	_, cmd := m.Update(uiTick{epoch: 1})
	if cmd != nil || m.View() != before {
		t.Fatal("古い試行のアニメーションが継続")
	}
	_, cmd = m.Update(uiTick{epoch: 2})
	if cmd == nil || m.View() == before {
		t.Fatal("検索中の表示が更新されない")
	}
	m.searching = false
	_, cmd = m.Update(uiTick{epoch: 3})
	if cmd != nil {
		t.Fatal("処理終了後もアニメーションが継続")
	}
}

func TestRichCompactProfileAndGuides(t *testing.T) {
	a, _, _ := testApp(t)
	p := Profile{Name: "dev", Instance: testInstance(), Database: "業務DB", Username: "reader-visible", Port: 55439}
	m := &connectUI{a: a, ctx: context.Background(), state: "list", tab: tabProfiles, profiles: []Profile{p}, selector: selector{width: 60, height: 16}}
	for _, value := range []string{"dev", "reader-visible", "業務DB", "127.0.0.1:55439", p.Project, p.Instance.Name} {
		if !strings.Contains(m.View(), value) {
			t.Fatalf("狭い設定一覧から欠落: %s", value)
		}
	}
	m.openForm(p)
	if strings.Contains(m.View(), "Tab →") || strings.Contains(m.View(), "← Tab") {
		t.Fatal("フォームで実行不能な一覧切替を案内")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.focus != 1 {
		t.Fatal("フォームのTab項目移動が壊れた")
	}
	m.beginExport("pgadmin")
	if strings.Contains(m.View(), "Tab →") || strings.Contains(m.View(), "← Tab") {
		t.Fatal("出力画面で一覧切替を案内")
	}
}

// 有効な長い管理版パスも後続手順も、ページ送りで全文に到達できる。
func TestRichLongRecoveryGuide(t *testing.T) {
	m := &connectUI{state: "error", selector: selector{width: 60, height: 16}}
	command := "'/managed/" + strings.Repeat("long-directory/", 45) + "gcloud' auth application-default login"
	m.diagnostic = &Diagnostic{Cause: "ADCの再認証が必要", Next: command + "\n認証後、Proxyを再起動してください。検索用のgcloud auth loginとは別の認証です"}
	var seen strings.Builder
	for i := 0; i < 30; i++ {
		view := m.View()
		for _, line := range strings.Split(ansi.Strip(view), "\n") {
			if strings.HasPrefix(line, "│") {
				seen.WriteString(strings.TrimSpace(strings.Trim(line, "│ ")))
			}
		}
		m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	}
	compact := func(s string) string { return strings.Join(strings.Fields(s), "") }
	all := compact(seen.String())
	if !strings.Contains(all, compact(command)) || !strings.Contains(all, "別の認証です") {
		t.Fatal("ページ送りでも完全なコマンド・後続手順へ到達しない")
	}
	if !strings.Contains(m.View(), "PgUp") {
		t.Fatal("ページ送りの操作案内がない")
	}
}

// 接続中の保存結果も診断と同じページに含め、行数の境界で消さない。
func TestRichRecoveryKeepsSaveResult(t *testing.T) {
	m := &connectUI{state: "error", selector: selector{width: 60, height: 16}, message: "保存しました: dev"}
	m.diagnostic = &Diagnostic{Cause: "ADCの再認証が必要", Next: strings.Repeat("auth action\n", 15) + "final action"}
	seen := map[string]bool{"保存しました: dev": false, "auth action": false, "final action": false}
	for i := 0; i < 8; i++ {
		view := m.View()
		for text := range seen {
			if strings.Contains(view, text) {
				seen[text] = true
			}
		}
		m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	}
	for text, visible := range seen {
		if !visible {
			t.Fatalf("診断ページの境界で表示が消える: %s", text)
		}
	}
}

// Eはツール選択へ進み、背景の接続終了でも対象を保って自動保存先へ進む。
func TestConnectUIExportToolMenu(t *testing.T) {
	for index, tc := range []struct{ format, ext, guide string }{{"pgadmin", ".json", "Import/Export Servers"}, {"dbeaver", ".csv", "Custom"}, {"a5", ".a5dblist", "データベースの追加と削除"}, {"uri", ".txt", "接続URI"}} {
		t.Run(tc.format, func(t *testing.T) {
			a, _, _ := testApp(t)
			t.Chdir(filepath.Dir(a.Paths.ConfigDir))
			p := Profile{Name: "dev", Instance: testInstance(), Database: "業務db", Username: "reader@team", Port: 55439}
			m := &connectUI{a: a, ctx: context.Background(), state: "connecting", selectedID: 1, selector: selector{width: 60, height: 16}, connections: []*uiConnection{{id: 1, attempt: 1, profile: p, running: true, progress: &connectionProgress{snapshot: connectionSnapshot{Session: SessionStatus{Port: p.Port}}}}, {id: 2, attempt: 1, running: true, profile: Profile{Instance: testInstance()}}}}
			m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
			for _, label := range []string{"pgAdmin", "DBeaver", "A5:SQL Mk-2", "接続URI"} {
				if !strings.Contains(m.View(), label) {
					t.Fatalf("Eの選択肢に%sがない: %s", label, m.View())
				}
			}
			m.Update(connectionFinished{id: 2, attempt: 1, err: fmt.Errorf("別の接続が終了")})
			for i := 0; i < index; i++ {
				m.Update(tea.KeyMsg{Type: tea.KeyDown})
			}
			m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			if m.state != "export" || m.exportFormat != tc.format || len(m.fields) != 1 {
				t.Fatal("ツール選択から出力へ進めない")
			}
			file := m.fields[0].value
			if !filepath.IsAbs(file) || filepath.Ext(file) != tc.ext || !strings.Contains(filepath.Base(file), "csql-dev-") {
				t.Fatalf("自動保存先が不正: %s", file)
			}
			if m.editOwner != 1 || m.editAttempt != 1 || m.editProfile.Database != "業務db" || m.editProfile.Port != 55439 {
				t.Fatal("背景終了で出力対象が混線")
			}
			m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			if !strings.Contains(m.View(), "DBeaver") || m.state == "connecting" {
				t.Fatal("出力パスからツール選択へ戻れない")
			}
			if _, err := os.Stat(file); !os.IsNotExist(err) {
				t.Fatal("取消時に出力")
			}
			m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			// 保存Cmd配送前に出力対象が終了しても、完了結果とツール別案内を保持する。
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m.Update(connectionFinished{id: 1, attempt: 1})
			if cmd == nil {
				t.Fatal("Enterだけで出力できない")
			}
			m.Update(cmd())
			if !strings.Contains(m.message, tc.guide) || !strings.Contains(m.message, file) {
				t.Fatalf("ツール別の取込案内がない: %s", m.message)
			}
			if _, err := os.Stat(file); err != nil {
				t.Fatal(err)
			}
			m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			if m.state != "connecting" {
				t.Fatal("ツール選択の取消で接続詳細へ戻れない")
			}
			if view := m.View(); !strings.Contains(view, "E エクスポート") || strings.Contains(view, "U URI") {
				t.Fatal("出力キーが統合されていない")
			}
		})
	}
}

// 非対応の形式はツール選択画面で説明し、Proxyを止めずに別形式を選び直せる。
func TestConnectUIExportUnsupportedTool(t *testing.T) {
	for _, tc := range []struct {
		version, database, fragment string
		index                       int
	}{{"SQLSERVER_2019_STANDARD", "app", "PostgreSQL", 0}, {"POSTGRES_18", "app;other", "セミコロン", 2}} {
		a, _, _ := testApp(t)
		t.Chdir(filepath.Dir(a.Paths.ConfigDir))
		p := Profile{Name: "dev", Instance: testInstance(), Database: tc.database, Username: "reader", Port: 55439}
		p.DatabaseVersion = tc.version
		c := &uiConnection{id: 1, attempt: 1, profile: p, running: true, progress: &connectionProgress{snapshot: connectionSnapshot{Session: SessionStatus{Port: p.Port}}}}
		m := &connectUI{a: a, ctx: context.Background(), state: "connecting", selectedID: 1, connections: []*uiConnection{c}, selector: selector{width: 60, height: 16}, updateVersion: "v0.8.0"}
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
		for i := 0; i < tc.index; i++ {
			m.Update(tea.KeyMsg{Type: tea.KeyDown})
		}
		m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if m.state != "exportTools" || !strings.Contains(m.View(), tc.fragment) || !c.running {
			t.Fatalf("非対応の理由/再選択が失われた: %s", m.View())
		}
		// DBeaverは両ケースで使用できる。選択し直しても接続は維持される。
		direction := tea.KeyDown
		if tc.index == 2 {
			direction = tea.KeyUp
		}
		m.Update(tea.KeyMsg{Type: direction})
		m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if m.state != "export" || m.exportFormat != "dbeaver" || !c.running {
			t.Fatal("別の形式を選び直せない")
		}
	}
}

// テーマによって判別できる内容やセル幅が変わらず、明暗と無色を選べる。
func TestRichDashboardThemes(t *testing.T) {
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(old) })
	rendered := map[string]string{}
	for _, tc := range []struct{ name, theme, bg, noColor string }{{"auto", "auto", "", ""}, {"dark", "dark", "", ""}, {"light", "light", "", ""}, {"auto-dark", "auto", "15;0", ""}, {"auto-light", "auto", "0;15", ""}, {"no-color", "dark", "", "1"}} {
		a, _, _ := testApp(t)
		a.Env = func(key string) string {
			switch key {
			case "CSQL_THEME":
				return tc.theme
			case "COLORFGBG":
				return tc.bg
			case "NO_COLOR":
				return tc.noColor
			}
			return ""
		}
		theme, err := parseUITheme(a.Env)
		if err != nil {
			t.Fatal(err)
		}
		p := Profile{Name: strings.Repeat("日本語", 20), Instance: testInstance(), Database: "orders", Port: 55441}
		m := &connectUI{a: a, ctx: context.Background(), state: "list", tab: tabConnections, connections: []*uiConnection{{id: 1, attempt: 1, profile: p}}, selector: selector{width: 100, height: 20, color: tc.noColor == "", theme: theme}}
		view := m.View()
		rendered[tc.name] = view
		if tc.noColor != "" && strings.Contains(view, "\x1b[") {
			t.Fatal("NO_COLORで色/反転が残った")
		}
		if !strings.Contains(view, "55441") || !strings.Contains(view, "Ctrl+C") {
			t.Fatal("テーマで情報を喪失")
		}
		for _, line := range strings.Split(ansi.Strip(view), "\n") {
			if ansi.StringWidth(line) > 100 {
				t.Fatal("テーマでセル幅を超えた")
			}
		}
	}
	if rendered["auto"] == rendered["dark"] || rendered["dark"] == rendered["light"] || rendered["auto-dark"] != rendered["dark"] || rendered["auto-light"] != rendered["light"] {
		t.Fatal("背景に対応するテーマが選択されない")
	}
	// 不正値は対話開始前に案内し、補助コマンドを巻き込まない。
	for _, args := range [][]string{{}, {"version"}, {"--help"}} {
		a, out, errs := testApp(t)
		a.TTY = true
		a.Env = func(key string) string {
			if key == "CSQL_THEME" {
				return "invalid"
			}
			return ""
		}
		a.Runner = runnerFunc(func(context.Context, Executable, []string) ([]byte, error) {
			t.Error("テーマ検証で外部実行")
			return nil, fmt.Errorf("禁止")
		})
		code := a.Run(context.Background(), args)
		if len(args) == 0 {
			if code == 0 || !strings.Contains(errs.String(), "auto|dark|light") {
				t.Fatalf("不正値の案内なし: %s", errs)
			}
		} else if code != 0 || out.Len() == 0 {
			t.Fatal("非対話コマンドへ影響")
		}
	}
}
