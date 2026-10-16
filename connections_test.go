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
	if body := h.m.View(); !strings.Contains(body, "db · 停止済み") || !strings.Contains(body, "log-099") || !strings.Contains(body, "Ctrl+C") {
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
	h.m.updateVersion = "v0.5.0"
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
