package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// 接続の所有権は画面内ID、結果の配送先はIDと試行番号で固定する。
// 管理セッションIDや検索世代とは独立し、選択を変えても処理を混線させない。
type uiConnection struct {
	id, attempt              uint64
	profile                  Profile
	progress                 *connectionProgress
	cancel                   context.CancelFunc
	running, stopping, retry bool
	err                      error
	diagnostic               *Diagnostic
	showLogs                 bool
	recoveryPage             int
}
type connectionFinished struct {
	id, attempt uint64
	err         error
}

func (m *connectUI) connection(id uint64) *uiConnection {
	for _, c := range m.connections {
		if c.id == id {
			return c
		}
	}
	return nil
}
func (c *uiConnection) effectiveProfile() Profile {
	p := c.profile
	if c.progress != nil && (c.running || p.Port == 0) {
		if port := c.progress.Snapshot().Session.Port; port > 0 {
			p.Port = port
		}
	}
	return p
}
func (c *uiConnection) status() string {
	if c.stopping {
		return "停止中"
	}
	if !c.running {
		if c.err != nil {
			return "エラー"
		}
		return "停止済み"
	}
	if c.progress != nil {
		switch c.progress.Snapshot().State {
		case "ready":
			return "Proxy利用可能"
		case "checking":
			return "接続確認中"
		case "error":
			return "エラー"
		}
	}
	return "起動中"
}
func (m *connectUI) hasOwnedWork() bool {
	for _, c := range m.connections {
		if c.running {
			return true
		}
	}
	return false
}
func (m *connectUI) matchedConnections() []*uiConnection {
	var rows []*uiConnection
	q := strings.ToLower(m.selector.query)
	for _, c := range m.connections {
		p := c.effectiveProfile()
		if strings.Contains(strings.ToLower(fmt.Sprintf("%s %s %s %s %s %d %s", p.Project, p.Instance.Name, p.Name, p.Database, p.Username, p.Port, c.status())), q) {
			rows = append(rows, c)
		}
	}
	return rows
}
func (m *connectUI) startConnection(p Profile) tea.Cmd {
	if m.exit {
		return nil
	}
	if len(m.connections) == 0 && m.request.Port != 0 {
		p.Port = m.request.Port
	}
	// 稼働中を先に照合し、古い停止履歴より現在のProxyへ移動する。
	for _, c := range m.connections {
		if c.running && c.profile.ConnectionName == p.ConnectionName {
			m.focusConnection(c, p)
			return nil
		}
	}
	for _, c := range m.connections {
		live := c.effectiveProfile()
		if !c.running && live.ConnectionName == p.ConnectionName && live.PrivateIP == p.PrivateIP && (p.Port == 0 || p.Port == live.Port) {
			m.focusConnection(c, p)
			return nil
		}
	}
	m.nextID++
	c := &uiConnection{id: m.nextID, profile: p}
	m.connections = append(m.connections, c)
	m.selectedID = c.id
	m.showConnections()
	m.message = ""
	m.diagnostic = nil
	return m.launchConnection(c)
}

// 同じProxyの再利用では選択した保存済み設定のDB・ユーザーを保持する。
func (m *connectUI) focusConnection(c *uiConnection, p Profile) {
	m.selectedID = c.id
	m.showConnections()
	m.message = ""
	live := c.effectiveProfile()
	if p.PrivateIP != live.PrivateIP || (p.Port != 0 && p.Port != live.Port) {
		m.message = fmt.Sprintf("このインスタンスは既に %s / Port %d で接続処理中です。変更するにはSで停止後、追加してください", map[bool]string{true: "Private IP", false: "Public IP"}[live.PrivateIP], live.Port)
	} else if p.Name != "" {
		c.profile = p
	}
}
func (m *connectUI) launchConnection(c *uiConnection) tea.Cmd {
	for _, other := range m.connections {
		if other != c && other.running && other.profile.ConnectionName == c.profile.ConnectionName {
			m.focusConnection(other, c.profile)
			return nil
		}
	}
	c.attempt++
	c.progress = &connectionProgress{}
	c.running, c.stopping, c.retry = true, false, false
	c.err, c.diagnostic = nil, nil
	c.recoveryPage = 0
	ctx, cancel := context.WithCancel(m.ctx)
	c.cancel = cancel
	id, attempt, p, progress, a := c.id, c.attempt, c.profile, c.progress, m.a
	cancelled := connectionFinished{id: id, attempt: attempt, err: context.Canceled}
	return tea.Batch(m.ensureTick(), m.work(ctx, cancelled, func() tea.Msg {
		return connectionFinished{id: id, attempt: attempt, err: a.Connect(ctx, p.Instance, ConnectOptions{Port: p.Port, PrivateIP: p.PrivateIP, Progress: progress})}
	}))
}
func (m *connectUI) stopConnection(id uint64, retry bool) tea.Cmd {
	c := m.connection(id)
	if c == nil || m.exit {
		return nil
	}
	if c.running {
		c.retry = retry
		c.stopping = true
		if c.cancel != nil {
			c.cancel()
		}
		return m.ensureTick()
	}
	if retry {
		return m.launchConnection(c)
	}
	return nil
}
func (m *connectUI) finishConnection(v connectionFinished) tea.Cmd {
	c := m.connection(v.id)
	if c == nil || c.attempt != v.attempt || !c.running {
		return nil
	}
	c.running, c.stopping = false, false
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.err = nil
	if v.err != nil && !errors.Is(v.err, context.Canceled) {
		c.err = v.err
		fc := m.a.failureContext(m.ctx, "proxy", "Cloud SQLへの接続", c.profile.Instance, c.profile.PrivateIP)
		var de diagnosticError
		if errors.As(v.err, &de) {
			fc = de.Context
		}
		d := explainFailure(fc, v.err.Error())
		c.diagnostic = &d
		c.recoveryPage = 0
	}
	if c.retry && !m.exit {
		return m.launchConnection(c)
	}
	c.retry = false
	return nil
}
