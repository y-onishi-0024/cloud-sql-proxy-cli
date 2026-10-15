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
	v := terminalView{width, height, s.color}
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

// connectUIは表示だけを所有し、API・保存・Proxyの待機はCmdへ渡す。
type connectRequest struct {
	Query, Project, ProfileName string
	Refresh, PrivateIP          bool
	Port                        int
}
type uiResult struct {
	generation int
	kind       string
	result     DiscoveryResult
	notices    []string
	profile    Profile
	data       string
	err        error
}
type uiTick struct{ generation int }
type formField struct {
	label, value string
	pos          int
}
type connectUI struct {
	a                        *App
	ctx                      context.Context
	request                  connectRequest
	selector                 selector
	profiles                 []Profile
	profileTab               bool
	state                    string
	frame                    int
	recoveryPage             int
	generation               int
	running                  bool
	workerKind               string
	cancel                   context.CancelFunc
	cancelAll                context.CancelFunc
	workers                  sync.WaitGroup
	workMu                   sync.Mutex
	workClosed               bool
	progress                 *connectionProgress
	current                  Profile
	fields                   []formField
	focus                    int
	formBack                 string
	addOnly                  bool
	seed                     Profile
	saved                    Profile
	message                  string
	diagnostic               *Diagnostic
	showLogs                 bool
	exportFormat, exportData string
	exit, retry              bool
	resultErr                error
	notices                  []string
}

// Bubble Teaが未配送Cmdを破棄しても待機を残さず、終了後の遅いCmdも開始させない。
func (m *connectUI) work(ctx context.Context, generation int, kind string, run tea.Cmd) tea.Cmd {
	return func() tea.Msg {
		m.workMu.Lock()
		if m.workClosed || ctx.Err() != nil {
			m.workMu.Unlock()
			return uiResult{generation: generation, kind: kind, err: context.Canceled}
		}
		m.workers.Add(1)
		m.workMu.Unlock()
		defer m.workers.Done()
		return run()
	}
}

func (m *connectUI) tick() tea.Cmd {
	generation := m.generation
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return uiTick{generation: generation} })
}
func (m *connectUI) Init() tea.Cmd {
	if m.request.ProfileName != "" {
		p, err := findProfile(m.profiles, m.request.ProfileName)
		if err != nil {
			m.fail(err, "local")
			return nil
		}
		return m.startConnect(p)
	}
	if m.profileTab {
		return nil
	}
	return m.search()
}
func (m *connectUI) search() tea.Cmd {
	if m.running {
		return nil
	}
	m.state = "searching"
	m.running = true
	m.workerKind = "search"
	m.generation++
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	generation := m.generation
	request, a := m.request, m.a
	return tea.Batch(m.tick(), m.work(ctx, generation, "search", func() tea.Msg {
		var notices []string
		r, err := a.discover(ctx, request.Project, request.Refresh, func(s string) { notices = append(notices, s) })
		if err == nil && request.Query != "" && !request.Refresh && len(MatchInstances(r.Instances, request.Query)) == 0 {
			r, err = a.discover(ctx, request.Project, true, func(s string) { notices = append(notices, s) })
		}
		return uiResult{generation: generation, kind: "search", result: r, notices: notices, err: err}
	}))
}
func (m *connectUI) startConnect(p Profile) tea.Cmd {
	if m.running {
		return nil
	}
	m.current = p
	m.state = "connecting"
	m.recoveryPage = 0
	m.diagnostic = nil
	m.message = ""
	m.progress = &connectionProgress{}
	m.running = true
	m.workerKind = "connect"
	m.generation++
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	generation := m.generation
	progress, a := m.progress, m.a
	port := p.Port
	if m.request.Port != 0 {
		port = m.request.Port
	}
	private := p.PrivateIP
	return tea.Batch(m.tick(), m.work(ctx, generation, "connect", func() tea.Msg {
		err := a.Connect(ctx, p.Instance, ConnectOptions{Port: port, PrivateIP: private, Progress: progress})
		return uiResult{generation: generation, kind: "connect", err: err}
	}))
}
func (m *connectUI) fail(err error, source string) {
	fc := m.a.failureContext(m.ctx, source, map[string]string{"gcloud": "インスタンス検索", "proxy": "Cloud SQLへの接続", "local": "操作"}[source], m.current.Instance, m.current.PrivateIP)
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
	if m.profileTab {
		rows := m.matchedProfiles()
		if m.selector.cursor >= len(rows) {
			return Profile{}, false
		}
		return rows[m.selector.cursor], true
	}
	rows := m.selector.filtered()
	if m.selector.cursor >= len(rows) {
		return Profile{}, false
	}
	return Profile{Instance: rows[m.selector.cursor], Port: m.request.Port, PrivateIP: m.request.PrivateIP}, true
}
func (m *connectUI) openForm(p Profile) {
	back := m.state
	if back == "error" {
		back = "list"
	}
	m.formBack = back
	if m.addOnly {
		p.Name = m.seed.Name
		p.Database = m.seed.Database
		p.Username = m.seed.Username
		p.Port = m.seed.Port
		p.PrivateIP = m.seed.PrivateIP
	}
	if m.progress != nil && m.running && m.progress.Snapshot().Session.Port != 0 {
		p.Port = m.progress.Snapshot().Session.Port
	}
	m.current = p
	port := ""
	if p.Port > 0 {
		port = strconv.Itoa(p.Port)
	}
	m.fields = []formField{{label: "設定名", value: p.Name, pos: len([]rune(p.Name))}, {label: "DB名", value: p.Database, pos: len([]rune(p.Database))}, {label: "DBユーザー", value: p.Username, pos: len([]rune(p.Username))}, {label: "Port（空なら自動）", value: port, pos: len(port)}}
	m.focus = 0
	m.state = "form"
	m.message = ""
	m.diagnostic = nil
}
func (m *connectUI) saveForm() tea.Cmd {
	p := m.current
	p.Name = m.fields[0].value
	p.Database = m.fields[1].value
	p.Username = m.fields[2].value
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
	// 稼働中と違うPortを保存すると、出力した接続情報が実際のProxyからずれる。
	if m.running && m.workerKind == "connect" {
		livePort := m.current.Port
		if m.progress != nil && m.progress.Snapshot().Session.Port != 0 {
			livePort = m.progress.Snapshot().Session.Port
		}
		if livePort == 0 || p.Port != livePort {
			m.message = "接続中のPortは変更できません。Proxyを停止してから保存してください"
			return nil
		}
	}
	// 保存/出力中は接続の終了通知でも画面移動を解禁しない。
	if m.running && m.workerKind != "connect" {
		return nil
	}
	m.state = "saving"
	m.message = "保存中…"
	generation := m.generation
	a := m.a
	ctx := m.ctx
	return m.work(ctx, generation, "save", func() tea.Msg {
		if err := ctx.Err(); err != nil {
			return uiResult{generation: generation, kind: "save", err: err}
		}
		prepared, err := a.PrepareProfile(p)
		if err == nil && previewOnly {
			return uiResult{generation: generation, kind: "prepared", profile: prepared}
		}
		if err == nil {
			err = a.AddProfile(ctx, prepared)
		}
		return uiResult{generation: generation, kind: "save", profile: prepared, err: err}
	})
}
func (m *connectUI) beginExport(format string) {
	if m.current.Name == "" {
		m.message = "先に接続設定を保存してください"
		m.openForm(m.current)
		return
	}
	data, err := profileExport(m.current, format)
	if err != nil {
		m.message = err.Error()
		return
	}
	path := ""
	label := "新規出力パス（空で戻る）"
	if format == "pgadmin" {
		dir, err := os.Getwd()
		if err != nil {
			m.message = "出力先フォルダを確認できません: " + safeLog(err.Error())
			return
		}
		// 既存ファイル・ディレクトリ・リンクを避けた候補だけを提示する。
		// 保存時も共通のO_EXCL検査を使い、確認後に生じた競合を上書きしない。
		base := filepath.Join(dir, "csql-"+m.current.Name+"-pgadmin")
		for i := 0; i < 1000; i++ {
			candidate := base + ".json"
			if i > 0 {
				candidate = fmt.Sprintf("%s-%d.json", base, i+1)
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
		label = "出力パス（Enterで保存・編集可 / Escで戻る）"
	}
	m.exportFormat = format
	m.exportData = string(data)
	m.state = "export"
	m.fields = []formField{{label: label, value: path, pos: len([]rune(path))}}
	m.focus = 0
	m.message = ""
}
func (m *connectUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.selector.width = v.Width
		m.selector.height = v.Height
		return m, nil
	case uiTick:
		if v.generation != m.generation {
			return m, nil
		}
		if m.running {
			m.frame = (m.frame + 1) % 10
			if m.workerKind == "connect" && m.progress != nil {
				if s := m.progress.Snapshot(); s.Diagnostic != nil {
					if m.diagnostic == nil || m.diagnostic.Detail != s.Diagnostic.Detail {
						m.recoveryPage = 0
					}
					m.diagnostic = s.Diagnostic
				}
			}
			return m, m.tick()
		}
		return m, nil
	case uiResult:
		if v.generation != m.generation {
			return m, nil
		}
		if v.kind == "search" || v.kind == "connect" {
			m.running = false
			if m.cancel != nil {
				m.cancel()
				m.cancel = nil
			}
		}
		if m.exit {
			if !m.running && v.kind != "save" {
				return m, tea.Quit
			}
			if v.kind == "save" {
				return m, tea.Quit
			}
			return m, nil
		}
		switch v.kind {
		case "prepared":
			m.state = "form"
			m.fields[3].value = strconv.Itoa(v.profile.Port)
			m.fields[3].pos = len(m.fields[3].value)
			m.message = fmt.Sprintf("保存予定のPortは %d です。Enter / Ctrl+Sで保存します", v.profile.Port)
			return m, nil
		case "search":
			m.notices = v.notices
			for _, f := range v.result.Failures {
				m.notices = append(m.notices, "検索失敗 ["+f.Project+"]: "+f.Message)
			}
			if v.err != nil {
				m.fail(v.err, "gcloud")
				return m, nil
			}
			m.selector.rows = v.result.Instances
			m.state = "list"
			m.selector.query = ""
			m.selector.inputPos = 0
			m.selector.cursor = 0
			if m.request.Query != "" {
				m.selector.rows = MatchInstances(v.result.Instances, m.request.Query)
				if m.addOnly {
					var exact []Instance
					for _, r := range v.result.Instances {
						if r.Name == m.request.Query {
							exact = append(exact, r)
						}
					}
					m.selector.rows = exact
				}
			}
			if len(m.selector.rows) == 0 {
				m.fail(fmt.Errorf("一致するインスタンスがありません。検索語・--project・--refreshを確認してください"), "local")
				return m, nil
			}
			if m.addOnly && len(m.selector.rows) == 1 {
				m.openForm(Profile{Instance: m.selector.rows[0]})
				return m, nil
			}
			if !m.addOnly && m.request.Query != "" && len(m.selector.rows) == 1 {
				return m, m.startConnect(Profile{Instance: m.selector.rows[0], Port: m.request.Port, PrivateIP: m.request.PrivateIP})
			}
		case "connect":
			if m.retry {
				m.retry = false
				return m, m.startConnect(m.current)
			}
			previousState := m.state
			if v.err != nil && !errors.Is(v.err, context.Canceled) {
				m.fail(v.err, "proxy")
			} else {
				m.state = "stopped"
				m.resultErr = nil
			}
			// フォーム・出力・保存の完了を、接続workerの終了と混同しない。
			if previousState == "saving" || previousState == "form" || previousState == "export" {
				m.state = previousState
			}
		case "save":
			if v.err != nil {
				m.state = "form"
				m.message = safeLog(v.err.Error())
				return m, nil
			}
			m.saved = v.profile
			m.current = v.profile
			m.profiles, _ = m.a.LoadProfiles()
			m.message = fmt.Sprintf("保存しました: %s / 127.0.0.1:%d（DBログイン未検証）", v.profile.Name, v.profile.Port)
			if m.addOnly {
				return m, tea.Quit
			}
			if m.running {
				m.state = "connecting"
			} else if m.formBack == "list" {
				return m, m.startConnect(v.profile)
			} else if m.resultErr != nil {
				m.state = "error"
			} else {
				m.state = "stopped"
			}
		case "export":
			m.state = "export"
			if v.err != nil {
				m.message = safeLog(v.err.Error())
			} else {
				m.message = "ファイルへ出力しました: " + v.data + "\npgAdminは初回だけTools → Import/Export Serversで取込み（既存サーバーの削除は無効）"
			}
		}
		return m, nil
	case tea.KeyMsg:
		key := v.String()
		if key == "ctrl+c" {
			m.exit = true
			m.resultErr = ErrCancelled
			if m.cancelAll != nil {
				m.cancelAll()
			}
			if m.cancel != nil {
				m.cancel()
			}
			if m.running || m.state == "saving" {
				m.state = "stopping"
				return m, nil
			}
			return m, tea.Quit
		}
		if m.selector.width > 0 && (m.selector.width < 60 || m.selector.height < 16) && key != "esc" {
			return m, nil
		}
		if m.state == "saving" || m.state == "stopping" {
			return m, nil
		}
		if m.diagnostic != nil && (m.state == "error" || m.state == "connecting" || m.state == "stopped") && (key == "pgup" || key == "pgdown") {
			width, height := m.selector.width, m.selector.height
			if width < 1 {
				width = 80
			}
			if height < 1 {
				height = 24
			}
			pages := max(1, (len(m.recoveryLines(width))+max(1, height-6)-1)/max(1, height-6))
			if key == "pgup" {
				m.recoveryPage = max(0, min(m.recoveryPage, pages-1)-1)
			} else {
				m.recoveryPage = min(m.recoveryPage+1, pages-1)
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
				if !m.running && m.formBack != "list" {
					m.state = "stopped"
					if m.resultErr != nil {
						m.state = "error"
					}
				}
				return m, nil
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
		if m.state == "export" {
			if key == "esc" {
				m.state = "connecting"
				if !m.running {
					m.state = "stopped"
					if m.resultErr != nil {
						m.state = "error"
					}
				}
				return m, nil
			}
			if key == "enter" {
				path := m.fields[0].value
				if path == "" {
					m.state = "connecting"
					if !m.running {
						m.state = "stopped"
						if m.resultErr != nil {
							m.state = "error"
						}
					}
					return m, nil
				}
				data := []byte(m.exportData)
				generation := m.generation
				m.state = "saving"
				ctx := m.ctx
				return m, m.work(ctx, generation, "export", func() tea.Msg {
					if err := ctx.Err(); err != nil {
						return uiResult{generation: generation, kind: "export", err: err}
					}
					err := writeProfileExport(path, data)
					return uiResult{generation: generation, kind: "export", data: path, err: err}
				})
			}
			editText(&m.fields[0].value, &m.fields[0].pos, v)
			return m, nil
		}
		if m.running && m.workerKind == "search" {
			if key == "esc" {
				m.exit = true
				m.resultErr = ErrCancelled
				m.cancel()
				m.state = "stopping"
			}
			return m, nil
		}
		if m.state == "list" {
			switch key {
			case "esc":
				m.resultErr = ErrCancelled
				return m, tea.Quit
			case "tab":
				m.profileTab = !m.profileTab
				m.selector.query = ""
				m.selector.inputPos = 0
				m.selector.cursor = 0
				if !m.profileTab && len(m.selector.rows) == 0 {
					return m, m.search()
				}
			case "ctrl+s":
				if p, ok := m.selected(); ok {
					m.openForm(p)
				}
			case "enter":
				if p, ok := m.selected(); ok {
					if m.addOnly {
						m.openForm(p)
					} else {
						return m, m.startConnect(p)
					}
				}
			case "up", "ctrl+p":
				if m.selector.cursor > 0 {
					m.selector.cursor--
				}
			case "down", "ctrl+n":
				count := len(m.selector.filtered())
				if m.profileTab {
					count = len(m.matchedProfiles())
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
		switch key {
		case "l", "L":
			m.showLogs = !m.showLogs
			m.recoveryPage = 0
		case "e", "E":
			m.beginExport("pgadmin")
		case "u", "U":
			m.beginExport("uri")
		case "ctrl+s":
			m.openForm(m.current)
		case "r", "R":
			if m.running {
				m.retry = true
				m.cancel()
				m.state = "stopping"
			} else if m.current.ConnectionName != "" {
				return m, m.startConnect(m.current)
			} else {
				m.request.Refresh = true
				return m, m.search()
			}
		case "esc":
			if m.running {
				m.cancel()
				m.state = "stopping"
			} else {
				m.state = "list"
				m.resultErr = nil
			}
		}
		return m, nil
	}
	return m, nil
}
func (m *connectUI) View() string {
	width, height := m.selector.width, m.selector.height
	if width < 1 {
		width = 80
	}
	if height < 1 {
		height = 24
	}
	if width < 60 || height < 16 {
		return fitScreen("60列×16行以上に広げてください。\nCtrl+C: 所有Proxyを停止して終了", width, height)
	}
	v := terminalView{width, height, m.selector.color}
	tabs := "Cloud SQL 接続ワークスペース"
	footer := []string{"↑↓ 選択  Enter 接続  Tab 切替  Ctrl+S 保存", "Ctrl+U 消去  Ctrl+L 注意  Esc 取消  Ctrl+C 終了"}
	if m.state == "list" {
		tabs = paint("[インスタンス]", tuiAccent, v.color) + "  Tab → 保存済み設定"
		if m.profileTab {
			tabs = "インスタンス ← Tab  " + paint("[保存済み設定]", tuiAccent, v.color)
		}
		if m.showLogs && len(m.notices) > 0 {
			return v.frame("検索の詳細", tabs, "注意", v.panel("検索時のメッセージ", strings.Split(safeLog(strings.Join(m.notices, "\n")), "\n"), width, height-4), []string{"Ctrl+L 一覧へ戻る", "Esc 取消  Ctrl+C 終了"})
		}
		c := choiceView{title: "インスタンス一覧", tabs: tabs, query: m.selector.query, pos: m.selector.inputPos, cursor: m.selector.cursor, footer: footer}
		if len(m.notices) > 0 {
			c.notice = fmt.Sprintf("! 検索注意 %d件", len(m.notices))
		}
		if m.profileTab {
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
		return v.choices(c)
	}
	title, badge := "接続ステータス", paint("● 処理中", tuiAccent, v.color)
	footer = []string{"L ログ  E pgAdmin  U URI  Ctrl+S 保存  R 再試行", "Esc 停止/戻る  Ctrl+C 終了"}
	var lines []string
	spin := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}[m.frame%10]
	switch m.state {
	case "searching":
		title = "インスタンスを検索"
		badge = paint(spin+" 検索中", tuiAccent, v.color)
		lines = []string{"", spin + " アクセスできるCloud SQLを取得しています", "", "プロジェクトの数に応じて時間がかかる場合があります"}
		footer = []string{"検索結果が揃うまでお待ちください", "Esc 取消  Ctrl+C 終了"}
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
		footer = []string{"Tab / ↑↓ 項目移動  Enter 次へ/保存  Ctrl+S 保存", "Esc 戻る  Ctrl+C 終了"}
	case "export":
		title = "接続情報を出力"
		badge = paint("↓ "+m.exportFormat, tuiAccent, v.color)
		// 出力先と操作を先に置き、長いJSONでも入力欄を隠さない。
		lines = []string{m.fields[0].label, inputView(m.fields[0].value, m.fields[0].pos, width-6), ""}
		if m.message != "" {
			lines = append(lines, ansi.Wrap(safeLog(m.message), width-4, ""), "")
		}
		room := max(0, height-6-len(strings.Split(strings.Join(lines, "\n"), "\n"))-1)
		preview := strings.Split(m.exportData, "\n")
		if len(preview) > room {
			preview = append(preview[:room], "… 全体をファイルへ出力します")
		}
		lines = append(lines, preview...)
		footer = []string{"Enter 出力  左右 / Home / End 編集  Ctrl+U 消去", "Esc 戻る  Ctrl+C 終了"}
	case "saving":
		lines = []string{"", "保存中… 完了までお待ちください"}
		footer = []string{"保存処理を実行しています", "Ctrl+C 取消して終了"}
	case "stopping":
		badge = paint(spin+" 停止中", tuiAccent, v.color)
		lines = []string{"", "停止処理中… 所有Proxyと実行中の処理の終了を待っています"}
		footer = []string{"所有プロセスの終了を待っています", "Ctrl+C 終了"}
	case "error":
		badge = paint("! 接続エラー", tuiError, v.color)
		lines = []string{"接続できませんでした"}
	case "stopped":
		badge = paint("○ 停止済み", tuiMuted, v.color)
		lines = []string{"Proxy停止済み。登録した接続設定は次回も使えます"}
	default:
		label := spin + " Proxy起動中…"
		if m.progress != nil {
			s := m.progress.Snapshot()
			switch s.State {
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
	recoveryVisible := m.diagnostic != nil && (m.state == "error" || m.state == "connecting" || m.state == "stopped")
	if recoveryVisible {
		// 原因と次の操作を優先し、長い管理版パスもページ送りで最後まで表示する。
		recovery := m.recoveryLines(width)
		pageHeight := max(1, height-6)
		pages := max(1, (len(recovery)+pageHeight-1)/pageHeight)
		page := min(m.recoveryPage, pages-1)
		start := page * pageHeight
		lines = recovery[start:min(len(recovery), start+pageHeight)]
		title = "接続エラーと対処"
		footer[0] = fmt.Sprintf("PgUp/PgDn 案内 %d/%d  L 詳細  R 再試行", page+1, pages)
	}
	if m.message != "" && m.state != "export" && !recoveryVisible {
		lines = append(lines, "", ansi.Wrap(safeLog(m.message), width-4, ""))
	}
	if m.current.ConnectionName != "" && m.state != "form" && m.state != "export" && m.state != "error" {
		lines = append(lines, "", paint(m.current.Project+" / "+m.current.Instance.Name, tuiMuted, v.color), m.current.Region+" · "+m.current.DatabaseVersion)
	}
	if m.showLogs && m.progress != nil && m.diagnostic == nil {
		space := height - 7 - len(strings.Split(strings.Join(lines, "\n"), "\n"))
		if space > 0 {
			logs := m.progress.Snapshot().Logs
			if len(logs) > space {
				logs = logs[len(logs)-space:]
			}
			lines = append(lines, "ログ:")
			lines = append(lines, logs...)
		}
	}
	return v.frame("CLOUD SQL", tabs, badge, v.panel(title, strings.Split(strings.Join(lines, "\n"), "\n"), width, height-4), footer)
}

func (m *connectUI) recoveryLines(width int) []string {
	text := m.diagnostic.Cause + "\n\n次の操作:\n" + m.diagnostic.Next
	// 保存完了もページ数に含め、復旧案内が画面を埋めても到達できるようにする。
	if m.message != "" {
		text += "\n\n" + m.message
	}
	if m.showLogs {
		text += "\n\n詳細:\n" + m.diagnostic.Detail
		if m.progress != nil {
			text += "\n" + strings.Join(m.progress.Snapshot().Logs, "\n")
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
	m := &connectUI{a: a, ctx: uiCtx, cancelAll: cancelAll, request: request, profiles: profiles, profileTab: len(profiles) > 0 && request.Query == "" && request.Project == "" && !request.Refresh && seed == nil, state: "list", selector: selector{color: a.Env("NO_COLOR") == ""}}
	if seed != nil {
		m.addOnly = true
		m.seed = *seed
	}
	_, err = tea.NewProgram(m, tea.WithInput(a.In), tea.WithOutput(a.terminalOutput()), tea.WithContext(ctx), tea.WithoutSignalHandler(), tea.WithAltScreen()).Run()
	m.workMu.Lock()
	m.workClosed = true
	m.workMu.Unlock()
	cancelAll()
	if m.cancel != nil {
		m.cancel()
	}
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
