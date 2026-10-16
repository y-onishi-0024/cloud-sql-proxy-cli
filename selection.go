package main

import (
	"context"
	"errors"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type selector struct {
	rows                    []Instance
	query                   string
	cursor                  int
	picked                  *Instance
	cancelled               bool
	width, height, inputPos int
	color                   bool
}

func (s selector) filtered() []Instance {
	var rows []Instance
	for _, r := range s.rows {
		if strings.Contains(strings.ToLower(r.Name+" "+r.Project), strings.ToLower(s.query)) {
			rows = append(rows, r)
		}
	}
	return rows
}
func (s selector) Init() tea.Cmd { return nil }
func (s selector) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		s.width = size.Width
		s.height = size.Height
		return s, nil
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return s, nil
	}
	rows := s.filtered()
	switch key.String() {
	case "ctrl+c", "esc":
		s.cancelled = true
		return s, tea.Quit
	case "up", "ctrl+p":
		if s.cursor > 0 {
			s.cursor--
		}
	case "down", "ctrl+n":
		if s.cursor+1 < len(rows) {
			s.cursor++
		}
	case "enter":
		if s.cursor < len(rows) {
			r := rows[s.cursor]
			s.picked = &r
			return s, tea.Quit
		}
	default:
		if editText(&s.query, &s.inputPos, key) {
			s.cursor = 0
		}
	}
	return s, nil
}

// 端末のセル幅と高さに収め、候補が減っても同じ画面内を再描画する。
func (s selector) View() string {
	width, height := s.width, s.height
	if width < 1 {
		width = 80
	}
	if height < 1 {
		height = 24
	}
	if width < 60 || height < 16 {
		return fitScreen("60列×16行以上に広げてください。\nCtrl+C / Esc: 取消", width, height)
	}
	var rows []terminalChoice
	for _, r := range s.filtered() {
		rows = append(rows, instanceChoice(r))
	}
	v := terminalView{width: width, height: height, color: s.color}
	return v.choices(choiceView{title: "インスタンス一覧", tabs: "名前・プロジェクトで検索", query: s.query, pos: s.inputPos, cursor: s.cursor, rows: rows, footer: []string{"↑↓ 選択  Enter 接続  Ctrl+U 検索を消去", "Esc 取消  Ctrl+C 終了"}})
}
func instanceChoice(r Instance) terminalChoice {
	return terminalChoice{name: cleanText(r.Name), hint: cleanText(r.Project), summary: cleanText(r.Region) + " · " + cleanText(r.DatabaseVersion), details: []string{"インスタンス", cleanText(r.Name), "", "プロジェクト", cleanText(r.Project), "", "リージョン", cleanText(r.Region), "", "DB種別", cleanText(r.DatabaseVersion)}}
}

func paint(s, color string, enabled bool) string {
	if !enabled {
		return s
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(s)
}
func fitScreen(s string, width, height int) string {
	if width < 1 {
		width = 80
	}
	if height < 1 {
		height = 24
	}
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	return strings.Join(lines, "\n")
}
func inputView(s string, pos, width int) string {
	r := []rune(s)
	if pos < 0 {
		pos = 0
	}
	if pos > len(r) {
		pos = len(r)
	}
	view := string(r[:pos]) + "▏" + string(r[pos:])
	for ansi.StringWidth(view) > width && pos > 0 {
		r = r[1:]
		pos--
		view = "…" + string(r[:pos]) + "▏" + string(r[pos:])
	}
	return view
}
func editText(value *string, pos *int, key tea.KeyMsg) bool {
	r := []rune(*value)
	if *pos > len(r) {
		*pos = len(r)
	}
	if *pos < 0 {
		*pos = 0
	}
	changed := false
	switch key.Type {
	case tea.KeyLeft:
		if *pos > 0 {
			*pos--
		}
	case tea.KeyRight:
		if *pos < len(r) {
			*pos++
		}
	case tea.KeyHome, tea.KeyCtrlA:
		*pos = 0
	case tea.KeyEnd, tea.KeyCtrlE:
		*pos = len(r)
	case tea.KeyBackspace, tea.KeyCtrlH:
		if *pos > 0 {
			r = append(r[:*pos-1], r[*pos:]...)
			*pos--
			changed = true
		}
	case tea.KeyDelete:
		if *pos < len(r) {
			r = append(r[:*pos], r[*pos+1:]...)
			changed = true
		}
	case tea.KeyCtrlU:
		r = nil
		*pos = 0
		changed = true
	case tea.KeyRunes:
		text := []rune(cleanText(string(key.Runes)))
		if len(r)+len(text) <= 4096 {
			tail := append([]rune(nil), r[*pos:]...)
			r = append(append(r[:*pos], text...), tail...)
			*pos += len(text)
			changed = len(text) > 0
		}
	case tea.KeySpace:
		if len(r) < 4096 {
			tail := append([]rune(nil), r[*pos:]...)
			r = append(append(r[:*pos], ' '), tail...)
			*pos++
			changed = true
		}
	}
	*value = string(r)
	return changed
}
func (a *App) SelectInstance(ctx context.Context, rows []Instance) (Instance, error) {
	if !a.TTY {
		a.printInstances(rows)
		return Instance{}, fmt.Errorf("候補の選択には対話端末が必要です。インスタンス名と --project を指定してください")
	}
	m, err := tea.NewProgram(selector{rows: rows}, tea.WithInput(a.In), tea.WithOutput(a.terminalOutput()), tea.WithContext(ctx), tea.WithoutSignalHandler(), tea.WithAltScreen()).Run()
	if ctx.Err() != nil {
		return Instance{}, ctx.Err()
	}
	if err != nil {
		return Instance{}, err
	}
	s := m.(selector)
	if s.cancelled || s.picked == nil {
		return Instance{}, ErrCancelled
	}
	return *s.picked, nil
}

// 画面は検索・編集と接続集合を所有し、API・保存・Proxy待機をCmdへ渡す。
type connectRequest struct {
	Query, Project, ProfileName string
	Refresh, PrivateIP          bool
	Port                        int
}
type uiResult struct {
	generation                int
	operation, owner, attempt uint64
	kind                      string
	result                    DiscoveryResult
	notices                   []string
	profile                   Profile
	profiles                  []Profile
	data                      string
	err                       error
}
type uiTick struct{ epoch uint64 }
type uiShutdownComplete struct{}
type connectionTab int

const (
	tabInstances connectionTab = iota
	tabProfiles
	tabConnections
)

type formField struct {
	label, value string
	pos          int
}

// 入力画面から戻る際、検索と選択行を同じ位置へ復元する。
type listLocation struct {
	tab              connectionTab
	query            string
	inputPos, cursor int
	connectionID     uint64
}
type connectUI struct {
	a                                    *App
	ctx                                  context.Context
	request                              connectRequest
	selector                             selector
	profiles                             []Profile
	allInstances                         []Instance
	tab                                  connectionTab
	connections                          []*uiConnection
	nextID, selectedID                   uint64
	state                                string
	frame                                int
	updateVersion                        string
	recoveryPage                         int
	generation                           int
	searching                            bool
	cancel                               context.CancelFunc
	cancelAll                            context.CancelFunc
	workers                              sync.WaitGroup
	workMu                               sync.Mutex
	workClosed                           bool
	tickEpoch                            uint64
	tickPending                          bool
	operation, editOwner, editAttempt    uint64
	editProfile                          Profile
	fields                               []formField
	focus                                int
	formBack                             string
	addOnly                              bool
	seed, saved                          Profile
	message                              string
	diagnostic                           *Diagnostic
	showLogs                             bool
	exportFormat, exportData             string
	exportCursor                         int
	location                             *listLocation
	actionCursor, portExpected, portPage int
	portNames                            []string
	connectionLogs                       bool
	exit                                 bool
	resultErr                            error
	notices                              []string
}

// 未配送Cmdを待機数に含めず、ゲート閉鎖後の遅いCmdを実行しない。
func (m *connectUI) work(ctx context.Context, cancelled tea.Msg, run tea.Cmd) tea.Cmd {
	return func() tea.Msg {
		m.workMu.Lock()
		if m.workClosed || ctx.Err() != nil {
			m.workMu.Unlock()
			return cancelled
		}
		m.workers.Add(1)
		m.workMu.Unlock()
		defer m.workers.Done()
		return run()
	}
}
func (m *connectUI) closeWork() {
	m.workMu.Lock()
	m.workClosed = true
	m.workMu.Unlock()
	if m.cancelAll != nil {
		m.cancelAll()
	}
	if m.cancel != nil {
		m.cancel()
	}
	for _, c := range m.connections {
		if c.cancel != nil {
			c.cancel()
		}
	}
}
func (m *connectUI) beginShutdown() tea.Cmd {
	if m.exit {
		return nil
	}
	m.exit = true
	m.resultErr = ErrCancelled
	m.state = "stopping"
	m.closeWork()
	// 待機自身はworkersへ登録しない。終了通知の配送を待たず、実workerの終了を待つ。
	return func() tea.Msg { m.workers.Wait(); return uiShutdownComplete{} }
}
func (m *connectUI) ensureTick() tea.Cmd {
	if m.exit || m.tickPending || (!m.searching && m.state != "saving" && !m.hasOwnedWork()) {
		return nil
	}
	m.tickPending = true
	m.tickEpoch++
	epoch := m.tickEpoch
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return uiTick{epoch: epoch} })
}
func (m *connectUI) Init() tea.Cmd {
	var start tea.Cmd
	if m.request.ProfileName != "" {
		p, err := findProfile(m.profiles, m.request.ProfileName)
		if err != nil {
			m.fail(err, "local")
		} else {
			start = m.startConnection(p)
		}
	} else if m.tab == tabInstances {
		start = m.search()
	}
	if m.addOnly || !m.a.TTY || m.a.Env("CSQL_NO_UPDATE_CHECK") == "1" {
		return start
	}
	a, ctx := m.a, m.ctx
	return tea.Batch(start, m.work(ctx, updateChecked{}, func() tea.Msg { return updateChecked{latest: a.checkForUpdate(ctx)} }))
}
func (m *connectUI) search() tea.Cmd {
	if m.searching || m.exit {
		return nil
	}
	m.searching = true
	m.state = "searching"
	m.generation++
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	generation, request, a := m.generation, m.request, m.a
	return tea.Batch(m.ensureTick(), m.work(ctx, uiResult{generation: generation, kind: "search", err: context.Canceled}, func() tea.Msg {
		var notices []string
		r, err := a.discover(ctx, request.Project, request.Refresh, func(s string) { notices = append(notices, s) })
		if err == nil && request.Query != "" && !request.Refresh && len(MatchInstances(r.Instances, request.Query)) == 0 {
			r, err = a.discover(ctx, request.Project, true, func(s string) { notices = append(notices, s) })
		}
		return uiResult{generation: generation, kind: "search", result: r, notices: notices, err: err}
	}))
}
func (m *connectUI) fail(err error, source string) {
	fc := m.a.failureContext(m.ctx, source, map[string]string{"gcloud": "インスタンス検索", "local": "操作"}[source], Instance{}, false)
	var e diagnosticError
	if errors.As(err, &e) {
		fc = e.Context
	}
	d := explainFailure(fc, err.Error())
	m.diagnostic = &d
	m.recoveryPage = 0
	m.state = "error"
	m.resultErr = err
}
func (m *connectUI) matchedProfiles() []Profile {
	q := strings.ToLower(m.selector.query)
	var rows []Profile
	for _, p := range m.profiles {
		if strings.Contains(strings.ToLower(p.Name+" "+p.Database+" "+p.Username+" "+p.Instance.Name+" "+p.Project), q) {
			rows = append(rows, p)
		}
	}
	return rows
}
func (m *connectUI) selected() (Profile, bool) {
	i := m.selector.cursor
	if i < 0 {
		return Profile{}, false
	}
	if m.tab == tabProfiles {
		rows := m.matchedProfiles()
		if i < len(rows) {
			return rows[i], true
		}
	} else if m.tab == tabInstances {
		rows := m.selector.filtered()
		if i < len(rows) {
			return Profile{Instance: rows[i], PrivateIP: m.request.PrivateIP}, true
		}
	}
	return Profile{}, false
}
func (m *connectUI) selectedConnection() *uiConnection { return m.connection(m.selectedID) }
func (m *connectUI) showConnections() {
	m.showLogs = false
	m.state = "list"
	m.tab = tabConnections
	m.selector.query = ""
	m.selector.inputPos = 0
	m.selector.cursor = 0
	m.message = ""
	for i, c := range m.connections {
		if c.id == m.selectedID {
			m.selector.cursor = i
		}
	}
}

// 一覧での操作は表示中の行を解決し、過去の詳細選択を参照しない。
func (m *connectUI) actionConnection() *uiConnection {
	if m.tab != tabConnections {
		return nil
	}
	rows := m.matchedConnections()
	i := m.selector.cursor
	if i < 0 || i >= len(rows) {
		return nil
	}
	return rows[i]
}
func (m *connectUI) rememberList() {
	if m.state != "list" {
		return
	}
	loc := &listLocation{tab: m.tab, query: m.selector.query, inputPos: m.selector.inputPos, cursor: m.selector.cursor}
	if c := m.actionConnection(); c != nil {
		loc.connectionID = c.id
		m.selectedID = c.id
	}
	m.location = loc
}
func (m *connectUI) returnToList() {
	if m.location == nil {
		m.state = "list"
		return
	}
	loc := *m.location
	m.location = nil
	m.state = "list"
	m.tab = loc.tab
	m.selector.query, m.selector.inputPos, m.selector.cursor = loc.query, loc.inputPos, loc.cursor
	count := len(m.selector.filtered())
	if m.tab == tabConnections {
		rows := m.matchedConnections()
		count = len(rows)
		for i, c := range rows {
			if c.id == loc.connectionID {
				m.selector.cursor = i
				m.selectedID = c.id
			}
		}
	} else if m.tab == tabProfiles {
		count = len(m.matchedProfiles())
	}
	m.selector.cursor = max(0, min(m.selector.cursor, count-1))
}
func (m *connectUI) workspaceActions() []string {
	if m.tab == tabProfiles {
		return []string{"ポート変更"}
	}
	return []string{"エクスポート  Ctrl+E", "ログ表示/非表示  Ctrl+L", "保存・別名保存  Ctrl+S", "ポート変更", "停止  Ctrl+D", "再試行  Ctrl+R"}
}
func (m *connectUI) openActions() {
	if c := m.actionConnection(); c != nil {
		m.selectedID = c.id
		m.editProfile = c.effectiveProfile()
	} else if p, ok := m.selected(); ok && m.tab == tabProfiles {
		m.editProfile = p
	} else {
		m.message = "操作する行を選択してください"
		return
	}
	m.rememberList()
	m.actionCursor = 0
	m.state = "actions"
	m.message = ""
}
func (m *connectUI) workspaceAction(index int) tea.Cmd {
	c := m.actionConnection()
	if m.state == "actions" && m.tab == tabConnections {
		c = m.selectedConnection()
	}
	if m.tab == tabProfiles {
		if index == 3 || m.state == "actions" {
			m.openPortForm(m.editProfile)
		}
		return nil
	}
	if c == nil {
		m.message = "操作する接続を選択してください"
		return nil
	}
	m.selectedID = c.id
	switch index {
	case 0:
		m.openExportMenu()
	case 1:
		if m.state == "actions" {
			m.returnToList()
		}
		m.connectionLogs = !m.connectionLogs
		c.recoveryPage = 0
	case 2:
		m.openForm(c.effectiveProfile())
	case 3:
		m.openPortForm(c.effectiveProfile())
	case 4, 5:
		if m.state == "actions" {
			m.returnToList()
		}
		return m.stopConnection(c.id, index == 5)
	}
	return nil
}
func (m *connectUI) openPortForm(p Profile) {
	if m.state == "list" {
		m.rememberList()
	}
	c := m.selectedConnection()
	if m.tab != tabConnections {
		c = nil
	}
	if c != nil && (c.running || c.stopping) {
		if m.location != nil {
			m.returnToList()
		}
		m.message = "先に対象Proxyを停止し、停止済みを確認してからPortを変更してください"
		return
	}
	rows, err := m.a.LoadProfiles()
	if err != nil {
		m.message = safeLog(err.Error())
		return
	}
	var found *Profile
	for _, row := range rows {
		if row.ConnectionName == p.ConnectionName && (found == nil || row.Name == p.Name) {
			value := row
			found = &value
		}
	}
	if found == nil {
		if m.location != nil {
			m.returnToList()
		}
		m.message = "先に接続設定を保存してください"
		return
	}
	m.editProfile = *found
	m.portExpected = found.Port
	m.portNames = nil
	for _, row := range rows {
		if row.ConnectionName == found.ConnectionName {
			m.portNames = append(m.portNames, row.Name)
		}
	}
	m.editOwner, m.editAttempt = 0, 0
	if c != nil {
		m.editOwner, m.editAttempt = c.id, c.attempt
	}
	value := strconv.Itoa(found.Port)
	m.fields = []formField{{label: "変更先Port", value: value, pos: len(value)}}
	m.portPage = 0
	m.state = "portForm"
	m.message = ""
}
func (m *connectUI) savePortForm() tea.Cmd {
	port, err := strconv.Atoi(m.fields[0].value)
	if err != nil || port < 1 || port > 65535 {
		m.message = "Portは1〜65535を指定してください"
		m.portPage = 0
		return nil
	}
	m.operation++
	result := uiResult{kind: "portUpdate", operation: m.operation, owner: m.editOwner, attempt: m.editAttempt, profile: m.editProfile}
	result.profile.Port = port
	a, ctx, name, old := m.a, m.ctx, m.editProfile.Name, m.portExpected
	m.state = "saving"
	m.message = "保存中…"
	cancelled := result
	cancelled.err = context.Canceled
	return m.work(ctx, cancelled, func() tea.Msg { result.profiles, result.err = a.UpdateProfilePort(ctx, name, old, port); return result })
}

// 選択行の情報を、一覧を残したまま右または下の領域へ表示する。
func (m *connectUI) workspacePanel(width, height int) (string, []string, int) {
	c := m.actionConnection()
	limit := height - 6
	if width < 100 || height < 20 {
		limit = min(5, max(2, height-12))
	}
	wrapWidth := max(1, width-8)
	if width >= 100 && height >= 20 {
		wrapWidth = max(34, width/3) - 4
	}
	title := "接続情報"
	var lines []string
	if m.message != "" {
		lines = append(lines, strings.Split(ansi.Wrap(safeLog(m.message), wrapWidth, ""), "\n")...)
	}
	if c != nil {
		p := c.effectiveProfile()
		lines = append(lines, c.status(), fmt.Sprintf("Host 127.0.0.1:%d", p.Port), "DB "+p.Database+" / "+p.Username)
		lines = append(lines, p.Region+" / "+p.DatabaseVersion)
		if c.progress != nil {
			if id := c.progress.Snapshot().Session.ID; id != "" {
				lines = append(lines, "セッション "+id)
			}
		}
		if c.diagnostic != nil {
			lines = append(lines, strings.Split(ansi.Wrap(safeLog(c.diagnostic.Cause+"\n次の操作: "+c.diagnostic.Next), wrapWidth, ""), "\n")...)
		}
		if m.connectionLogs {
			title = "ログ · PgUp/PgDn"
			var logs []string
			if c.progress != nil {
				logs = strings.Split(ansi.Wrap(strings.Join(c.progress.Snapshot().Logs, "\n"), wrapWidth, ""), "\n")
			}
			if c.diagnostic != nil && c.diagnostic.Detail != "" {
				lines = append(lines, "詳細:")
				lines = append(lines, strings.Split(ansi.Wrap(safeLog(c.diagnostic.Detail), wrapWidth, ""), "\n")...)
			}
			// 通常のログは末尾から過去へ辿る。対処や操作結果は先頭から読めるようにする。
			if m.message == "" && c.diagnostic == nil {
				last := max(0, (len(logs)-1)/max(1, limit-1))
				end := max(0, len(logs)-min(last, c.recoveryPage)*max(1, limit-1))
				start := max(0, end-max(1, limit-1))
				return title, append([]string{c.status()}, logs[start:end]...), last
			}
			lines = append(lines, "ログ:")
			lines = append(lines, logs...)
		}
		last := max(0, (len(lines)-1)/max(1, limit))
		start := min(last, c.recoveryPage) * max(1, limit)
		return title, lines[start:min(len(lines), start+limit)], last
	}
	if len(lines) > limit {
		lines = lines[:limit]
	}
	return title, lines, 0
}
func (m *connectUI) addConnection() tea.Cmd {
	m.state = "list"
	m.tab = tabInstances
	m.request.Query = ""
	m.selector.query = ""
	m.selector.inputPos = 0
	m.selector.cursor = 0
	m.message = ""
	if len(m.allInstances) > 0 {
		m.selector.rows = m.allInstances
	}
	if len(m.selector.rows) == 0 {
		return m.search()
	}
	return nil
}
func (m *connectUI) openForm(p Profile) {
	m.rememberList()
	m.formBack = m.state
	if m.formBack == "error" {
		m.formBack = "list"
	}
	m.editOwner, m.editAttempt = 0, 0
	if m.state == "connecting" || ((m.state == "list" || m.state == "actions") && m.tab == tabConnections) {
		if c := m.selectedConnection(); c != nil {
			p = c.effectiveProfile()
			if c.stopping {
				m.message = "停止処理の完了を待ってから保存してください"
				return
			}
			if c.running && (c.progress == nil || c.progress.Snapshot().Session.Port == 0) {
				m.message = "Portの確定を待ってから保存してください"
				return
			}
			m.editOwner, m.editAttempt = c.id, c.attempt
		}
	}
	if m.addOnly {
		p.Name = m.seed.Name
		p.Database = m.seed.Database
		p.Username = m.seed.Username
		p.Port = m.seed.Port
		p.PrivateIP = m.seed.PrivateIP
	}
	m.editProfile = p
	port := ""
	if p.Port > 0 {
		port = strconv.Itoa(p.Port)
	}
	m.fields = []formField{{label: "設定名", value: p.Name, pos: len([]rune(p.Name))}, {label: "DB名", value: p.Database, pos: len([]rune(p.Database))}, {label: "DBユーザー", value: p.Username, pos: len([]rune(p.Username))}, {label: "Port（空なら自動）", value: port, pos: len(port)}}
	m.focus = 0
	m.state = "form"
	m.message = ""
}
func (m *connectUI) saveForm() tea.Cmd {
	if m.state != "form" {
		return nil
	}
	p := m.editProfile
	p.Name, p.Database, p.Username = m.fields[0].value, m.fields[1].value, m.fields[2].value
	port := m.fields[3].value
	previewOnly := port == ""
	if port != "" {
		v, err := strconv.Atoi(port)
		if err != nil || v < 1 || v > 65535 {
			m.message = "Portは1〜65535を指定してください"
			return nil
		}
		p.Port = v
	} else {
		p.Port = 0
	}
	if c := m.connection(m.editOwner); c != nil && c.running {
		if live := c.effectiveProfile().Port; live == 0 || live != p.Port {
			m.message = "接続中のPortは変更できません。Proxyを停止してから保存してください"
			return nil
		}
	}
	m.state = "saving"
	m.message = "保存中…"
	m.operation++
	result := uiResult{operation: m.operation, owner: m.editOwner, attempt: m.editAttempt, kind: "save"}
	a, ctx := m.a, m.ctx
	return m.work(ctx, uiResult{operation: result.operation, owner: result.owner, attempt: result.attempt, kind: "save", err: context.Canceled}, func() tea.Msg {
		if err := ctx.Err(); err != nil {
			result.err = err
			return result
		}
		result.profile, result.err = a.PrepareProfile(p)
		if result.err == nil && previewOnly {
			result.kind = "prepared"
		} else if result.err == nil {
			result.err = a.AddProfile(ctx, result.profile)
		}
		return result
	})
}

// Eで対象接続を固定し、ツール選択中の背景終了でも別接続へ切り替えない。
func (m *connectUI) openExportMenu() {
	m.rememberList()
	c := m.selectedConnection()
	if c == nil {
		return
	}
	if c.stopping {
		m.message = "停止処理の完了を待ってから出力してください"
		return
	}
	if c.running && (c.progress == nil || c.progress.Snapshot().Session.Port == 0) {
		m.message = "Portの確定を待ってから出力してください"
		return
	}
	p := c.effectiveProfile()
	if p.Name == "" {
		m.openForm(p)
		m.message = "先に接続設定を保存してください。保存後にCtrl+Eで出力できます"
		return
	}
	m.editProfile = p
	m.editOwner, m.editAttempt = c.id, c.attempt
	m.exportCursor = 0
	m.state = "exportTools"
	m.message = ""
	m.exportData = ""
}
func (m *connectUI) beginExport(format string) {
	p := m.editProfile
	fromMenu := m.state == "exportTools"
	if !fromMenu {
		if c := m.selectedConnection(); c != nil && m.state == "connecting" {
			p = c.effectiveProfile()
			if c.stopping {
				m.message = "停止処理の完了を待ってから出力してください"
				return
			}
			if c.running && (c.progress == nil || c.progress.Snapshot().Session.Port == 0) {
				m.message = "Portの確定を待ってから出力してください"
				return
			}
		}
		if p.Name == "" {
			m.openForm(p)
			m.message = "先に接続設定を保存してください"
			return
		}
	}
	data, err := profileExport(p, format)
	if err != nil {
		m.message = err.Error()
		return
	}
	dir, err := os.Getwd()
	if err != nil {
		m.message = "出力先フォルダを確認できません: " + safeLog(err.Error())
		return
	}
	suffix := ""
	for _, tool := range exportTools {
		if tool.format == format {
			suffix = tool.suffix
			break
		}
	}
	base := filepath.Join(dir, "csql-"+p.Name+"-"+format)
	path := ""
	// 候補提示と書込時の両方で競合を検出し、既存ファイル・dir・linkを保持する。
	for i := 0; i < 1000; i++ {
		candidate := base + suffix
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d%s", base, i+1, suffix)
		}
		_, err := os.Lstat(candidate)
		if os.IsNotExist(err) {
			path = candidate
			break
		}
		if err != nil {
			m.message = "出力先を確認できません: " + safeLog(err.Error())
			return
		}
	}
	if path == "" {
		m.message = "出力先の同名ファイルが多いため、自動設定できません。別のフォルダからcsqlを起動してください"
		return
	}
	m.formBack = m.state
	if m.formBack == "form" {
		m.formBack = "list"
	}
	if !fromMenu {
		m.editProfile = p
		m.editOwner, m.editAttempt = 0, 0
		if c := m.selectedConnection(); c != nil && m.state == "connecting" {
			m.editOwner, m.editAttempt = c.id, c.attempt
		}
	}
	m.exportFormat, m.exportData = format, string(data)
	m.state = "export"
	m.fields = []formField{{label: "出力パス（Enterで保存・編集可 / Escで戻る）", value: path, pos: len([]rune(path))}}
	m.focus = 0
	m.message = ""
}
func (m *connectUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case uiShutdownComplete:
		return m, tea.Quit
	case updateChecked:
		if !m.exit {
			m.updateVersion = v.latest
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.selector.width, m.selector.height = v.Width, v.Height
		return m, nil
	case connectionFinished:
		return m, m.finishConnection(v)
	case uiTick:
		if v.epoch != m.tickEpoch || !m.tickPending {
			return m, nil
		}
		m.tickPending = false
		if m.exit {
			return m, nil
		}
		m.frame = (m.frame + 1) % 10
		for _, c := range m.connections {
			if c.progress != nil && c.running {
				if d := c.progress.Snapshot().Diagnostic; d != nil {
					if c.diagnostic == nil || c.diagnostic.Detail != d.Detail {
						c.recoveryPage = 0
					}
					c.diagnostic = d
				}
			}
		}
		return m, m.ensureTick()
	case uiResult:
		if m.exit {
			return m, nil
		}
		if v.kind == "search" {
			if v.generation != m.generation {
				return m, nil
			}
			m.searching = false
			if m.cancel != nil {
				m.cancel()
				m.cancel = nil
			}
			visible := m.state == "searching" && m.tab == tabInstances
			m.notices = v.notices
			for _, f := range v.result.Failures {
				m.notices = append(m.notices, "検索失敗 ["+f.Project+"]: "+f.Message)
			}
			if v.err != nil {
				if visible {
					m.fail(v.err, "gcloud")
				} else {
					m.notices = append(m.notices, safeLog(v.err.Error()))
				}
				return m, nil
			}
			m.allInstances = v.result.Instances
			m.selector.rows = v.result.Instances
			if m.request.Query != "" {
				m.selector.rows = MatchInstances(v.result.Instances, m.request.Query)
				if m.addOnly {
					m.selector.rows = nil
					for _, r := range v.result.Instances {
						if r.Name == m.request.Query {
							m.selector.rows = append(m.selector.rows, r)
						}
					}
				}
			}
			if !visible {
				return m, nil
			}
			m.state = "list"
			m.selector.query = ""
			m.selector.inputPos = 0
			m.selector.cursor = 0
			if len(m.selector.rows) == 0 {
				m.fail(fmt.Errorf("一致するインスタンスがありません。検索語・--project・--refreshを確認してください"), "local")
				return m, nil
			}
			if m.addOnly && len(m.selector.rows) == 1 {
				m.openForm(Profile{Instance: m.selector.rows[0]})
				return m, nil
			}
			if !m.addOnly && len(m.connections) == 0 && m.request.Query != "" && len(m.selector.rows) == 1 {
				return m, m.startConnection(Profile{Instance: m.selector.rows[0], PrivateIP: m.request.PrivateIP})
			}
			return m, nil
		}
		// 編集操作の結果は検索世代とは分け、開始時の対象と操作番号にのみ配送する。
		if v.operation != m.operation || v.owner != m.editOwner || v.attempt != m.editAttempt || m.state != "saving" {
			return m, nil
		}
		switch v.kind {
		case "prepared":
			m.state = "form"
			m.fields[3].value = strconv.Itoa(v.profile.Port)
			m.fields[3].pos = len(m.fields[3].value)
			m.message = fmt.Sprintf("保存予定のPortは %d です。Enter / Ctrl+Sで保存します", v.profile.Port)
		case "save":
			if v.err != nil {
				m.state = "form"
				m.message = safeLog(v.err.Error())
				return m, nil
			}
			m.saved = v.profile
			m.editProfile = v.profile
			m.profiles, _ = m.a.LoadProfiles()
			m.message = fmt.Sprintf("保存しました: %s / 127.0.0.1:%d（DBログイン未検証）", v.profile.Name, v.profile.Port)
			if m.addOnly {
				return m, tea.Quit
			}
			if m.location != nil {
				if c := m.connection(v.owner); c != nil && c.attempt == v.attempt {
					c.profile = v.profile
					c.recoveryPage = 0
				}
				m.returnToList()
				return m, nil
			}
			if c := m.connection(v.owner); c != nil && c.attempt == v.attempt {
				c.profile = v.profile
				c.recoveryPage = 0
				m.state = "connecting"
			} else if m.formBack == "list" {
				return m, m.startConnection(v.profile)
			} else {
				m.state = m.formBack
			}
		case "portUpdate":
			if v.err != nil {
				m.state = "portForm"
				m.portPage = 0
				m.message = safeLog(v.err.Error())
				return m, nil
			}
			m.profiles = v.profiles
			for _, c := range m.connections {
				if c.profile.ConnectionName == v.profile.ConnectionName {
					c.profile.Port = v.profile.Port
					c.recoveryPage = 0
				}
			}
			m.returnToList()
			m.message = fmt.Sprintf("Portを更新しました: %d → %d\nDBツール側のPortを変更するか再エクスポートしてください", m.portExpected, v.profile.Port)
		case "export":
			m.state = "export"
			if v.err != nil {
				m.message = safeLog(v.err.Error())
			} else {
				m.message = "ファイルへ出力しました: " + v.data + "\n" + exportGuide(m.exportFormat)
				if c := m.connection(v.owner); c != nil {
					c.recoveryPage = 0
				}
				if m.location != nil {
					m.returnToList()
				}
			}
		}
		return m, nil
	case tea.KeyMsg:
		key := v.String()
		if key == "ctrl+c" {
			return m, m.beginShutdown()
		}
		if m.exit || m.state == "saving" || m.state == "stopping" {
			return m, nil
		}
		if m.selector.width > 0 && (m.selector.width < 60 || m.selector.height < 16) && key != "esc" {
			return m, nil
		}
		if d, _, page := m.recovery(); d != nil && (m.state == "error" || m.state == "connecting") && (key == "pgup" || key == "pgdown") {
			width, height := m.dimensions()
			pageHeight := max(1, height-6)
			pages := max(1, (len(m.recoveryLines(width))+pageHeight-1)/pageHeight)
			if key == "pgup" {
				*page = max(0, min(*page, pages-1)-1)
			} else {
				*page = min(*page+1, pages-1)
			}
			return m, nil
		}
		if m.state == "actions" {
			if m.tab == tabConnections {
				indices := map[string]int{"ctrl+e": 0, "ctrl+l": 1, "ctrl+s": 2, "ctrl+d": 4, "ctrl+r": 5}
				if index, ok := indices[key]; ok {
					return m, m.workspaceAction(index)
				}
			}
			switch key {
			case "esc":
				m.returnToList()
			case "up", "shift+tab":
				m.actionCursor = max(0, m.actionCursor-1)
			case "down", "tab":
				m.actionCursor = min(len(m.workspaceActions())-1, m.actionCursor+1)
			case "enter":
				return m, m.workspaceAction(m.actionCursor)
			}
			return m, nil
		}
		if m.state == "portForm" {
			switch key {
			case "esc":
				m.returnToList()
			case "enter", "ctrl+s":
				return m, m.savePortForm()
			case "pgup":
				m.portPage = max(0, m.portPage-1)
			case "pgdown":
				width, _ := m.dimensions()
				count := len(m.portNames) + len(strings.Split(ansi.Wrap(safeLog(m.message), max(1, width-4), ""), "\n"))
				m.portPage = min(max(0, (count-1)/3), m.portPage+1)
			default:
				editText(&m.fields[0].value, &m.fields[0].pos, v)
			}
			return m, nil
		}
		if m.state == "form" {
			switch key {
			case "esc":
				if m.addOnly {
					m.resultErr = ErrCancelled
					return m, tea.Quit
				}
				m.state = m.formBack
				if m.location != nil {
					m.returnToList()
				}
			case "tab", "down":
				m.focus = (m.focus + 1) % len(m.fields)
			case "shift+tab", "up":
				m.focus = (m.focus + len(m.fields) - 1) % len(m.fields)
			case "enter":
				if m.focus < len(m.fields)-1 {
					m.focus++
				} else {
					return m, m.saveForm()
				}
			case "ctrl+s":
				return m, m.saveForm()
			default:
				editText(&m.fields[m.focus].value, &m.fields[m.focus].pos, v)
			}
			return m, nil
		}
		if m.state == "exportTools" {
			switch key {
			case "esc":
				m.state = "connecting"
				if m.location != nil {
					m.returnToList()
				}
				m.message = ""
			case "up", "shift+tab":
				m.exportCursor = max(0, m.exportCursor-1)
			case "down", "tab":
				m.exportCursor = min(len(exportTools)-1, m.exportCursor+1)
			case "enter":
				m.beginExport(exportTools[m.exportCursor].format)
			}
			return m, nil
		}
		if m.state == "export" {
			if key == "esc" {
				m.state = m.formBack
				return m, nil
			}
			if key == "enter" {
				path := m.fields[0].value
				if path == "" {
					m.state = m.formBack
					return m, nil
				}
				data, ctx := []byte(m.exportData), m.ctx
				m.operation++
				m.state = "saving"
				result := uiResult{kind: "export", operation: m.operation, owner: m.editOwner, attempt: m.editAttempt, data: path}
				cancelled := result
				cancelled.err = context.Canceled
				return m, m.work(ctx, cancelled, func() tea.Msg {
					if err := ctx.Err(); err != nil {
						result.err = err
					} else {
						result.err = writeProfileExport(path, data)
					}
					return result
				})
			}
			editText(&m.fields[0].value, &m.fields[0].pos, v)
			return m, nil
		}
		if key == "esc" {
			if m.state == "connecting" {
				m.showConnections()
				return m, nil
			}
			if m.state == "searching" {
				if m.cancel != nil {
					m.cancel()
					m.cancel = nil
				}
				m.searching = false
				m.generation++
				if len(m.connections) > 0 {
					m.showConnections()
					return m, nil
				}
			} else if m.hasOwnedWork() {
				m.showConnections()
				return m, nil
			}
			m.resultErr = ErrCancelled
			if m.cancelAll != nil {
				return m, m.beginShutdown()
			}
			return m, tea.Quit
		}
		if (m.state == "list" || m.state == "searching") && key == "tab" {
			if m.tab == tabConnections && !m.addOnly {
				return m, m.addConnection()
			}
			tabs := 3
			if m.addOnly {
				tabs = 2
			}
			m.tab = connectionTab((int(m.tab) + 1) % tabs)
			m.selector.query = ""
			m.selector.inputPos = 0
			m.selector.cursor = 0
			m.showLogs = false
			m.state = "list"
			if m.tab == tabInstances && len(m.selector.rows) == 0 {
				if m.searching {
					m.state = "searching"
					return m, nil
				}
				return m, m.search()
			}
			return m, nil
		}
		if m.state == "searching" {
			return m, nil
		}
		if m.state == "list" && (m.tab == tabConnections || m.tab == tabProfiles) {
			if key == "ctrl+o" {
				m.openActions()
				return m, nil
			}
			if m.tab == tabConnections {
				indices := map[string]int{"ctrl+e": 0, "ctrl+l": 1, "ctrl+s": 2, "ctrl+d": 4, "ctrl+r": 5}
				if index, ok := indices[key]; ok {
					return m, m.workspaceAction(index)
				}
				if key == "pgup" || key == "pgdown" {
					if c := m.actionConnection(); c != nil {
						width, height := m.dimensions()
						_, _, last := m.workspacePanel(width, height)
						next := key == "pgdown"
						if m.connectionLogs && m.message == "" && c.diagnostic == nil {
							next = !next
						}
						if next {
							c.recoveryPage = min(last, c.recoveryPage+1)
						} else {
							c.recoveryPage = max(0, min(last, c.recoveryPage)-1)
						}
					}
					return m, nil
				}
			}
		}
		if m.state == "list" {
			switch key {
			case "ctrl+s":
				if p, ok := m.selected(); ok {
					m.openForm(p)
				}
			case "enter":
				if m.tab == tabConnections {
					m.openActions()
				} else if p, ok := m.selected(); ok {
					if m.addOnly {
						m.openForm(p)
					} else {
						return m, m.startConnection(p)
					}
				}
			case "ctrl+d":
				if m.tab == tabConnections {
					rows := m.matchedConnections()
					if m.selector.cursor < len(rows) {
						return m, m.stopConnection(rows[m.selector.cursor].id, false)
					}
				}
			case "up", "ctrl+p":
				m.selector.cursor = max(0, m.selector.cursor-1)
			case "down", "ctrl+n":
				count := len(m.selector.filtered())
				if m.tab == tabProfiles {
					count = len(m.matchedProfiles())
				} else if m.tab == tabConnections {
					count = len(m.matchedConnections())
				}
				if m.selector.cursor+1 < count {
					m.selector.cursor++
				}
			case "ctrl+l":
				m.showLogs = !m.showLogs
			default:
				if editText(&m.selector.query, &m.selector.inputPos, v) {
					m.selector.cursor = 0
				}
			}
			return m, nil
		}
		if m.state == "connecting" {
			c := m.selectedConnection()
			if c == nil {
				return m, nil
			}
			switch key {
			case "n", "N":
				return m, m.addConnection()
			case "s", "S":
				return m, m.stopConnection(c.id, false)
			case "r", "R":
				return m, m.stopConnection(c.id, true)
			case "l", "L":
				c.showLogs = !c.showLogs
				c.recoveryPage = 0
			case "e", "E":
				m.openExportMenu()
			case "ctrl+s":
				m.openForm(c.effectiveProfile())
			}
			return m, nil
		}
		if m.state == "error" && (key == "r" || key == "R") {
			m.request.Refresh = true
			return m, m.search()
		}
		if m.state == "error" && key == "tab" {
			m.state = "list"
			m.tab = tabProfiles
		}
	}
	return m, nil
}
func (m *connectUI) View() string {
	width, height := m.dimensions()
	diagnostic, showLogs, page := m.recovery()
	current := Profile{}
	var progress *connectionProgress
	c := m.selectedConnection()
	if c != nil {
		current = c.effectiveProfile()
		progress = c.progress
	}
	if m.state == "actions" || m.state == "portForm" {
		current = m.editProfile
		progress = nil
	}
	if width < 60 || height < 16 {
		return fitScreen("60列×16行以上に広げてください。\nCtrl+C: この画面の全接続停止", width, height)
	}
	v := terminalView{width: width, height: height, color: m.selector.color, update: m.updateVersion}
	tabs := "Cloud SQL 接続ワークスペース"
	footer := []string{"↑↓ 選択  Enter 接続  Tab 切替  Ctrl+S 保存", "Ctrl+U 消去  Ctrl+L 注意  Esc 戻る  Ctrl+C 全接続停止"}
	if m.state == "list" {
		tabs = m.tabsView()

		if m.tab != tabConnections && m.showLogs && len(m.notices) > 0 {
			return v.frame("検索の詳細", tabs, "注意", v.panel("検索時のメッセージ", strings.Split(safeLog(strings.Join(m.notices, "\n")), "\n"), width, height-4), []string{"Ctrl+L 一覧へ戻る", "Esc 戻る  Ctrl+C この画面の全接続停止"})
		}
		c := choiceView{title: "インスタンス一覧", tabs: tabs, query: m.selector.query, pos: m.selector.inputPos, cursor: m.selector.cursor, footer: footer}
		if len(m.notices) > 0 {
			c.notice = fmt.Sprintf("! 検索注意 %d件", len(m.notices))
		}
		if m.tab == tabConnections {
			active := 0
			for _, conn := range m.connections {
				if conn.running {
					active++
				}
			}
			c.title = fmt.Sprintf("接続一覧（稼働 %d件 / 全%d件）", active, len(m.connections))
			c.footer = []string{"↑↓ 選択  Enter 詳細  Ctrl+D 個別停止  Tab 切替", "Ctrl+U 消去  Esc 戻る  Ctrl+C この画面の全接続停止"}
			for _, conn := range m.matchedConnections() {
				p := conn.effectiveProfile()
				row := instanceChoice(p.Instance)
				row.name = p.Instance.Name + " · " + conn.status()
				row.hint = fmt.Sprintf("%s / 127.0.0.1:%d", p.Project, p.Port)
				row.compact = []string{"状態  " + conn.status(), "DB " + p.Database + " / " + p.Username, fmt.Sprintf("Host 127.0.0.1:%d", p.Port)}
				row.details = append(row.details, "", conn.status(), fmt.Sprintf("Host 127.0.0.1:%d", p.Port), "DB "+p.Database, "ユーザー "+p.Username)
				c.rows = append(c.rows, row)
			}
		} else if m.tab == tabProfiles {
			c.title = "保存済み接続設定"
			for _, p := range m.matchedProfiles() {
				row := instanceChoice(p.Instance)
				row.name = p.Name
				row.hint = p.Project + " / " + p.Instance.Name
				row.summary = fmt.Sprintf("%s · 127.0.0.1:%d", p.Database, p.Port)
				row.compact = []string{"DB  " + p.Database, "ユーザー  " + p.Username, fmt.Sprintf("Host 127.0.0.1:%d", p.Port)}
				row.details = []string{"設定名", p.Name, "", "インスタンス", p.Instance.Name, "", "DB / ユーザー", p.Database, p.Username, "", "Host / Port", fmt.Sprintf("127.0.0.1:%d", p.Port)}
				c.rows = append(c.rows, row)
			}
		} else {
			for _, r := range m.selector.filtered() {
				c.rows = append(c.rows, instanceChoice(r))
			}
		}
		if m.tab == tabConnections {
			c.footer = []string{"↑↓ 選択  Enter 操作  Ctrl+E 出力  Ctrl+L ログ", "Tab 切替  Esc 戻る  Ctrl+C 全接続停止"}
			c.sideTitle, c.sideLines, _ = m.workspacePanel(width, height)
		} else if m.message != "" {
			c.sideTitle = "操作結果"
			c.sideLines = strings.Split(safeLog(m.message), "\n")
		}
		if m.tab == tabProfiles {
			c.footer = []string{"Enter 接続  Ctrl+O 操作/Port変更  Ctrl+S 別名保存", "Tab 切替  Esc 戻る  Ctrl+C 全接続停止"}
		}
		return v.choices(c)
	}
	title, badge := "接続ステータス", paint("● 処理中", tuiAccent, v.color)
	footer = []string{"L ログ  E エクスポート  Ctrl+S 保存  R 再試行", "N 追加  S 停止  Esc 接続一覧  Ctrl+C 全接続停止"}
	var lines []string
	spin := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}[m.frame%10]
	switch m.state {
	case "searching":
		title = "インスタンスを検索"
		badge = paint(spin+" 検索中", tuiAccent, v.color)
		lines = []string{"", spin + " アクセスできるCloud SQLを取得しています", "", "プロジェクトの数に応じて時間がかかる場合があります"}
		footer = []string{"Tab 保存済み設定・接続一覧へ切替", "Esc 戻る  Ctrl+C この画面の全接続停止"}
	case "actions":
		title = "選択行の操作"
		lines = []string{m.editProfile.Project + " / " + m.editProfile.Instance.Name, ""}
		for i, label := range m.workspaceActions() {
			marker := "  "
			if i == m.actionCursor {
				marker = "› "
			}
			lines = append(lines, marker+label)
		}
		footer = []string{"↑↓ / Tab 選択  Enter 実行", "Esc 一覧へ戻る  Ctrl+C 全接続停止"}
	case "portForm":
		title = "保存済みPortを変更"
		lines = []string{m.editProfile.Project + " / " + m.editProfile.Instance.Name, fmt.Sprintf("現在 %d → %s", m.portExpected, m.fields[0].value), "変更先Port  " + inputView(m.fields[0].value, m.fields[0].pos, width-22), "対象設定・案内（PgUp/PgDn）:"}
		var detail []string
		if m.message != "" {
			detail = strings.Split(ansi.Wrap(safeLog(m.message), width-4, ""), "\n")
		}
		detail = append(detail, m.portNames...)
		start := min(m.portPage*3, max(0, len(detail)-1))
		for _, line := range detail[start:min(len(detail), start+3)] {
			lines = append(lines, "  "+line)
		}
		footer = []string{"Enter / Ctrl+S 変更を保存  PgUp/PgDn 設定名", "Esc 変更せず一覧へ  Ctrl+C 全接続停止"}
	case "form":
		title = "接続設定を保存"
		badge = paint("✎ 入力", tuiAccent, v.color)
		lines = []string{"パスワードはDBツールで管理します", ""}
		for i, f := range m.fields {
			marker, text := "  ", f.value
			if i == m.focus {
				marker = "› "
				text = inputView(f.value, f.pos, width-28)
			}
			lines = append(lines, marker+f.label+"  "+text)
		}
		footer = []string{"Tab / ↑↓ 項目移動  Enter 次へ/保存  Ctrl+S 保存", "Esc 戻る  Ctrl+C この画面の全接続停止"}
	case "exportTools":
		title = "エクスポート先ツール"
		badge = paint("↓ 選択", tuiAccent, v.color)
		lines = []string{"出力形式を選んでください", ""}
		for i, tool := range exportTools {
			label := "  " + tool.label + "  (" + tool.suffix + ")"
			if i == m.exportCursor {
				label = paint("› "+tool.label+"  ("+tool.suffix+")", tuiAccent, v.color)
			}
			lines = append(lines, label)
		}
		if m.message == "" {
			lines = append(lines, "", fmt.Sprintf("設定 %s / 127.0.0.1:%d", m.editProfile.Name, m.editProfile.Port))
		}
		footer = []string{"↑↓ / Tab 選択  Enter 次へ", "Esc 一覧へ戻る  Ctrl+C この画面の全接続停止"}
	case "export":
		title = "接続情報を出力"
		badge = paint("↓ "+m.exportFormat, tuiAccent, v.color)
		// 出力先と操作を先に置き、長いJSONでも入力欄を隠さない。
		lines = []string{m.fields[0].label, inputView(m.fields[0].value, m.fields[0].pos, width-6), ""}
		if m.message != "" {
			text := m.message
			if strings.HasPrefix(text, "ファイルへ出力しました:") {
				text = "ファイルへ出力しました\n" + exportGuide(m.exportFormat)
			}
			lines = append(lines, ansi.Wrap(safeLog(text), width-4, ""), "")
		}
		room := max(0, height-6-len(strings.Split(strings.Join(lines, "\n"), "\n"))-1)
		preview := strings.Split(strings.TrimPrefix(m.exportData, "\ufeff"), "\n")
		if len(preview) > room {
			preview = append(preview[:room], "… 全体をファイルへ出力します")
		}
		lines = append(lines, preview...)
		footer = []string{"Enter 出力  左右 / Home / End 編集  Ctrl+U 消去", "Esc 戻る  Ctrl+C この画面の全接続停止"}
	case "saving":
		lines = []string{"", "保存中… 完了までお待ちください"}
		footer = []string{"保存処理を実行しています", "Ctrl+C 取消して終了"}
	case "stopping":
		badge = paint(spin+" 停止中", tuiAccent, v.color)
		lines = []string{"", "停止処理中… 所有Proxyと実行中の処理の終了を待っています"}
		footer = []string{"所有プロセスの終了を待っています", "Ctrl+C 終了"}
	case "error":
		footer = []string{"R 再検索  Tab 保存済み設定へ", "Esc 戻る  Ctrl+C この画面の全接続停止"}
		badge = paint("! 接続エラー", tuiError, v.color)
		lines = []string{"接続できませんでした"}
	case "stopped":
		badge = paint("○ 停止済み", tuiMuted, v.color)
		lines = []string{"Proxy停止済み。登録した接続設定は次回も使えます"}
	default:
		label := spin + " Proxy起動中…"
		if c != nil && !c.running {
			label = "○ 停止済み"
			badge = paint("○ 停止済み", tuiMuted, v.color)
			if c.err != nil {
				badge = paint("! 接続エラー", tuiError, v.color)
			}
		}
		if c != nil && c.stopping {
			label = spin + " 停止中"
			badge = paint("停止中", tuiMuted, v.color)
		}
		if progress != nil {
			s := progress.Snapshot()
			liveState := ""
			if c != nil && c.running && !c.stopping {
				liveState = s.State
			}
			switch liveState {
			case "checking":
				label = spin + " Cloud SQLへの接続確認中…"
			case "ready":
				label = "● Proxy利用可能 / DBログイン未検証"
				badge = paint("● 接続済み", tuiSuccess, v.color)
			case "error":
				label = "! 直近の接続試行でエラー"
				badge = paint("! 接続エラー", tuiError, v.color)
			}
			if s.Session.Port > 0 {
				lines = append(lines, fmt.Sprintf("Host 127.0.0.1  ·  Port %d", s.Session.Port), "セッション "+s.Session.ID, "")
			}
		}
		lines = append(lines, paint(label, tuiAccent, v.color))
	}
	recoveryVisible := diagnostic != nil && (m.state == "error" || m.state == "connecting" || m.state == "stopped")
	if recoveryVisible {
		// 原因と次の操作を優先し、長い管理版パスもページ送りで最後まで表示する。
		recovery := m.recoveryLines(width)
		pageHeight := max(1, height-6)
		pages := max(1, (len(recovery)+pageHeight-1)/pageHeight)
		pageIndex := min(*page, pages-1)
		start := pageIndex * pageHeight
		lines = recovery[start:min(len(recovery), start+pageHeight)]
		title = "接続エラーと対処"
		footer[0] = fmt.Sprintf("PgUp/PgDn 案内 %d/%d  L 詳細  R 再試行", pageIndex+1, pages)
	}
	if m.message != "" && m.state != "export" && m.state != "portForm" && !recoveryVisible {
		lines = append(lines, "", ansi.Wrap(safeLog(m.message), width-4, ""))
	}
	if current.ConnectionName != "" && m.state != "form" && m.state != "export" && m.state != "exportTools" && m.state != "error" {
		lines = append(lines, "", paint(current.Project+" / "+current.Instance.Name, tuiMuted, v.color), current.Region+" · "+current.DatabaseVersion, "DB "+current.Database+" / ユーザー "+current.Username)
	}
	if showLogs && progress != nil && diagnostic == nil {
		space := height - 7 - len(strings.Split(strings.Join(lines, "\n"), "\n"))
		if space > 0 {
			logs := progress.Snapshot().Logs
			if len(logs) > space {
				logs = logs[len(logs)-space:]
			}
			lines = append(lines, "ログ:")
			lines = append(lines, logs...)
		}
	}
	return v.frame("CLOUD SQL", tabs, badge, v.panel(title, strings.Split(strings.Join(lines, "\n"), "\n"), width, height-4), footer)
}

func (m *connectUI) dimensions() (int, int) {
	width, height := m.selector.width, m.selector.height
	if width < 1 {
		width = 80
	}
	if height < 1 {
		height = 24
	}
	return width, height
}
func (m *connectUI) tabsView() string {
	active := 0
	for _, c := range m.connections {
		if c.running {
			active++
		}
	}
	tabs := []string{"インスタンス", "保存済み設定", fmt.Sprintf("接続一覧 %d", active)}
	if m.addOnly {
		tabs = tabs[:2]
	}
	for i := range tabs {
		if connectionTab(i) == m.tab {
			tabs[i] = "[" + tabs[i] + "]"
		}
	}
	return strings.Join(tabs, "  ") + "  Tab 切替"
}
func (m *connectUI) recovery() (*Diagnostic, bool, *int) {
	if m.state == "connecting" {
		if c := m.selectedConnection(); c != nil {
			return c.diagnostic, c.showLogs, &c.recoveryPage
		}
	}
	return m.diagnostic, m.showLogs, &m.recoveryPage
}
func (m *connectUI) recoveryLines(width int) []string {
	d, logs, _ := m.recovery()
	if d == nil {
		return nil
	}
	text := d.Cause + "\n\n次の操作:\n" + d.Next
	if m.message != "" {
		text += "\n\n" + m.message
	}
	if logs {
		text += "\n\n詳細:\n" + d.Detail
		if c := m.selectedConnection(); c != nil && c.progress != nil {
			text += "\n" + strings.Join(c.progress.Snapshot().Logs, "\n")
		}
	}
	return strings.Split(ansi.Wrap(safeLog(text), max(1, width-4), ""), "\n")
}
func (a *App) runConnectModel(ctx context.Context, request connectRequest, seed *Profile) error {
	profiles, err := a.LoadProfiles()
	if err != nil {
		return err
	}
	uiCtx, cancelAll := context.WithCancel(ctx)
	defer cancelAll()
	m := &connectUI{a: a, ctx: uiCtx, cancelAll: cancelAll, request: request, profiles: profiles, state: "list", selector: selector{color: a.Env("NO_COLOR") == ""}}
	if len(profiles) > 0 && request.Query == "" && request.Project == "" && !request.Refresh && seed == nil {
		m.tab = tabProfiles
	}
	if seed != nil {
		m.addOnly = true
		m.seed = *seed
	}
	_, err = tea.NewProgram(m, tea.WithInput(a.In), tea.WithOutput(a.terminalOutput()), tea.WithContext(ctx), tea.WithoutSignalHandler(), tea.WithAltScreen()).Run()
	m.closeWork()
	m.workers.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	if seed != nil && m.saved.Name != "" {
		fmt.Fprintf(a.stdout(), "保存しました: %s / 127.0.0.1:%d\n", m.saved.Name, m.saved.Port)
	}
	return m.resultErr
}
func (a *App) RunConnectUI(ctx context.Context, request connectRequest) error {
	return a.runConnectModel(ctx, request, nil)
}
