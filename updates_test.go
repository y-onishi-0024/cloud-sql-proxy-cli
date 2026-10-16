package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type updateTransport func(*http.Request) (*http.Response, error)

func (f updateTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func updateModel(t *testing.T, a *App) *connectUI {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &connectUI{a: a, ctx: ctx, cancelAll: cancel, state: "list", tab: tabProfiles, profiles: []Profile{{Name: "dev", Instance: testInstance(), Database: "app", Username: "reader", Port: 55439}}, selector: selector{width: 60, height: 16}}
}

// 実際のInitコマンドを配送する。静的な保存済み一覧なので接続workerは起動しない。
func deliverStartup(m *connectUI) {
	queue := []tea.Cmd{m.Init()}
	for len(queue) > 0 {
		cmd := queue[0]
		queue = queue[1:]
		if cmd == nil {
			continue
		}
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			queue = append(queue, batch...)
		} else {
			m.Update(msg)
		}
	}
}

// 毎回の通信、文字列順での版比較、期限切れの再確認漏れを検出する。
func TestStartupUpdateNoticeCache(t *testing.T) {
	a, _, _ := testApp(t)
	a.TTY = true
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return now }
	calls := 0
	status := 200
	body := `{"tag_name":"v0.10.0","html_url":"https://untrusted.invalid/"}`
	a.HTTP = &http.Client{Transport: updateTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "GET" || r.URL.String() != "https://api.github.com/repos/y-onishi-0024/cloud-sql-proxy-cli/releases/latest" || r.Header.Get("Authorization") != "" {
			t.Fatalf("更新確認が認証情報か別の接続先を使用: %s", r.URL)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	for i := 0; i < 2; i++ {
		m := updateModel(t, a)
		deliverStartup(m)
		view := m.View()
		if !strings.Contains(view, "v0.10.0") || strings.Contains(view, "untrusted.invalid") {
			t.Fatal("新版と固定の更新先が案内されない: ", view)
		}
		if !strings.Contains(readUIHelp(m), updatePage) {
			t.Fatal("F1に固定更新先がない")
		}
		now = now.Add(23 * time.Hour)
	}
	if calls != 1 {
		t.Fatal("24時間以内に同じ確認を繰り返した")
	}
	body = `{"tag_name":"v0.11.0"}`
	m := updateModel(t, a)
	deliverStartup(m)
	if calls != 2 || !strings.Contains(m.View(), "v0.11.0") {
		t.Fatal("期限切れ後に最新版を確認しない")
	}
	// 時計が戻ったときに、未来の確認日時で永久に通信を抑止しない。
	now = now.Add(-time.Hour)
	body = `{"tag_name":"v0.12.0"}`
	m = updateModel(t, a)
	deliverStartup(m)
	if calls != 3 || !strings.Contains(m.View(), "v0.12.0") {
		t.Fatal("未来のキャッシュが再確認を抑止")
	}
	now = now.Add(25 * time.Hour)
	status = 403
	m = updateModel(t, a)
	deliverStartup(m)
	if !strings.Contains(m.View(), "v0.12.0") {
		t.Fatal("通信失敗で前回確認済みの更新案内が消えた")
	}
	status = 200
	body = `{"tag_name":"v0.13.0"}`
	if err := os.WriteFile(filepath.Join(a.Paths.CacheDir, "update-check.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	m = updateModel(t, a)
	deliverStartup(m)
	if !strings.Contains(m.View(), "v0.13.0") {
		t.Fatal("壊れたキャッシュから回復しない")
	}
	// キャッシュ先が利用できなくても、取得した案内は通常通り表示する。
	a.Paths.CacheDir = filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(a.Paths.CacheDir, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	m = updateModel(t, a)
	deliverStartup(m)
	if !strings.Contains(m.View(), "v0.13.0") || m.resultErr != nil {
		t.Fatal("キャッシュの書込失敗が通常操作へ影響")
	}
}

// 正常JSONの後の追記や非正式な版を、24時間キャッシュとして採用しない。
func TestStartupUpdateRefreshesCorruptCache(t *testing.T) {
	for _, tc := range []struct{ name, tag, tail string }{
		{"trailing-data", "v0.11.0", "broken"},
		{"second-json", "v0.11.0", `{}`},
		{"missing-prefix", "0.11.0", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _ := testApp(t)
			a.TTY = true
			file := filepath.Join(a.Paths.CacheDir, "update-check.json")
			if err := os.MkdirAll(a.Paths.CacheDir, 0700); err != nil {
				t.Fatal(err)
			}
			data := fmt.Sprintf(`{"checked_at":%q,"latest":%q}%s`, a.Now().Format(time.RFC3339Nano), tc.tag, tc.tail)
			if err := os.WriteFile(file, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			a.HTTP = &http.Client{Transport: updateTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"tag_name":"v0.12.0"}`))}, nil
			})}
			m := updateModel(t, a)
			deliverStartup(m)
			if calls != 1 || !strings.Contains(m.View(), "v0.12.0") {
				t.Fatal("壊れたキャッシュが再確認を抑止")
			}
		})
	}
}

// 古い版・不正な外部文字列・HTTPエラーで更新案内や通常操作のエラーを出さない。
func TestStartupUpdateIgnoresUnavailableRelease(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"same", `{"tag_name":"v0.7.0"}`, 200},
		{"older", `{"tag_name":"v0.2.2"}`, 200},
		{"prerelease", `{"tag_name":"v0.11.0","prerelease":true}`, 200},
		{"draft", `{"tag_name":"v0.11.0","draft":true}`, 200},
		{"suffix", `{"tag_name":"v0.11.0-rc.1"}`, 200},
		{"control", `{"tag_name":"v0.11.0\u001b[31m"}`, 200},
		{"overflow", `{"tag_name":"v9999999999999999999999.0.0"}`, 200},
		{"invalid-json", `{"tag_name":`, 200},
		{"oversized", `{"tag_name":"v0.11.0","body":"` + strings.Repeat("x", 256<<10) + `"}`, 200},
		{"rate-limited", `{"tag_name":"v0.11.0"}`, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, errs := testApp(t)
			a.TTY = true
			a.HTTP = &http.Client{Transport: updateTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			m := updateModel(t, a)
			deliverStartup(m)
			if strings.Contains(m.View(), "releases/latest") || m.state != "list" || m.resultErr != nil || errs.Len() != 0 {
				t.Fatal("確認失敗が通常操作や表示へ漏れた")
			}
		})
	}
}

// パイプ処理・補助コマンド・無効化指定からの通信と、無効化中のキャッシュ通知を防ぐ。
func TestStartupUpdateScopeAndOptOut(t *testing.T) {
	for _, mode := range []string{"non-tty", "disabled", "version", "help", "status", "profile-add"} {
		t.Run(mode, func(t *testing.T) {
			a, _, _ := testApp(t)
			a.TTY = mode != "non-tty"
			if err := writeJSON(filepath.Join(a.Paths.CacheDir, "update-check.json"), map[string]any{"checked_at": a.Now(), "latest": "v0.11.0"}); err != nil {
				t.Fatal(err)
			}
			if mode == "disabled" {
				a.Env = func(name string) string {
					if name == "CSQL_NO_UPDATE_CHECK" {
						return "1"
					}
					return ""
				}
			}
			a.HTTP = &http.Client{Transport: updateTransport(func(*http.Request) (*http.Response, error) {
				t.Error("更新確認の対象外で通信した")
				return nil, fmt.Errorf("通信対象外")
			})}
			switch mode {
			case "version", "help", "status":
				args := []string{mode}
				if mode == "help" {
					args = []string{"--help"}
				}
				if code := a.Run(context.Background(), args); code != 0 {
					t.Fatal("対象外コマンドの終了コードが変化")
				}
			default:
				m := updateModel(t, a)
				m.addOnly = mode == "profile-add"
				deliverStartup(m)
				if strings.Contains(m.View(), "releases/latest") {
					t.Fatal("無効化中にキャッシュ通知を表示")
				}
			}
		})
	}
}

// 更新APIが止まっていても入力でき、Ctrl+Cで応答待ちと書込を中断できる。
func TestStartupUpdateCheckCancellation(t *testing.T) {
	a, _, _ := testApp(t)
	a.TTY = true
	started := make(chan struct{})
	a.HTTP = &http.Client{Transport: updateTransport(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > 2*time.Second {
			t.Error("更新APIの待機上限が2秒を超える")
		}
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	m := updateModel(t, a)
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("起動時に更新確認を開始しない")
	}
	// 保存済み一覧には他の初期workerがなく、更新確認だけが残る。
	done := make(chan tea.Msg, 1)
	go func() {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, part := range batch {
				msg = part()
			}
		}
		done <- msg
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("更新確認が始まらない")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("dev")})
	if m.selector.query != "dev" {
		t.Fatal("更新確認中に検索入力できない")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	select {
	case msg := <-done:
		m.Update(msg)
		m.workers.Wait()
	case <-time.After(time.Second):
		t.Fatal("終了時に更新確認が残留")
	}
	if _, err := os.Stat(filepath.Join(a.Paths.CacheDir, "update-check.json")); !os.IsNotExist(err) {
		t.Fatal("取消後に更新キャッシュを書き込んだ")
	}
}

// 通知が本文や操作ガイドを押し出さず、画面遷移後も更新先へ到達できる。
func TestStartupUpdateNoticeLayouts(t *testing.T) {
	a, _, _ := testApp(t)
	a.TTY = true
	a.HTTP = &http.Client{Transport: updateTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"tag_name":"v0.11.0"}`))}, nil
	})}
	m := updateModel(t, a)
	deliverStartup(m)
	for _, size := range [][2]int{{60, 16}, {100, 20}, {120, 26}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, state := range []string{"list", "searching", "form", "export", "connecting", "error", "stopped", "saving", "stopping"} {
			m.state = state
			m.fields = []formField{{label: "設定名", value: "dev"}}
			view := ansi.Strip(m.View())
			if !strings.Contains(view, "v0.11.0") || !strings.Contains(view, "Ctrl+C") || !strings.Contains(view, "保存済み設定") {
				t.Fatalf("%dx%d %sで更新先または終了案内が消える: %s", size[0], size[1], state, view)
			}
			lines := strings.Split(view, "\n")
			if len(lines) > size[1] {
				t.Fatal("通知で画面の高さを超える")
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > size[0] {
					t.Fatal("通知で画面幅を超える")
				}
			}
		}
	}
}
