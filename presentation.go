package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// 描画だけをまとめ、接続・保存・取消の状態はselection.goに残す。
type terminalView struct {
	width, height int
	color         bool
	update        string
}
type terminalChoice struct {
	name, hint, summary string
	details             []string
	compact             []string
}
type choiceView struct {
	sideTitle                  string
	sideLines                  []string
	title, tabs, query, notice string
	pos, cursor                int
	rows                       []terminalChoice
	footer                     []string
}

const (
	tuiAccent  = "#78DCE8"
	tuiMuted   = "#8995A8"
	tuiSuccess = "#A9DC76"
	tuiError   = "#FF6188"
)

func paddedLine(s string, width int) string {
	s = ansi.Truncate(s, width, "…")
	return s + strings.Repeat(" ", max(0, width-ansi.StringWidth(s)))
}

func (v terminalView) panel(title string, lines []string, width, height int) string {
	title = ansi.Truncate(title, max(0, width-6), "…")
	top := "╭─ " + title + " " + strings.Repeat("─", max(0, width-ansi.StringWidth(title)-5)) + "╮"
	result := []string{paint(top, tuiMuted, v.color)}
	for i := 0; i < height-2; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		result = append(result, paint("│ ", tuiMuted, v.color)+paddedLine(line, width-4)+paint(" │", tuiMuted, v.color))
	}
	result = append(result, paint("╰"+strings.Repeat("─", max(0, width-2))+"╯", tuiMuted, v.color))
	return strings.Join(result, "\n")
}

// 操作ガイドの2行を先に確保し、長い本文やログで隠さない。
func (v terminalView) frame(title, subtitle, badge, body string, footer []string) string {
	heading := paint(" csql ", tuiAccent, v.color) + " " + title
	if v.update != "" {
		// 既存のヘッダーだけを使い、最小画面でも本文と操作ガイドの高さを保つ。
		heading = paint(" csql ", tuiAccent, v.color) + " 更新あり " + v.update
		subtitle = updatePage
		if ansi.StringWidth(subtitle) > v.width {
			subtitle = strings.TrimPrefix(subtitle, "https://")
		}
	}
	room := v.width - ansi.StringWidth(badge) - 1
	lines := []string{paddedLine(heading, max(0, room)) + " " + badge, ansi.Truncate(subtitle, v.width, "…")}
	content := strings.Split(body, "\n")
	for i := 0; i < v.height-4; i++ {
		line := ""
		if i < len(content) {
			line = content[i]
		}
		lines = append(lines, paddedLine(line, v.width))
	}
	for i := 0; i < 2; i++ {
		line := ""
		if i < len(footer) {
			line = footer[i]
		}
		lines = append(lines, paint(ansi.Truncate(line, v.width, "…"), tuiMuted, v.color))
	}
	return fitScreen(strings.Join(lines, "\n"), v.width, v.height)
}

func (v terminalView) choices(c choiceView) string {
	bodyHeight := v.height - 4
	wide := v.width >= 100 && v.height >= 20
	leftWidth := v.width
	if wide {
		leftWidth = v.width - max(34, v.width/3) - 1
	}
	lines := []string{"検索  " + inputView(c.query, c.pos, leftWidth-10), paint(fmt.Sprintf("%d 件  ·  ↑↓で選択", len(c.rows)), tuiMuted, v.color), ""}
	reserve := 2
	if c.cursor >= 0 && c.cursor < len(c.rows) && len(c.rows[c.cursor].compact) > 0 {
		reserve = len(c.rows[c.cursor].compact)
	}
	if wide {
		reserve = 0
	} else if len(c.sideLines) > 0 {
		reserve = min(6, max(2, bodyHeight-7))
	}
	limit := max(1, (bodyHeight-2-len(lines)-reserve)/2)
	start := max(0, c.cursor-limit+1)
	for i := start; i < len(c.rows) && i < start+limit; i++ {
		row := c.rows[i]
		name := "  " + row.name
		hint := "  " + row.hint
		if i == c.cursor {
			name = "› " + row.name
			if v.color {
				style := lipgloss.NewStyle().Foreground(lipgloss.Color("#EAF7FA")).Background(lipgloss.Color("#244354"))
				name = style.Render(paddedLine(name, leftWidth-4))
				hint = style.Render(paddedLine(hint, leftWidth-4))
			}
		} else {
			hint = paint(hint, tuiMuted, v.color)
		}
		lines = append(lines, name, hint)
	}
	if len(c.rows) == 0 {
		lines = append(lines, "一致する候補はありません。Ctrl+Uで検索を消去")
	}
	detail := []string{"候補を選択してください"}
	if c.cursor >= 0 && c.cursor < len(c.rows) {
		row := c.rows[c.cursor]
		detail = row.details
		if !wide && len(c.sideLines) == 0 {
			compact := row.compact
			if len(compact) == 0 {
				compact = []string{"選択中  " + row.summary, "  " + row.hint}
			}
			for len(lines) < bodyHeight-2-len(compact) {
				lines = append(lines, "")
			}
			for _, line := range compact {
				lines = append(lines, paint(line, tuiAccent, v.color))
			}
		}
	}
	if c.sideLines != nil {
		if wide {
			detail = c.sideLines
		} else {
			count := min(len(c.sideLines), max(1, reserve-1))
			for len(lines) < bodyHeight-2-count-1 {
				lines = append(lines, "")
			}
			lines = append(lines, "── "+c.sideTitle+" ──")
			lines = append(lines, c.sideLines[:count]...)
		}
	}
	body := v.panel(c.title, lines, leftWidth, bodyHeight)
	if wide {
		rightWidth := v.width - leftWidth - 1
		left := strings.Split(body, "\n")
		title := "接続先の詳細"
		if c.sideTitle != "" {
			title = c.sideTitle
		}
		right := strings.Split(v.panel(title, detail, rightWidth, bodyHeight), "\n")
		for i := range left {
			left[i] += " " + right[i]
		}
		body = strings.Join(left, "\n")
	}
	badge := paint("● 選択", tuiAccent, v.color)
	if c.notice != "" {
		badge = paint(c.notice, tuiError, v.color)
	}
	return v.frame("CLOUD SQL", c.tabs, badge, body, c.footer)
}
