package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Bubble Teaと同じくCmdを並行実行し、モデル更新だけをテストの主goroutineで行う。
type connectionsHarness struct {
	t      *testing.T
	m      *connectUI
	events chan tea.Msg
	ctx    context.Context
}

func newConnectionsHarness(t *testing.T, a *App) *connectionsHarness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	first, second := testInstance(), testInstance()
	second.Name, second.ConnectionName = "second", "alpha:asia-northeast1:second"
	m := &connectUI{a: a, ctx: ctx, cancelAll: cancel, state: "list", selector: selector{width: 100, height: 30, rows: []Instance{first, second}}}
	h := &connectionsHarness{t: t, m: m, events: make(chan tea.Msg, 128), ctx: ctx}
	t.Cleanup(func() {
		m.workMu.Lock()
		m.workClosed = true
		m.workMu.Unlock()
		cancel()
		m.workers.Wait()
	})
	return h
}

func (h *connectionsHarness) command(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		select {
		case h.events <- msg:
		case <-h.ctx.Done():
		}
	}()
}

func (h *connectionsHarness) update(msg tea.Msg) {
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, cmd := range batch {
			h.command(cmd)
		}
		return
	}
	_, cmd := h.m.Update(msg)
	h.command(cmd)
}

func (h *connectionsHarness) key(key string) {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	switch key {
	case "enter":
		msg.Type = tea.KeyEnter
	case "esc":
		msg.Type = tea.KeyEsc
	case "tab":
		msg.Type = tea.KeyTab
	case "down":
		msg.Type = tea.KeyDown
	case "f1":
		msg.Type = tea.KeyF1
	case "f2":
		msg.Type = tea.KeyF2
	case "f3":
		msg.Type = tea.KeyF3
	case "ctrl+k":
		msg.Type = tea.KeyCtrlK
	case "up":
		msg.Type = tea.KeyUp
	case "pgdown":
		msg.Type = tea.KeyPgDown
	case "ctrl+c":
		msg.Type = tea.KeyCtrlC
	case "ctrl+s":
		msg.Type = tea.KeyCtrlS
	case "ctrl+e":
		msg.Type = tea.KeyCtrlE
	case "ctrl+l":
		msg.Type = tea.KeyCtrlL
	case "ctrl+r":
		msg.Type = tea.KeyCtrlR
	case "ctrl+o":
		msg.Type = tea.KeyCtrlO
	case "ctrl+d":
		msg.Type = tea.KeyCtrlD
	}
	h.update(msg)
}

// 画面の共通エクスポート入口を通して、対象ツールを選択する。
func (h *connectionsHarness) exportTool(format string) {
	h.key("ctrl+e")
	for _, tool := range exportTools {
		if tool.format == format {
			h.key("enter")
			return
		}
		h.key("down")
	}
	h.t.Fatal("未知の出力形式: ", format)
}

func (h *connectionsHarness) until(text string) {
	h.t.Helper()
	h.pumpUntil(func() bool {
		if text == "Proxy利用可能" {
			c := h.m.actionConnection()
			if c == nil {
				c = h.m.selectedConnection()
			}
			return c != nil && c.status() == text
		}
		return strings.Contains(h.m.View(), text)
	}, text)
}
func (h *connectionsHarness) pumpUntil(done func() bool, label string) {
	h.t.Helper()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for !done() {
		select {
		case msg := <-h.events:
			h.update(msg)
		case <-timeout.C:
			h.t.Fatalf("表示待ち %q: %s", label, h.m.View())
		}
	}
}

// 追加・一覧切替で既存の接続を切らず、停止操作は選択した1件だけに作用する。
// 接続後は一覧に留まり、停止後のPort編集・ログ・再試行を選択行へ適用する。
func TestConnectionsWorkspaceActions(t *testing.T) {
	a := proxyApp(t)
	t.Setenv("CSQL_TEST_BIND", "1")
	p := Profile{Name: "dev", Instance: testInstance(), Database: "app", Username: "reader", Port: 55448}
	if err := a.AddProfile(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	h := newConnectionsHarness(t, a)
	h.m.profiles, _ = a.LoadProfiles()
	h.m.tab = tabProfiles
	h.key("enter")
	h.until("Proxy利用可能")
	if h.m.state != "list" || h.m.tab != tabConnections {
		t.Fatalf("接続後に一覧から離れた: %s", h.m.state)
	}
	h.key("ctrl+l")
	if !strings.Contains(h.m.View(), "ログ") {
		t.Fatal("一覧にログがない")
	}
	h.key("ctrl+e")
	if h.m.state != "exportTools" {
		t.Fatal("一覧から出力できない")
	}
	h.key("esc")
	if h.m.state != "list" {
		t.Fatal("出力取消で一覧へ戻らない")
	}
	h.key("ctrl+d")
	h.until("停止済み")
	h.key("enter")
	for i := 0; i < 3; i++ {
		h.key("down")
	}
	h.key("enter")
	if h.m.state != "portForm" {
		t.Fatalf("Port編集へ進めない: %s", h.m.state)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	h.m.fields[0].value = fmt.Sprint(port)
	h.key("ctrl+s")
	h.until("Portを更新")
	if h.m.state != "list" || h.m.connections[0].effectiveProfile().Port != port {
		t.Fatal("停止済み行が旧Portのまま")
	}
	h.key("ctrl+r")
	h.until("Proxy利用可能")
	session := awaitSession(t, a)
	if session.Port != port {
		t.Fatalf("再試行が旧Port: %+v", session)
	}
}

func TestConnectionsWorkspaceSelection(t *testing.T) {
	a, _, _ := testApp(t)
	h := newConnectionsHarness(t, a)
	first := Profile{Name: "first", Instance: testInstance(), Database: "first-db", Username: "first-user", Port: 55441}
	second := first
	second.Name = "second"
	second.Instance.Name = "second"
	second.ConnectionName = "alpha:asia-northeast1:second"
	second.Database = "second-db"
	second.Port = 55442
	h.m.connections = []*uiConnection{{id: 1, attempt: 1, profile: first, progress: &connectionProgress{}}, {id: 2, attempt: 1, profile: second, progress: &connectionProgress{}}}
	h.m.selectedID = 1
	h.m.tab = tabConnections
	h.key("second")
	h.key("ctrl+s")
	if h.m.state != "form" || h.m.fields[1].value != "second-db" {
		t.Fatal("過去の選択へ保存を向けた")
	}
	h.key("esc")
	if h.m.state != "list" || h.m.selector.query != "second" {
		t.Fatal("検索を失った")
	}
	h.key("ctrl+e")
	h.key("enter")
	if !strings.Contains(h.m.fields[0].value, "second-pgadmin") {
		t.Fatal("過去の行を出力")
	}
	h.update(connectionFinished{id: 1, attempt: 1, err: fmt.Errorf("背景の失敗")})
	if h.m.state != "export" {
		t.Fatal("背景終了が出力を奪った")
	}
	h.key("esc")
	h.key("esc")
	if h.m.selector.query != "second" {
		t.Fatal("出力取消で検索を失った")
	}
	h.key("not-found")
	h.key("ctrl+e")
	h.key("ctrl+d")
	if h.m.state != "list" {
		t.Fatal("0件で過去の行を操作")
	}
	h.key("e")
	if !strings.HasSuffix(h.m.selector.query, "e") {
		t.Fatal("通常文字Eで出力")
	}
	h.update(tea.KeyMsg{Type: tea.KeyCtrlU})
	for i := 0; i < 100; i++ {
		h.m.connections[0].progress.snapshot.Logs = append(h.m.connections[0].progress.snapshot.Logs, fmt.Sprintf("log-%03d", i))
	}
	h.m.message = ""
	h.key("ctrl+l")
	h.update(tea.WindowSizeMsg{Width: 60, Height: 16})
	if body := h.m.View(); !strings.Contains(body, "first · 停止済み") || !strings.Contains(body, "log-099") || !strings.Contains(body, "Ctrl+C") {
		t.Fatalf("狭幅で一覧・ログ・終了案内を喪失: %s", body)
	}
	h.update(tea.KeyMsg{Type: tea.KeyPgUp})
	if strings.Contains(h.m.View(), "log-099") {
		t.Fatal("過去ログへ送れない")
	}
	h.key("down")
	if !strings.Contains(h.m.View(), "second") || strings.Contains(h.m.View(), "log-099") {
		t.Fatal("ログが選択行に追従しない")
	}
}

// 実Proxy終了後、完了通知が未配送でも成功した設定更新を画面に残す。
func TestConnectionsPortUpdateBeforeFinish(t *testing.T) {
	a, _, _ := testApp(t)
	p := Profile{Name: "dev", Instance: testInstance(), Database: "app", Username: "reader", Port: 5432}
	if err := a.AddProfile(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	h := newConnectionsHarness(t, a)
	h.m.profiles, _ = a.LoadProfiles()
	h.m.tab = tabProfiles
	h.m.connections = []*uiConnection{{id: 1, attempt: 1, profile: p, running: true, progress: &connectionProgress{snapshot: connectionSnapshot{Session: SessionStatus{Port: p.Port}}}}}
	h.m.selectedID = 1
	h.key("ctrl+o")
	h.key("ポート")
	h.key("enter")
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	h.m.fields[0].value = fmt.Sprint(port)
	h.key("ctrl+s")
	h.until("Portを更新")
	h.update(connectionFinished{id: 1, attempt: 1, err: context.Canceled})
	h.key("tab")
	h.key("ctrl+e")
	h.key("enter")
	if !strings.Contains(h.m.exportData, fmt.Sprint(port)) {
		t.Fatalf("終了通知後に旧Portへ戻った: %s", h.m.exportData)
	}
}

func TestConnectionsWorkspaceDiagnosticDetail(t *testing.T) {
	a, _, _ := testApp(t)
	h := newConnectionsHarness(t, a)
	h.m.tab = tabConnections
	p := Profile{Name: "dev", Instance: testInstance(), Database: "app", Username: "reader", Port: 5432}
	h.m.connections = []*uiConnection{{id: 1, profile: p, err: fmt.Errorf("specific-config-error"), diagnostic: &Diagnostic{Cause: "操作できません", Next: "設定を確認してください", Detail: "具体的な失敗: specific-config-error"}}}
	h.update(tea.WindowSizeMsg{Width: 60, Height: 16})
	h.key("ctrl+l")
	found := false
	for i := 0; i < 10; i++ {
		if strings.Contains(h.m.View(), "specific-config-error") {
			found = true
			break
		}
		h.update(tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if !found {
		t.Fatal("Proxyログが空の失敗で具体的エラーを表示できない")
	}
}

func TestConnectionsOutputResetsPage(t *testing.T) {
	a, _, _ := testApp(t)
	h := newConnectionsHarness(t, a)
	h.m.tab = tabConnections
	p := Profile{Name: "dev", Instance: testInstance(), Database: "app", Username: "reader", Port: 5432}
	h.m.connections = []*uiConnection{{id: 1, attempt: 1, profile: p, recoveryPage: 1}}
	h.update(tea.WindowSizeMsg{Width: 60, Height: 16})
	h.key("ctrl+e")
	h.key("enter")
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h.m.fields[0].value = filepath.Join(dir, "servers.json")
	h.key("enter")
	h.pumpUntil(func() bool { return h.m.state == "list" }, "出力から一覧へ")
	if !strings.Contains(h.m.View(), "ファイルへ出力しました") {
		t.Fatal("前回のページ位置で新しい完了案内が隠れた")
	}
}

func TestConnectionsSearchNoticeDoesNotCoverWorkspace(t *testing.T) {
	a, _, _ := testApp(t)
	h := newConnectionsHarness(t, a)
	h.m.notices = []string{"検索の注意"}
	h.key("ctrl+l")
	if !strings.Contains(h.m.View(), "検索の詳細") {
		t.Fatal("検索注意を表示できない")
	}
	h.key("enter")
	if strings.Contains(h.m.View(), "検索の詳細") || !strings.Contains(h.m.View(), "接続一覧") {
		t.Fatal("検索注意が接続一覧を覆った")
	}
	h.m.showLogs = true
	if strings.Contains(h.m.View(), "検索の詳細") {
		t.Fatal("別タブの注意状態で接続一覧が隠れる")
	}
}

func TestConnectionsPortFormUsesTargetMetadata(t *testing.T) {
	a, _, _ := testApp(t)
	target := Profile{Name: "target", Instance: testInstance(), Database: "target-db", Username: "reader", Port: 55443}
	target.Project = "target-project"
	target.ConnectionName = "target-project:asia-northeast1:db"
	if err := a.AddProfile(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	h := newConnectionsHarness(t, a)
	old := target
	old.Project = "old-project"
	old.ConnectionName = "old-project:asia-northeast1:db"
	h.m.connections = []*uiConnection{{id: 1, profile: old}}
	h.m.selectedID = 1
	h.m.tab = tabProfiles
	h.m.profiles, _ = a.LoadProfiles()
	h.update(tea.WindowSizeMsg{Width: 60, Height: 16})
	h.key("ctrl+o")
	if body := h.m.View(); strings.Contains(body, "old-project") || !strings.Contains(body, "target-project") {
		t.Fatalf("操作メニューの対象が混線: %s", body)
	}
	h.key("enter")
	if body := h.m.View(); strings.Contains(body, "old-project") || !strings.Contains(body, "target-project") {
		t.Fatalf("Port確認の対象が混線: %s", body)
	}
}

func TestConnectionsSaveWithoutStarting(t *testing.T) {
	a, _, _ := testApp(t)
	p := Profile{Name: "dev", Instance: testInstance(), Database: "app", Username: "reader", Port: 5432}
	if err := a.AddProfile(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	h := newConnectionsHarness(t, a)
	h.m.profiles, _ = a.LoadProfiles()
	h.m.tab = tabProfiles
	h.key("ctrl+s")
	h.m.fields[0].value = "copy"
	h.key("ctrl+s")
	h.until("保存しました: copy")
	if h.m.state != "list" || len(h.m.connections) != 0 {
		t.Fatal("保存だけでProxyを開始")
	}
}

func TestConnectionsAddSwitchAndStop(t *testing.T) {
	a := proxyApp(t)
	t.Setenv("CSQL_TEST_BIND", "1")
	h := newConnectionsHarness(t, a)
	h.key("enter")
	h.until("Proxy利用可能")
	first := awaitSession(t, a)
	h.key("tab")
	if h.m.state != "list" {
		t.Fatal("稼働中の接続を保って追加画面へ戻れない")
	}
	awaitProxyPort(t, first.Port)
	h.key("down")
	h.key("enter")
	h.until("Proxy利用可能")
	rows := awaitSessions(t, a, 2)
	for _, row := range rows {
		awaitProxyPort(t, row.Port)
	}
	h.key("esc")
	h.m.updateVersion = "v0.10.0"
	if !strings.Contains(h.m.View(), "稼働 2件") {
		t.Fatal("更新通知で稼働件数を喪失")
	}
	if !strings.Contains(h.m.View(), "接続一覧") {
		t.Fatal("Escが接続を停止し、接続一覧へ戻らない")
	}
	h.key("enter")
	h.key("ctrl+d")
	h.until("停止済み")
	remaining := awaitSessions(t, a, 1)
	awaitProxyPort(t, remaining[0].Port)
}

// 起動待ちでの重複追加・再試行と古い結果が、次の試行や別接続へ作用しない。
func TestConnectionsDelayedResultsAndRetry(t *testing.T) {
	a := proxyApp(t)
	t.Setenv("CSQL_TEST_BIND", "1")
	gate := filepath.Join(t.TempDir(), "bind")
	t.Setenv("CSQL_TEST_BIND_GATE", gate)
	h := newConnectionsHarness(t, a)
	h.key("enter")
	c := h.m.selectedConnection()
	h.pumpUntil(func() bool { rows, _ := a.Status(context.Background()); return len(rows) == 1 }, "bind前の起動")
	h.key("tab")
	h.key("enter")
	if len(h.m.connections) != 1 || h.m.selectedID != c.id {
		t.Fatal("起動待ちの同じインスタンスを重複起動")
	}
	h.key("ctrl+r")
	h.key("ctrl+r")
	h.pumpUntil(func() bool { return c.attempt == 2 }, "終了後に同じIDを再試行")
	h.update(connectionFinished{id: c.id, attempt: 1, err: fmt.Errorf("古い失敗")})
	if !c.running || c.err != nil || c.attempt != 2 {
		t.Fatal("古い完了が新しい試行を変更")
	}
	if err := os.WriteFile(gate, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	h.until("Proxy利用可能")
	first := awaitSession(t, a)
	h.key("tab")
	h.key("down")
	h.key("enter")
	h.until("Proxy利用可能")
	other := h.m.selectedConnection()
	awaitSessions(t, a, 2)
	// 別CLIから停止しても選択中の接続・ログは維持する。
	exe, _ := os.Executable()
	cmd := exec.Command(exe, "stop", first.ID)
	cmd.Env = append(os.Environ(), "CSQL_TEST_CLIENT_DIR="+a.Paths.ConfigDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("外部stop: %v %s", err, out)
	}
	h.pumpUntil(func() bool { return !c.running }, "外部停止の反映")
	if h.m.selectedID != other.id || other.status() != "Proxy利用可能" {
		t.Fatal("他の接続の停止が選択先へ混入")
	}
	awaitProxyPort(t, other.effectiveProfile().Port)
	// 同じProxyの別DBプロファイルを選ぶと、出力にもそのDBとユーザーを使う。
	p := other.effectiveProfile()
	p.Name, p.Database, p.Username = "analytics", "analytics-db", "analyst"
	h.m.startConnection(p)
	h.exportTool("uri")
	if !strings.Contains(h.m.exportData, "analyst@127.0.0.1") || !strings.Contains(h.m.exportData, "analytics-db") {
		t.Fatal("再利用したプロファイルが出力へ反映されない")
	}
	h.key("esc")
	h.key("esc")
	h.m.startConnection(Profile{Instance: p.Instance})
	if other.profile.Name != "analytics" {
		t.Fatal("名前のない選択で保存済み情報を消去")
	}
	conflict := p
	conflict.PrivateIP = !p.PrivateIP
	h.m.startConnection(conflict)
	if !strings.Contains(h.m.message, "既に") || len(h.m.connections) != 2 {
		t.Fatal("接続方式の相違を説明せず二重起動")
	}
}

// 2件を実起動し、片方の終了中でも保存/出力対象のDB・ユーザー・実ポートを固定する。
func TestConnectionsSaveAndExportOwnership(t *testing.T) {
	for _, endTarget := range []bool{false, true} {
		t.Run(fmt.Sprint(endTarget), func(t *testing.T) {
			a := proxyApp(t)
			t.Setenv("CSQL_TEST_BIND", "1")
			h := newConnectionsHarness(t, a)
			h.key("enter")
			h.until("Proxy利用可能")
			first := h.m.selectedConnection()
			h.key("tab")
			h.key("down")
			h.key("enter")
			h.until("Proxy利用可能")
			second := h.m.selectedConnection()
			awaitSessions(t, a, 2)
			h.m.selectedID = first.id
			h.m.selector.cursor = 0
			h.key("ctrl+s")
			h.m.fields[0].value, h.m.fields[1].value, h.m.fields[2].value = "first", "業務/db", "reader"
			actualPort := first.effectiveProfile().Port
			h.key("ctrl+s")
			// Cmd完了の配送より前に終了通知を届け、入力・対象を奪わないことを観測する。
			ending := second
			if endTarget {
				ending = first
			}
			h.m.stopConnection(ending.id, false)
			if h.m.state != "saving" {
				t.Fatal("別処理で保存中の画面を変更")
			}
			h.until("保存しました: first")
			h.pumpUntil(func() bool { return !ending.running }, "停止完了後に出力を再開")
			p := h.m.saved
			if p.Instance.ConnectionName != first.profile.ConnectionName || p.Database != "業務/db" || p.Username != "reader" || p.Port != actualPort {
				t.Fatalf("保存対象が混線: %+v", p)
			}
			for _, format := range []string{"pgadmin", "dbeaver", "a5", "uri"} {
				h.exportTool(format)
				dir, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, "connection.txt")
				h.m.fields[0].value = path
				h.key("enter")
				h.until("ファイルへ出力しました")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if format == "pgadmin" {
					var v struct {
						Servers map[string]struct {
							MaintenanceDB, Username string
							Port                    int
						}
					}
					if err := json.Unmarshal(data, &v); err != nil {
						t.Fatal(err)
					}
					row := v.Servers["1"]
					if row.MaintenanceDB != "業務/db" || row.Username != "reader" || row.Port != actualPort {
						t.Fatalf("JSON対象が混線: %s", data)
					}
				} else if format == "dbeaver" {
					rows, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
					if err != nil || rows[1][2] != fmt.Sprint(actualPort) || rows[1][3] != "業務/db" || rows[1][4] != "reader" {
						t.Fatalf("CSV対象が混線: %s %v", data, err)
					}
				} else if format == "a5" {
					if !strings.Contains(string(data), fmt.Sprintf("Port=%d;Database=業務/db;UserName=reader;", actualPort)) {
						t.Fatalf("A5対象が混線: %s", data)
					}
				} else if !strings.Contains(string(data), fmt.Sprintf("reader@127.0.0.1:%d/", actualPort)) || !strings.Contains(string(data), "%E6%A5%AD%E5%8B%99%2Fdb") {
					t.Fatalf("URI対象が混線: %s", data)
				}
			}
		})
	}
}

// Ctrl+Cはこの画面のworker終了を待ち、別Appの接続を止めない。未配送Cmdも後から起動しない。
func TestConnectionsShutdownIsolation(t *testing.T) {
	a := proxyApp(t)
	t.Setenv("CSQL_TEST_BIND", "1")
	t.Setenv("CSQL_TEST_IGNORE", "1")
	external := proxyApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	externalPort := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	go func() {
		done <- external.Connect(ctx, testInstance(), ConnectOptions{Port: externalPort, Progress: &connectionProgress{}})
	}()
	ext := awaitSession(t, external)
	awaitProxyPort(t, ext.Port)
	defer func() { cancel(); <-done }()
	h := newConnectionsHarness(t, a)
	h.key("enter")
	h.until("Proxy利用可能")
	h.key("tab")
	h.key("down")
	h.key("enter")
	h.until("Proxy利用可能")
	awaitSessions(t, a, 2)
	pending := h.m.startConnection(Profile{Instance: Instance{"alpha", "queued", "asia-northeast1", "alpha:asia-northeast1:queued", "POSTGRES_16"}})
	h.update(tea.WindowSizeMsg{Width: 48, Height: 12})
	h.key("ctrl+c")
	h.m.workers.Wait()
	// ゲート閉鎖後、未配送の接続Cmdを実行しても副作用が発生しない。
	h.update(pending())
	if rows, _ := a.Status(context.Background()); len(rows) != 0 {
		t.Fatal("終了後にProxyが残留")
	}
	if rows, _ := external.Status(context.Background()); len(rows) != 1 {
		t.Fatal("別Appの接続を停止")
	}
	awaitProxyPort(t, ext.Port)
	if !errors.Is(h.m.resultErr, ErrCancelled) {
		t.Fatal("全終了が130へ対応しない")
	}
}

// 検索と接続の完了を独立させ、検索/フォームの通常文字を停止キーと解釈しない。
func TestConnectionsNavigationDuringSearch(t *testing.T) {
	a, _, _ := testApp(t)
	h := newConnectionsHarness(t, a)
	m := h.m
	// 起動直後はPort未確定なので、未確定の設定を保存フォームへ持ち込まない。
	m.connections = []*uiConnection{{id: 1, attempt: 1, profile: Profile{Instance: testInstance()}, running: true, progress: &connectionProgress{}}}
	m.selectedID = 1
	m.state = "list"
	m.tab = tabConnections
	h.key("ctrl+s")
	if m.state != "list" || !strings.Contains(m.message, "Portの確定") {
		t.Fatal("起動前の未確定Portを保存できる")
	}
	m.connections[0].progress.snapshot.Session.Port = 55439
	m.request.Query = "db"
	m.request.Port = 55439
	m.request.Project = "alpha"
	m.request.PrivateIP = true
	m.allInstances = append([]Instance(nil), m.selector.rows...)
	h.key("tab")
	if m.request.Query != "" || m.request.Project != "alpha" || !m.request.PrivateIP || len(m.selector.rows) != 2 {
		t.Fatal("追加時の検索範囲/初期条件が不正")
	}
	h.key("s")
	if m.selector.query != "s" || !m.connections[0].running {
		t.Fatal("検索文字Sを停止として処理")
	}
	h.update(tea.KeyMsg{Type: tea.KeyCtrlU})
	// 検索中から接続タブへ移動し、遅い結果でも表示を奪わない。
	m.state = "searching"
	m.searching = true
	m.generation = 3
	h.key("tab")
	h.key("tab")
	h.update(uiResult{kind: "search", generation: 3, err: fmt.Errorf("gcloud auth login が必要")})
	if m.state != "list" || m.tab != tabConnections || !m.connections[0].running {
		t.Fatal("検索エラーで接続一覧を終了")
	}
	h.key("ctrl+s")
	if m.state != "form" {
		t.Fatal("稼働中の保存フォームへ進めない")
	}
	h.key("nseru")
	before := m.fields[0].value
	h.update(connectionFinished{id: 1, attempt: 1, err: fmt.Errorf("invalid_grant invalid_rapt")})
	if m.state != "form" || m.fields[0].value != before {
		t.Fatal("接続エラーが編集中のフォームを変更")
	}
	h.key("esc")
	if !strings.Contains(m.View(), "ADC") {
		t.Fatal("フォームを戻った際に接続ごとの復旧案内がない")
	}
	if !strings.Contains(m.View(), "エラー") {
		t.Fatal("非選択のエラーが一覧に表示されない")
	}
}

// 再試行の終了待ち中に旧実ポートを保存・出力し、次の試行の設定と混線させない。
func TestConnectionsEditingDuringRetry(t *testing.T) {
	a, _, _ := testApp(t)
	h := newConnectionsHarness(t, a)
	p := Profile{Name: "dev", Instance: testInstance(), Database: "old-db", Username: "old-user", Port: 55439}
	c := &uiConnection{id: 1, attempt: 1, profile: p, running: true, cancel: func() {}, progress: &connectionProgress{snapshot: connectionSnapshot{Session: SessionStatus{Port: p.Port}}}}
	h.m.connections = []*uiConnection{c}
	h.m.selectedID = 1
	h.m.state = "connecting"
	// 停止を開始した直後もCtrl+S/Eは旧試行の編集へ入らず、待機理由を案内する。
	h.m.stopConnection(c.id, true)
	for _, key := range []string{"ctrl+s", "e"} {
		h.key(key)
		if h.m.state != "connecting" || !strings.Contains(h.m.message, "停止") {
			t.Fatalf("停止待ちの%sで旧試行を編集: %s", key, h.m.state)
		}
	}
	// 次の試行が始まっても、実ポート確定前は出力を作らない。
	h.m.finishConnection(connectionFinished{id: 1, attempt: 1, err: context.Canceled})
	for _, key := range []string{"ctrl+s", "e"} {
		h.key(key)
		if h.m.state != "connecting" || !strings.Contains(h.m.message, "Port") {
			t.Fatalf("起動待ちの%sで未確定Portを出力", key)
		}
	}
}

// 停止済みの同じ接続は詳細へ戻り、旧履歴からの再試行でも稼働中の同じDBを二重起動しない。
func TestConnectionsReusesStoppedConnection(t *testing.T) {
	a := proxyApp(t)
	t.Setenv("CSQL_TEST_BIND", "1")
	h := newConnectionsHarness(t, a)
	h.key("enter")
	h.until("Proxy利用可能")
	first := h.m.selectedConnection()
	h.key("ctrl+d")
	h.until("停止済み")
	h.key("tab")
	h.key("enter")
	if len(h.m.connections) != 1 || h.m.selectedID != first.id {
		t.Fatal("停止済みの同じ接続を別IDで追加")
	}
	h.key("ctrl+r")
	h.until("Proxy利用可能")
	if first.attempt != 2 {
		t.Fatal("停止済みの再試行が同じIDを使わない")
	}
	h.key("ctrl+d")
	h.until("停止済み")
	// 接続方式を変更して追加する経路は維持し、旧履歴のRから重複させない。
	changed := first.profile
	changed.PrivateIP = true
	h.command(h.m.startConnection(changed))
	h.until("Proxy利用可能")
	active := h.m.selectedConnection()
	h.m.selectedID = first.id
	h.m.selector.cursor = 0
	h.key("ctrl+r")
	if h.m.selectedID != active.id || first.running {
		t.Fatal("古い停止済み接続のRから同じDBを二重起動")
	}
	rows := awaitSessions(t, a, 1)
	awaitProxyPort(t, rows[0].Port)
}

// ヘルプは実際のキー配送で読み、表示状態を閉じてから元の操作を続ける。
func readUIHelp(m *connectUI) string {
	m.Update(tea.KeyMsg{Type: tea.KeyF1})
	var all, body strings.Builder
	for i := 0; i < 30; i++ {
		all.WriteString(m.View())
		for _, line := range strings.Split(m.View(), "\n") {
			if strings.HasPrefix(line, "│") {
				body.WriteString(strings.Trim(line, "│ "))
			}
		}
		all.WriteByte('\n')
		m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	}
	m.Update(tea.KeyMsg{Type: tea.KeyF1})
	return all.String() + body.String()
}

// 保存専用コマンドの操作メニューから、意図せずProxyを開始しない。
func TestProfileAddActionMenuRemainsSaveOnly(t *testing.T) {
	for _, tab := range []connectionTab{tabInstances, tabProfiles} {
		t.Run(fmt.Sprint(tab), func(t *testing.T) {
			a, _, _ := testApp(t)
			h := newConnectionsHarness(t, a)
			h.m.addOnly = true
			h.m.tab = tab
			h.m.profiles = []Profile{{Name: "existing", Instance: testInstance(), Port: 55441}}
			key := "ctrl+k"
			if tab == tabProfiles {
				key = "ctrl+o"
			}
			h.key(key)
			h.key("connect")
			h.key("enter")
			if len(h.m.connections) != 0 || h.m.state != "actions" {
				t.Fatal("保存専用画面から接続を開始した")
			}
			h.update(tea.KeyMsg{Type: tea.KeyCtrlU})
			h.key("save")
			h.key("enter")
			if h.m.state != "form" {
				t.Fatal("保存専用画面から設定保存へ進めない")
			}
		})
	}
}

func TestConnectionsDashboardContract(t *testing.T) {
	a, _, _ := testApp(t)
	h := newConnectionsHarness(t, a)
	first := Profile{Name: "開発", Instance: testInstance(), Database: "orders", Username: "reader", Port: 55441}
	second := first
	second.Name = "検証"
	second.Database = "reports"
	second.Port = 55442
	second.Instance.Name = "reporting"
	second.ConnectionName = "alpha:asia-northeast1:reporting"
	h.m.connections = []*uiConnection{{id: 1, attempt: 1, profile: first}, {id: 2, attempt: 1, profile: second}}
	h.m.tab = tabConnections
	h.m.selectedID = 1
	h.m.updateVersion = "v0.11.0"
	for _, size := range [][2]int{{120, 26}, {100, 20}, {60, 16}} {
		h.update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		view := h.m.View()
		for _, word := range []string{"v0.11.0", "保存済み設定", "接続一覧", "Ctrl+C", "F1"} {
			if !strings.Contains(view, word) {
				t.Fatalf("%vで欠落 %s: %s", size, word, view)
			}
		}
		if size[0] >= 100 {
			for _, word := range []string{"状態", "プロジェクト", "Port", "DB"} {
				if !strings.Contains(view, word) {
					t.Fatalf("表に列がない %s", word)
				}
			}
			for _, pair := range []struct{ name, port, db string }{{"開発", "55441", "orders"}, {"検証", "55442", "reports"}} {
				found := false
				for _, line := range strings.Split(view, "\n") {
					if strings.Contains(line, pair.name) && strings.Contains(line, pair.port) && strings.Contains(line, pair.db) {
						found = true
					}
				}
				if !found {
					t.Fatalf("列と対象が対応しない: %+v\n%s", pair, view)
				}
			}
		}
	}
	h.key("検証")
	h.key("ctrl+k")
	if h.m.state != "actions" {
		t.Fatal("Ctrl+Kで操作メニューが開かない")
	}
	h.key("ポート")
	if !strings.Contains(h.m.View(), "先に設定を保存") {
		t.Fatal("無効理由がない")
	}
	h.key("enter")
	if h.m.state != "actions" {
		t.Fatal("無効操作を実行")
	}
	h.update(tea.KeyMsg{Type: tea.KeyCtrlU})
	h.key("export")
	h.key("enter")
	if h.m.state != "exportTools" {
		t.Fatal("検索後に別の操作を実行")
	}
	h.key("esc")
	if h.m.selector.query != "検証" {
		t.Fatal("操作検索が接続検索を上書き")
	}
	h.key("ctrl+k")
	h.key("none-found")
	h.key("enter")
	if h.m.state != "actions" {
		t.Fatal("検索0件で操作")
	}
	h.key("esc")
	// メニュー開始後の再試行は古い対象から操作しない。
	h.key("ctrl+k")
	h.key("save")
	h.m.connections[1].attempt++
	h.key("enter")
	if h.m.state == "form" {
		t.Fatal("古い試行を保存")
	}
	h.key("esc")
	h.update(tea.KeyMsg{Type: tea.KeyCtrlU})
	live := h.m.connections[1]
	live.running = true
	live.progress = &connectionProgress{snapshot: connectionSnapshot{State: "ready", Session: SessionStatus{Port: live.profile.Port}}}
	h.key("Proxy利用可能")
	h.key("ctrl+k")
	h.key("logs")
	live.progress.state("error")
	h.key("enter")
	if h.m.state != "actions" {
		t.Fatal("状態検索から消えた対象で別の行のログを開いた")
	}
}
func TestConnectionsHelpAndBackgroundResults(t *testing.T) {
	a, _, _ := testApp(t)
	h := newConnectionsHarness(t, a)
	p := Profile{Name: "dev", Instance: testInstance(), Database: "app", Port: 55441}
	h.m.connections = []*uiConnection{{id: 1, attempt: 1, profile: p, running: true, progress: &connectionProgress{snapshot: connectionSnapshot{State: "ready", Session: SessionStatus{Port: p.Port}}}}}
	h.m.tab = tabConnections
	h.m.selectedID = 1
	h.key("ctrl+s")
	h.key("日本語")
	fields := h.m.fields[0].value
	h.key("f1")
	if !strings.Contains(h.m.View(), "操作ヘルプ") {
		t.Fatal("フォームでF1が開かない")
	}
	h.key("unwanted")
	h.update(connectionFinished{id: 1, attempt: 1, err: fmt.Errorf("background")})
	if !strings.Contains(h.m.View(), "操作ヘルプ") {
		t.Fatal("背景終了がヘルプを閉じた")
	}
	h.key("esc")
	if h.m.state != "form" || h.m.fields[0].value != fields {
		t.Fatal("ヘルプが入力を変更")
	}
	h.key("esc")
	h.key("ctrl+k")
	h.key("logs")
	h.key("f1")
	h.key("esc")
	h.key("enter")
	if !strings.Contains(h.m.View(), "ログ") {
		t.Fatal("ヘルプから戻るとメニュー検索を喪失")
	}
	// 保存完了が背後へ配送されてもヘルプを維持し、閉じた後にsavingへ戻さない。
	h.m.state = "saving"
	h.m.operation = 9
	h.m.editOwner = 1
	h.m.editAttempt = 1
	h.key("f1")
	h.update(uiResult{kind: "save", operation: 9, owner: 1, attempt: 1, profile: p})
	if !strings.Contains(h.m.View(), "操作ヘルプ") {
		t.Fatal("保存完了でヘルプが閉じた")
	}
	h.key("f1")
	if h.m.state == "saving" || !strings.Contains(h.m.View(), "保存しました") {
		t.Fatal("保存完了が消えた")
	}
	h.m.state = "searching"
	h.m.tab = tabInstances
	h.m.searching = true
	h.m.generation = 4
	h.key("f1")
	h.update(uiResult{kind: "search", generation: 4, result: DiscoveryResult{Instances: []Instance{testInstance()}}})
	if !strings.Contains(h.m.View(), "操作ヘルプ") {
		t.Fatal("検索完了がヘルプを閉じた")
	}
	h.key("f1")
	if h.m.state != "list" || len(h.m.selector.rows) != 1 {
		t.Fatal("ヘルプから検索結果へ戻れない")
	}
	h.key("f1")
	h.key("ctrl+c")
	if !h.m.exit {
		t.Fatal("ヘルプ中に終了できない")
	}
}
func TestConnectionsRecoveryCopy(t *testing.T) {
	for _, mode := range []string{"success", "fail", "other-row", "other-attempt", "form", "cancel", "repeat"} {
		t.Run(mode, func(t *testing.T) {
			a, _, _ := testApp(t)
			h := newConnectionsHarness(t, a)
			p := Profile{Name: "dev", Instance: testInstance(), Port: 55441}
			d := explainFailure(FailureContext{Source: "proxy", Operation: "接続", GcloudCommand: "'/日本語 path/gcloud'"}, "invalid_rapt")
			h.m.connections = []*uiConnection{{id: 1, attempt: 1, profile: p, err: fmt.Errorf("auth"), diagnostic: &d}, {id: 2, attempt: 1, profile: p}}
			h.m.tab = tabConnections
			h.m.selectedID = 1
			started := make(chan string, 2)
			gate := make(chan struct{})
			a.CopyText = func(ctx context.Context, text string) error {
				started <- text
				select {
				case <-gate:
					if mode == "fail" {
						return fmt.Errorf("copy failed")
					}
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			h.key("ctrl+k")
			h.key("コピー")
			h.key("enter")
			select {
			case text := <-started:
				if text != d.Commands[0].Text {
					t.Fatalf("折返し/別コマンドをコピー: %q", text)
				}
			case <-time.After(time.Second):
				t.Fatal("コピーを開始しない")
			}
			if mode == "repeat" {
				h.key("ctrl+k")
				h.key("コピー")
				h.key("enter")
				select {
				case <-started:
					t.Fatal("コピーを二重開始")
				default:
				}
				h.key("esc")
			}
			if mode == "other-row" {
				h.key("down")
			}
			if mode == "other-attempt" {
				h.m.connections[0].attempt++
			}
			if mode == "form" {
				h.key("ctrl+s")
			}
			if mode == "cancel" {
				h.key("ctrl+c")
			}
			close(gate)
			if mode == "cancel" {
				h.m.workers.Wait()
				return
			}
			h.pumpUntil(func() bool { return !h.m.copyBusy }, "copy result")
			if mode == "success" && !strings.Contains(h.m.View(), "別ターミナル") {
				t.Fatal("認証後の復旧案内がない")
			}
			if mode == "fail" {
				var seen strings.Builder
				for i := 0; i < 20; i++ {
					seen.WriteString(h.m.View())
					h.key("pgdown")
				}
				if !strings.Contains(seen.String(), "手動") || !strings.Contains(seen.String(), "application-default") {
					t.Fatal("コピー失敗でコマンドが読めない")
				}
			}
			if (mode == "other-row" || mode == "other-attempt" || mode == "form") && strings.Contains(h.m.message, "コピーしました") {
				t.Fatal("遅いコピー結果が別対象を上書き")
			}
			if mode == "form" && h.m.state != "form" {
				t.Fatal("コピー結果がフォームを閉じた")
			}
		})
	}
}

// 検索認証の復旧をProxyのADC認証と取り違えず、認証自体は起動しない。
func TestConnectionsSearchRecoveryCopy(t *testing.T) {
	a, _, _ := testApp(t)
	h := newConnectionsHarness(t, a)
	d := explainFailure(FailureContext{Source: "gcloud", Operation: "検索", GcloudCommand: "'/検索 path/gcloud'"}, "invalid_grant")
	h.m.state = "error"
	h.m.diagnostic = &d
	copied := make(chan string, 1)
	a.CopyText = func(ctx context.Context, text string) error { copied <- text; return nil }
	a.Runner = runnerFunc(func(context.Context, Executable, []string) ([]byte, error) {
		t.Error("認証コマンドを自動実行")
		return nil, fmt.Errorf("禁止")
	})
	h.key("ctrl+k")
	h.key("コピー")
	h.key("enter")
	h.pumpUntil(func() bool { return !h.m.copyBusy }, "検索の復旧コピー")
	select {
	case text := <-copied:
		if text != "'/検索 path/gcloud' auth login" {
			t.Fatalf("検索の認証が異なる: %q", text)
		}
	default:
		t.Fatal("コピーされない")
	}
}

// 起動直後の各一覧で、接続先を登録・選択せずに更新確認を開ける。
func TestConnectionsTopLevelUpdate(t *testing.T) {
	for _, tab := range []connectionTab{tabInstances, tabProfiles, tabConnections} {
		t.Run(fmt.Sprint(tab), func(t *testing.T) {
			a, _, _ := testApp(t)
			selfUpdateUIFixture(t, a)
			h := newConnectionsHarness(t, a)
			h.m.tab = tab
			h.m.selector.rows = nil
			for _, width := range []int{60, 100} {
				h.update(tea.WindowSizeMsg{Width: width, Height: 16})
				if !strings.Contains(h.m.View(), "F2 csqlを更新") {
					t.Fatalf("%d列の一覧に更新入口がない: %s", width, h.m.View())
				}
			}
			h.key("f2")
			if h.m.state != "updateChecking" {
				t.Fatal("接続先なしでF2から更新を開けない", h.m.state)
			}
			h.pumpUntil(func() bool { return h.m.state == "updateConfirm" }, "更新確認")
			h.key("esc")
			if h.m.state != "list" || h.m.tab != tab || len(h.m.connections) != 0 {
				t.Fatal("取消で元の一覧に戻らない、または接続を開始した")
			}
		})
	}
	// 入力中やヘルプ表示中は、F2で編集・表示を中断しない。
	for _, mode := range []string{"add-only", "form", "help"} {
		t.Run(mode, func(t *testing.T) {
			a, _, _ := testApp(t)
			h := newConnectionsHarness(t, a)
			switch mode {
			case "add-only":
				h.m.addOnly = true
			case "form":
				h.m.openForm(Profile{Instance: testInstance()})
			case "help":
				h.key("f1")
			}
			before := h.m.View()
			h.key("f2")
			if h.m.updateJob != nil || h.m.View() != before || (mode != "help" && strings.Contains(before, "F2 csqlを更新")) {
				t.Fatal("対象外の画面で更新を案内・開始した")
			}
		})
	}
	t.Run("search-error", func(t *testing.T) {
		a, _, _ := testApp(t)
		selfUpdateUIFixture(t, a)
		h := newConnectionsHarness(t, a)
		h.m.fail(fmt.Errorf("検索できません"), "local")
		h.key("ctrl+l")
		if !strings.Contains(h.m.View(), "F2 csqlを更新") {
			t.Fatal("検索失敗時に更新の入口がない")
		}
		h.key("f2")
		h.pumpUntil(func() bool { return h.m.state == "updateConfirm" }, "更新確認")
		h.key("esc")
		if h.m.state != "error" || !strings.Contains(h.m.View(), "検索できません") {
			t.Fatal("更新取消で元のエラー情報が失われた")
		}
	})
}

// 対象行がなくても更新でき、確認/取得を取り消しても接続を維持する。
func TestConnectionsSelfUpdate(t *testing.T) {
	for _, mode := range []string{"decline", "cancel", "offline", "lock", "ready", "add-only"} {
		t.Run(mode, func(t *testing.T) {
			a, _, _ := testApp(t)
			gate, started, fail := selfUpdateUIFixture(t, a)
			h := newConnectionsHarness(t, a)
			h.m.tab = tabConnections
			h.m.selector.query = "no-match"
			liveCtx, liveCancel := context.WithCancel(h.ctx)
			h.m.connections = []*uiConnection{{id: 1, attempt: 1, profile: Profile{Name: "live", Instance: testInstance()}, running: true, cancel: liveCancel}}
			t.Cleanup(func() {
				if h.m.pendingUpdate != nil {
					h.m.pendingUpdate.Close()
				}
			})
			if mode == "add-only" {
				h.m.addOnly = true
				h.key("ctrl+k")
				h.key("update")
				h.key("enter")
				if h.m.state != "actions" {
					t.Fatal("保存専用画面で更新開始")
				}
				return
			}
			if mode == "ready" {
				// 従来のメニュー入口も維持する。
				h.key("ctrl+k")
				h.key("update")
				h.key("enter")
			} else {
				h.key("f2")
			}
			if h.m.state != "updateChecking" {
				t.Fatal("検索0件の共通更新が開かない", h.m.state)
			}
			h.pumpUntil(func() bool { return h.m.state == "updateConfirm" }, "更新確認")
			if !strings.Contains(h.m.View(), "0.10.0") {
				t.Fatal("版の確認がない")
			}
			if mode == "decline" {
				h.key("enter")
				if h.m.state != "list" || h.m.selector.query != "no-match" || liveCtx.Err() != nil {
					t.Fatal("既定取消で位置が戻らない")
				}
				return
			}
			if mode == "offline" {
				fail.Store(true)
			}
			if mode == "lock" {
				os.Mkdir(filepath.Join(a.Paths.ConfigDir, ".path-lock"), 0700)
			}
			h.key("tab")
			h.key("enter")
			h.pumpUntil(func() bool {
				select {
				case <-started:
					return true
				default:
					return false
				}
			}, "取得開始")
			if h.m.exit {
				t.Fatal("取得中に終了")
			}
			h.key("ctrl+k")
			if h.m.state != "updatePreparing" {
				t.Fatal("準備中に別操作へ移動")
			}
			if mode == "cancel" {
				h.key("esc")
			} else {
				close(gate)
			}
			if mode == "ready" {
				h.pumpUntil(func() bool { return h.m.exit }, "更新終了")
				if h.m.pendingUpdate == nil || h.m.resultErr != nil {
					t.Fatal("通常取消と更新終了が混ざった")
				}
				h.key("ctrl+c")
				if !errors.Is(h.m.resultErr, ErrCancelled) {
					t.Fatal("終了中Ctrl+Cを無視")
				}
				return
			}
			h.pumpUntil(func() bool { return h.m.state == "list" }, "更新失敗/取消から復帰")
			if h.m.exit || h.m.selector.query != "no-match" || liveCtx.Err() != nil {
				t.Fatal("失敗で一覧状態を破壊")
			}
		})
	}
}

// 更新準備が終わるまで接続を維持し、更新終了では自画面のProxyだけを止める。
func TestConnectionsSelfUpdateShutdown(t *testing.T) {
	a := proxyApp(t)
	gate, started, _ := selfUpdateUIFixture(t, a)
	t.Setenv("CSQL_TEST_BIND", "1")
	external := proxyApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	done := make(chan error, 1)
	go func() {
		done <- external.Connect(ctx, testInstance(), ConnectOptions{Port: port, Progress: &connectionProgress{}})
	}()
	ext := awaitSession(t, external)
	defer func() { cancel(); <-done }()
	h := newConnectionsHarness(t, a)
	h.key("enter")
	h.until("Proxy利用可能")
	h.key("tab")
	h.key("down")
	h.key("enter")
	h.until("Proxy利用可能")
	awaitSessions(t, a, 2)
	t.Cleanup(func() {
		if h.m.pendingUpdate != nil {
			h.m.pendingUpdate.Close()
		}
	})
	h.key("ctrl+k")
	h.key("update")
	h.key("enter")
	if h.m.state != "updateChecking" {
		t.Fatal("更新を開始できない")
	}
	h.pumpUntil(func() bool { return h.m.state == "updateConfirm" }, "更新確認")
	h.key("tab")
	h.key("enter")
	h.pumpUntil(func() bool {
		select {
		case <-started:
			return true
		default:
			return false
		}
	}, "取得開始")
	if rows, _ := a.Status(ctx); len(rows) != 2 {
		t.Fatal("検証前に接続停止")
	}
	close(gate)
	h.pumpUntil(func() bool { return h.m.exit }, "更新停止")
	h.m.workers.Wait()
	if rows, _ := a.Status(ctx); len(rows) != 0 {
		t.Fatal("更新停止でProxy残留")
	}
	if rows, _ := external.Status(ctx); len(rows) != 1 {
		t.Fatal("外部Proxyを停止")
	}
	awaitProxyPort(t, ext.Port)
}

// readyをUIへ配送しない境界から終了・取消・再試行しても準備結果を漏らさない。
func TestConnectionsSelfUpdateDeliveryContract(t *testing.T) {
	for _, mode := range []string{"undelivered-exit", "cancel-stale-retry"} {
		t.Run(mode, func(t *testing.T) {
			a, _, _ := testApp(t)
			gate, _, _ := selfUpdateUIFixture(t, a)
			close(gate)
			h := newConnectionsHarness(t, a)
			t.Cleanup(func() {
				h.m.closeWork()
				h.m.workers.Wait()
				if h.m.updateJob != nil {
					h.m.updateJob.closePrepared()
				}
				if h.m.pendingUpdate != nil {
					h.m.pendingUpdate.Close()
				}
			})
			open := func() {
				h.key("ctrl+k")
				h.key("update")
				h.key("enter")
				h.pumpUntil(func() bool { return h.m.state == "updateConfirm" }, "更新確認")
				h.key("tab")
				h.key("enter")
			}
			open()
			var ready uiSelfUpdateResult
			timeout := time.NewTimer(6 * time.Second)
			defer timeout.Stop()
		waitReady:
			for {
				select {
				case msg := <-h.events:
					if v, ok := msg.(uiSelfUpdateResult); ok && v.prepared != nil {
						ready = v
						break waitReady
					}
					h.update(msg)
				case <-timeout.C:
					t.Fatal("未配送ready待ちが完了しない")
				}
			}
			h.m.workers.Wait()
			lock := filepath.Join(a.Paths.ConfigDir, ".path-lock")
			if _, err := os.Stat(lock); err != nil {
				t.Fatal("配送前の準備が排他を所有していない", err)
			}
			stage := ready.prepared.StageDir
			if mode == "undelivered-exit" {
				h.key("ctrl+c")
				h.m.workers.Wait()
				// runConnectModelの終了待機後と同じ所有権の解放を通す。
				h.m.updateJob.closePrepared()
				h.update(ready)
				if !h.m.exit || h.m.pendingUpdate != nil {
					t.Fatal("遅い結果が更新終了へ変化")
				}
			} else {
				h.key("esc")
				h.update(ready)
				if h.m.state != "list" || h.m.exit {
					t.Fatal("取消後に一覧へ戻らない")
				}
				h.key("ctrl+k")
				h.key("update")
				h.key("enter")
				h.update(ready) // 終了済みoperationが次の更新を上書きしない。
				if h.m.state != "updateChecking" || h.m.exit {
					t.Fatal("古いreadyが新しい操作を更新")
				}
				h.pumpUntil(func() bool { return h.m.state == "updateConfirm" }, "再試行の確認")
				h.key("tab")
				h.key("enter")
				h.pumpUntil(func() bool { return h.m.exit }, "再試行ready")
				if h.m.pendingUpdate == nil || h.m.pendingUpdate.StageDir == stage {
					t.Fatal("再試行が古い準備を再利用")
				}
				h.m.pendingUpdate.Close()
			}
			if _, err := os.Stat(stage); !os.IsNotExist(err) {
				t.Fatal("古いstagingが残留")
			}
			if _, err := os.Stat(lock); !os.IsNotExist(err) {
				t.Fatal("更新ロックが残留")
			}
			target := filepath.Join(a.Paths.ConfigDir, "bin", "csql")
			b, _ := os.ReadFile(target)
			if string(b) != "previous csql binary" {
				t.Fatal("取消/未配送の結果が配置を変更")
			}
		})
	}
}

// 新PCの空の一覧でも移行でき、検索中の1行だけを誤って書き出さない。
func TestConnectionsTopLevelMigration(t *testing.T) {
	for _, tab := range []connectionTab{tabInstances, tabProfiles, tabConnections} {
		t.Run(fmt.Sprint(tab), func(t *testing.T) {
			a, _, _ := testApp(t)
			h := newConnectionsHarness(t, a)
			h.m.tab = tab
			h.m.selector.rows = nil
			h.m.selector.query = "一致なし"
			for _, width := range []int{60, 100} {
				h.update(tea.WindowSizeMsg{Width: width, Height: 16})
				view := h.m.View()
				for _, label := range []string{"F1 ヘルプ", "F2 csqlを更新", "F3 データ移行", "Ctrl+C 全接続停止"} {
					if !strings.Contains(view, label) {
						t.Fatalf("%d列の一覧で%sが見えない: %s", width, label, view)
					}
				}
			}
			h.key("f3")
			if !strings.Contains(h.m.View(), "全接続先を書き出す") || !strings.Contains(h.m.View(), "移行ファイルを取り込む") {
				t.Fatal("接続先なしで移行メニューを開けない", h.m.View())
			}
			h.key("down")
			h.key("enter")
			if h.m.state != "migrationPath" || h.m.migrationKind != "import" {
				t.Fatal("新PCで取込パスへ進めない", h.m.state)
			}
			h.key("esc")
			if h.m.state != "list" || h.m.tab != tab || h.m.selector.query != "一致なし" || len(h.m.connections) != 0 {
				t.Fatal("取消後に元の未接続一覧へ戻らない")
			}
			if _, err := os.Stat(a.Paths.ConfigDir); !os.IsNotExist(err) {
				t.Fatal("取込前に設定を書き込んだ", err)
			}
			// 保存全件の出力を、検索結果0件のまま開始する。
			rows := migrationRows()
			if err := a.writeProfiles(rows); err != nil {
				t.Fatal(err)
			}
			h.m.profiles = rows
			h.key("f3")
			h.key("enter")
			h.pumpUntil(func() bool { return h.m.state == "migrationPath" }, "全件の出力パス")
			doc, err := decodeMigration(h.m.migrationData)
			if err != nil || len(doc.Profiles) != len(rows) || !strings.Contains(h.m.View(), "全件") {
				t.Fatal("検索や選択で全件出力の対象が減った", doc, err, h.m.View())
			}
			h.key("esc")
		})
	}
	t.Run("search-error", func(t *testing.T) {
		a, _, _ := testApp(t)
		h := newConnectionsHarness(t, a)
		h.m.fail(fmt.Errorf("検索できません"), "local")
		h.key("ctrl+l")
		h.key("f3")
		h.key("down")
		h.key("enter")
		if h.m.state != "migrationPath" {
			t.Fatal("依存未導入・認証失敗時に取込できない")
		}
		h.key("esc")
		if h.m.state != "error" || !strings.Contains(h.m.View(), "検索できません") {
			t.Fatal("取消で検索エラーが失われた")
		}
	})
	for _, mode := range []string{"add-only", "form", "help", "small"} {
		t.Run(mode, func(t *testing.T) {
			a, _, _ := testApp(t)
			h := newConnectionsHarness(t, a)
			switch mode {
			case "add-only":
				h.m.addOnly = true
			case "form":
				h.m.openForm(Profile{Instance: testInstance()})
			case "help":
				h.key("f1")
			case "small":
				h.update(tea.WindowSizeMsg{Width: 59, Height: 15})
			}
			before := h.m.View()
			h.key("f3")
			if h.m.View() != before || (mode != "help" && strings.Contains(before, "F3 データ移行")) {
				t.Fatal("対象外の画面で移行を開始・案内した")
			}
		})
	}
}

// 行の選択に依存しない移行操作と、確認・取消・一覧への反映を実キーで通す。
func TestConnectionsMigration(t *testing.T) {
	a, _, _ := testApp(t)
	h := newConnectionsHarness(t, a)
	h.m.tab = tabProfiles
	h.m.selector.query = "一致なし"
	source := migrationFile(t, migrationData(t, migrationRows()))
	open := func(kind string) {
		t.Helper()
		h.key("ctrl+k")
		h.key("migration " + kind)
		h.key("enter")
		if kind == "export" {
			h.pumpUntil(func() bool { return h.m.state == "migrationPath" }, "出力パス")
		} else if h.m.state != "migrationPath" {
			t.Fatalf("選択0件の共通入口: %s", h.m.state)
		}
	}
	input := func(path string) {
		t.Helper()
		h.m.fields[0].value = path
		h.m.fields[0].pos = len([]rune(path))
		h.key("enter")
		h.pumpUntil(func() bool { return h.m.state == "migrationConfirm" || h.m.state == "list" }, "取込確認")
	}
	open("import")
	input("'" + source + "'")
	h.update(tea.WindowSizeMsg{Width: 60, Height: 16})
	h.key("pgdown")
	h.key("f1")
	if !strings.Contains(h.m.View(), "操作ヘルプ") {
		t.Fatal("移行help")
	}
	h.key("esc")
	h.key("enter")
	if h.m.state != "list" {
		t.Fatal("既定取消で一覧へ戻らない")
	}
	if _, err := os.Stat(a.Paths.ConfigDir); !os.IsNotExist(err) {
		t.Fatal("取消で書込み")
	}
	open("import")
	input(source)
	h.key("tab")
	h.key("enter")
	h.pumpUntil(func() bool { return h.m.state == "list" }, "取込完了")
	if len(h.m.profiles) != 2 || h.m.selector.query != "一致なし" {
		t.Fatal("保存一覧/検索が復元されない")
	}
	h.key("f1")
	if !strings.Contains(strings.Join(h.m.help.lines, "\n"), "取り込みました") {
		t.Fatal("完了結果をhelpで確認できない")
	}
	h.key("esc")
	open("export")
	output := filepath.Join(filepath.Dir(source), "exported.json")
	h.m.fields[0].value = output
	h.m.fields[0].pos = len([]rune(output))
	h.key("enter")
	h.pumpUntil(func() bool { return h.m.state == "list" }, "出力完了")
	if _, err := os.Stat(output); err != nil {
		t.Fatal(err)
	}
	open("import")
	h.m.fields[0].value = source + ".absent"
	h.key("enter")
	h.pumpUntil(func() bool { return h.m.state == "migrationPath" }, "入力失敗")
	input(source)
	h.pumpUntil(func() bool { return h.m.state == "list" }, "同一再取込")
	for _, prefix := range []string{"~/", "~\\"} {
		home := filepath.Dir(source)
		a.Env = func(k string) string {
			if k == "HOME" {
				return home
			}
			return ""
		}
		open("import")
		input(prefix + filepath.Base(source))
		if h.m.state != "list" {
			t.Fatal("ホーム展開の再取込")
		}
	}
	h.m.addOnly = true
	h.key("ctrl+k")
	for _, action := range h.m.availableActions() {
		if strings.HasPrefix(action.ID, "migration-") {
			t.Fatal("保存専用画面で移行が有効")
		}
	}
}

// 書込み確定の前後で取消し、未配送結果と再試行の所有を確かめる。
func TestConnectionsMigrationLifecycle(t *testing.T) {
	for _, mode := range []string{"cancel-before-save", "cancel-after-save", "undelivered-exit", "outer-cancel"} {
		t.Run(mode, func(t *testing.T) {
			a, _, _ := testApp(t)
			if err := a.writeProfiles(migrationRows()); err != nil {
				t.Fatal(err)
			}
			h := newConnectionsHarness(t, a)
			incoming := migrationRows()[0]
			incoming.Name = "imported"
			source := migrationFile(t, migrationData(t, []Profile{incoming}))
			h.key("ctrl+k")
			h.key("migration import")
			h.key("enter")
			h.m.fields[0].value = source
			h.key("enter")
			h.pumpUntil(func() bool { return h.m.state == "migrationConfirm" }, "確認")
			entered, gate := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-gate:
				default:
					close(gate)
				}
			}()
			if mode == "cancel-before-save" {
				a.MigrationOps = &migrationFileOps{Backup: func(dir string, b []byte) (string, error) {
					path, err := backupMigration(dir, b)
					close(entered)
					<-gate
					return path, err
				}}
			} else {
				a.MigrationOps = &migrationFileOps{Save: func(rows []Profile) error { close(entered); <-gate; return a.writeProfiles(rows) }}
			}
			h.key("tab")
			h.key("enter")
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("保存待機")
			}
			job := h.m.migrationJob
			if mode == "cancel-before-save" || mode == "cancel-after-save" {
				h.key("esc")
				if h.m.state != "migrationCancelling" {
					t.Fatal("取消待機なし")
				}
				h.key("ctrl+k")
				if h.m.migrationJob != job {
					t.Fatal("取消中の新操作")
				}
			}
			close(gate)
			if mode == "undelivered-exit" || mode == "outer-cancel" {
				// 結果が配送される前に実workerの確定を待つ。
				h.m.workers.Wait()
				job.mu.Lock()
				done := job.finished
				job.mu.Unlock()
				if done == nil || !done.result.Committed {
					t.Fatal("配送前に結果を保持していない")
				}
				if mode == "outer-cancel" {
					h.m.cancelAll()
				} else {
					h.key("ctrl+c")
				}
				h.m.workers.Wait()
				h.update(*done)
				job.mu.Lock()
				displayed := job.displayed
				job.mu.Unlock()
				if displayed {
					t.Fatal("終了画面が結果を表示済みとして破棄")
				}
				return
			}
			h.pumpUntil(func() bool { return h.m.state == "list" }, "取消/保存の完了")
			rows, err := a.LoadProfiles()
			if err != nil {
				t.Fatal(err)
			}
			if mode == "cancel-before-save" {
				if len(rows) != 2 || !strings.Contains(h.m.migrationReport, "バックアップ") {
					t.Fatal("未確定で保存/backup消失")
				}
			} else {
				if len(rows) != 3 || !strings.Contains(h.m.migrationReport, "取り込みました") {
					t.Fatal("確定済み結果を取消と誤表示")
				}
			}
			// 古い結果を次のoperationへ配送しても画面を変更しない。
			h.key("ctrl+k")
			h.key("migration import")
			h.key("enter")
			job.mu.Lock()
			old := *job.finished
			job.mu.Unlock()
			h.update(old)
			if h.m.state != "migrationPath" {
				t.Fatal("古い結果が新操作へ適用")
			}
			h.m.fields[0].value = source
			h.key("enter")
			h.pumpUntil(func() bool { return h.m.state == "migrationConfirm" || h.m.state == "list" }, "再試行")
			if mode == "cancel-before-save" {
				h.key("tab")
				a.MigrationOps = nil
				h.key("enter")
				h.pumpUntil(func() bool { return h.m.state == "list" }, "再取込")
			}
		})
	}
}

// 通常の移行は、自画面2本と別所有の実fixtureセッションを停止しない。
func TestConnectionsMigrationLive(t *testing.T) {
	t.Setenv("CSQL_TEST_BIND", "1")
	a := proxyApp(t)
	h := newConnectionsHarness(t, a)
	h.key("enter")
	h.until("Proxy利用可能")
	h.command(h.m.startConnection(Profile{Instance: h.m.selector.rows[1]}))
	h.pumpUntil(func() bool {
		return len(h.m.connections) == 2 && h.m.connections[0].status() == "Proxy利用可能" && h.m.connections[1].status() == "Proxy利用可能"
	}, "2接続")
	external := proxyApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- external.Connect(ctx, testInstance(), ConnectOptions{}) }()
	awaitSession(t, external)
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("外部fixture終了待機")
		}
	}()
	source := migrationFile(t, migrationData(t, migrationRows()))
	h.key("f3")
	h.key("down")
	h.key("enter")
	h.m.fields[0].value = source
	h.key("enter")
	h.pumpUntil(func() bool { return h.m.state == "migrationConfirm" }, "稼働中の確認")
	h.key("tab")
	h.key("enter")
	h.pumpUntil(func() bool { return h.m.state == "list" }, "稼働中の取込")
	if rows, _ := a.Status(context.Background()); len(rows) != 2 {
		t.Fatal("自画面の接続を停止")
	}
	if rows, _ := external.Status(context.Background()); len(rows) != 1 {
		t.Fatal("外部接続を停止")
	}
	for _, c := range h.m.connections {
		if c.profile.Username != "" || !c.running {
			t.Fatal("取込が稼働中の接続を書き換えた")
		}
	}
	h.key("f3")
	h.key("enter")
	h.pumpUntil(func() bool { return h.m.state == "migrationPath" }, "稼働中の出力")
	h.m.fields[0].value = filepath.Join(filepath.Dir(source), "running-export.json")
	h.key("enter")
	h.pumpUntil(func() bool { return h.m.state == "list" }, "出力完了")
	if rows, _ := a.Status(context.Background()); len(rows) != 2 {
		t.Fatal("出力で接続停止")
	}
	h.key("ctrl+c")
	h.m.workers.Wait()
	if rows, _ := a.Status(context.Background()); len(rows) != 0 {
		t.Fatal("Ctrl+Cで自画面の接続が残った")
	}
	if rows, _ := external.Status(context.Background()); len(rows) != 1 {
		t.Fatal("Ctrl+Cが外部に波及")
	}
}

// 起点の検索・選択を保ち、既定取消と一覧からの登録を確認する。
func TestConnectionsDBToolRegistration(t *testing.T) {
	a, p, exe, w := dbToolFixture(t)
	h := newConnectionsHarness(t, a)
	m := h.m
	m.tab = tabProfiles
	m.profiles = []Profile{p}
	m.selector.query = "dev"
	m.selector.inputPos = 3
	h.key("ctrl+k")
	h.key("register")
	h.key("enter")
	if m.state != "registrationTools" {
		t.Fatalf("入口: %s %s", m.state, m.message)
	}
	h.key("down")
	h.key("enter")
	m.fields[0].value = exe
	m.fields[1].value = w
	m.focus = 1
	h.key("enter")
	h.pumpUntil(func() bool { return m.state == "registrationConfirm" }, "登録前確認")
	h.key("enter")
	if m.state != "list" || m.tab != tabProfiles || m.selector.query != "dev" {
		t.Fatalf("取消後の一覧 %+v", m)
	}
	files, _ := filepath.Glob(filepath.Join(w, "General/.dbeaver/data-sources-csql-*.json"))
	if len(files) != 0 {
		t.Fatal("既定取消で書込み")
	}
	h.key("ctrl+k")
	h.key("register")
	h.key("enter")
	h.key("down")
	h.key("enter")
	m.fields[0].value = exe
	m.fields[1].value = w
	m.focus = 1
	h.key("enter")
	h.pumpUntil(func() bool { return m.state == "registrationConfirm" }, "登録前確認")
	h.key("tab")
	h.key("enter")
	h.pumpUntil(func() bool { return m.state == "registrationResult" }, "登録結果")
	if !strings.Contains(m.registrationReport, "追加JSONを保存") {
		t.Fatal(m.registrationReport)
	}
	for _, size := range [][2]int{{60, 16}, {100, 30}} {
		m.selector.width, m.selector.height = size[0], size[1]
		view := m.View()
		if !strings.Contains(view, "登録結果") {
			t.Fatal("結果画面を表示できない")
		}
	}
	h.key("enter")
	if m.state != "list" || m.selector.query != "dev" {
		t.Fatal("登録後の起点を失った")
	}
}

// 画面終了でCmdが配送されなくても、確定した外部副作用はjobが保持する。
func TestConnectionsDBToolRegistrationDelivery(t *testing.T) {
	a, p, exe, w := dbToolFixture(t)
	h := newConnectionsHarness(t, a)
	m := h.m
	m.tab = tabProfiles
	m.profiles = []Profile{p}
	m.openActions()
	m.editProfile = p
	m.startDBToolRegistration()
	plan, err := a.planDBToolRegistration(h.ctx, p, dbToolOptions{Tool: "dbeaver", Executable: exe, DBeaverWorkspace: w})
	if err != nil {
		t.Fatal(err)
	}
	cmd := m.dbToolRegistrationWork("apply", func(ctx context.Context) uiDBToolRegistrationResult {
		r, e := a.applyDBToolRegistration(ctx, plan)
		return uiDBToolRegistrationResult{result: r, err: e}
	})
	// Bubble Teaへ配送する前に終了させる。
	msg := cmd()
	m.beginShutdown()
	job := m.registrationJobs[len(m.registrationJobs)-1]
	job.mu.Lock()
	finished := job.finished
	displayed := job.displayed
	job.mu.Unlock()
	if finished == nil || !finished.result.Committed || displayed {
		t.Fatalf("未配送結果を失った %+v", finished)
	}
	m.Update(msg)
	if m.state != "stopping" {
		t.Fatal("遅延結果が終了画面を書き換えた")
	}
}

func TestConnectionsDBToolRegistrationSmallTerminal(t *testing.T) {
	a, p, exe, w := dbToolFixture(t)
	h := newConnectionsHarness(t, a)
	m := h.m
	m.tab = tabProfiles
	m.profiles = []Profile{p}
	m.openActions()
	m.editProfile = p
	m.startDBToolRegistration()
	plan, err := a.planDBToolRegistration(h.ctx, p, dbToolOptions{Tool: "dbeaver", Executable: exe, DBeaverWorkspace: w})
	if err != nil {
		t.Fatal(err)
	}
	m.registrationPlan = plan
	m.state = "registrationConfirm"
	h.update(tea.WindowSizeMsg{Width: 59, Height: 15})
	h.key("tab")
	h.key("enter")
	if m.registrationYes || m.registrationJob != nil || m.state != "registrationConfirm" {
		t.Fatal("確認内容が隠れたまま登録を開始")
	}
	h.key("esc")
	if m.state != "registrationForm" {
		t.Fatal("小さい画面で取消できない")
	}
}
