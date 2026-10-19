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
	globalActions bool
	theme         uiTheme
}
type terminalChoice struct {
	name, hint, summary string
	details             []string
	compact             []string
	columns             []string
	statusColor         string
}
type choiceView struct {
	sideTitle                  string
	sideLines                  []string
	title, tabs, query, notice string
	pos, cursor                int
	rows                       []terminalChoice
	footer                     []string
	table                      bool
}

// 色の指定がないautoでは端末の標準文字色を使い、入力への背景問い合わせを行わない。
type uiTheme struct {
	accent, muted, success, warning, failure string
	selectionForeground, selectionBackground string
}

func parseUITheme(env func(string) string) (uiTheme, error) {
	value, background := "", ""
	if env != nil {
		value = env("CSQL_THEME")
		background = env("COLORFGBG")
	}
	switch value {
	case "", "auto":
		parts := strings.Split(background, ";")
		switch parts[len(parts)-1] {
		case "0", "8":
			value = "dark"
		case "7", "15":
			value = "light"
		default:
			return uiTheme{}, nil
		}
	case "dark", "light":
	default:
		return uiTheme{}, fmt.Errorf("CSQL_THEMEはauto|dark|lightを指定してください（例: CSQL_THEME=auto csql）")
	}
	if value == "light" {
		return uiTheme{accent: "#005A9C", muted: "#465364", success: "#176B32", warning: "#805400", failure: "#B42336", selectionForeground: "#102E46", selectionBackground: "#DCEAF4"}, nil
	}
	return uiTheme{accent: "#78DCE8", muted: "#A3AFBF", success: "#A9DC76", warning: "#FFD866", failure: "#FF6188", selectionForeground: "#EAF7FA", selectionBackground: "#244354"}, nil
}
func (v terminalView) selected(s string) string {
	if !v.color {
		return s
	}
	style := lipgloss.NewStyle()
	if v.theme.selectionForeground == "" {
		style = style.Reverse(true)
	} else {
		style = style.Foreground(lipgloss.Color(v.theme.selectionForeground)).Background(lipgloss.Color(v.theme.selectionBackground))
	}
	return style.Render(s)
}

func paddedLine(s string, width int) string {
	s = ansi.Truncate(s, width, "…")
	return s + strings.Repeat(" ", max(0, width-ansi.StringWidth(s)))
}

func (v terminalView) panel(title string, lines []string, width, height int) string {
	title = ansi.Truncate(title, max(0, width-6), "…")
	top := "╭─ " + title + " " + strings.Repeat("─", max(0, width-ansi.StringWidth(title)-5)) + "╮"
	result := []string{paint(top, v.theme.muted, v.color)}
	for i := 0; i < height-2; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		result = append(result, paint("│ ", v.theme.muted, v.color)+paddedLine(line, width-4)+paint(" │", v.theme.muted, v.color))
	}
	result = append(result, paint("╰"+strings.Repeat("─", max(0, width-2))+"╯", v.theme.muted, v.color))
	return strings.Join(result, "\n")
}

// 操作ガイドの2行を先に確保し、長い本文やログで隠さない。
func (v terminalView) frame(title, subtitle, badge, body string, footer []string) string {
	heading := paint(" csql ", v.theme.accent, v.color) + " " + title
	if v.update != "" {
		badge = paint("更新 "+v.update, v.theme.warning, v.color) + " · " + badge
	}
	room := max(0, v.width-ansi.StringWidth(badge)-1)
	lines := []string{paddedLine(heading, room) + " " + badge, ansi.Truncate(subtitle, v.width, "…")}
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
		if i == 0 {
			if v.globalActions {
				line = "F2 csqlを更新  F3 データ移行  " + line
			}
			line = "F1 ヘルプ  " + line
		} else {
			ending := "Ctrl+C 終了"
			if strings.Contains(line, "全接続") {
				ending = "Ctrl+C 全接続停止"
			}
			if at := strings.Index(line, "Ctrl+C"); at >= 0 {
				line = strings.TrimSpace(line[:at])
			}
			line = paddedLine(line, max(0, v.width-ansi.StringWidth(ending)-2)) + "  " + ending
		}
		lines = append(lines, paint(ansi.Truncate(line, v.width, "…"), v.theme.muted, v.color))
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
	lines := []string{"検索  " + inputView(c.query, c.pos, leftWidth-10), paint(fmt.Sprintf("%d 件  ·  ↑↓で選択", len(c.rows)), v.theme.muted, v.color), ""}
	reserve := 2
	if c.cursor >= 0 && c.cursor < len(c.rows) && len(c.rows[c.cursor].compact) > 0 {
		reserve = len(c.rows[c.cursor].compact)
	}
	if wide {
		reserve = 0
	} else if len(c.sideLines) > 0 {
		reserve = min(6, max(2, bodyHeight-7))
	}
	rowHeight := 2
	var widths []int
	if wide && c.table {
		rowHeight = 1
		remaining := leftWidth - 6 - 18
		nameWidth, projectWidth := remaining*4/10, remaining*35/100
		widths = []int{8, nameWidth, projectWidth, 6, remaining - nameWidth - projectWidth}
		lines[len(lines)-1] = "  " + tableLine([]string{"状態", "設定・接続先", "プロジェクト", "Port", "DB"}, widths)
	}
	limit := max(1, (bodyHeight-2-len(lines)-reserve)/rowHeight)
	start := max(0, c.cursor-limit+1)
	for i := start; i < len(c.rows) && i < start+limit; i++ {
		row := c.rows[i]
		name := "  " + row.name
		hint := "  " + row.hint
		if wide && c.table {
			name = "  " + tableLine(row.columns, widths)
			if i == c.cursor {
				name = "› " + tableLine(row.columns, widths)
				name = v.selected(paddedLine(name, leftWidth-4))
			} else if row.statusColor != "" {
				name = paint(name, row.statusColor, v.color)
			}
			lines = append(lines, name)
			continue
		}
		if i == c.cursor {
			name = v.selected(paddedLine("› "+row.name, leftWidth-4))
			hint = v.selected(paddedLine(hint, leftWidth-4))
		} else {
			hint = paint(hint, v.theme.muted, v.color)
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
				lines = append(lines, paint(line, v.theme.accent, v.color))
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
	badge := paint("● 選択", v.theme.accent, v.color)
	if c.notice != "" {
		badge = paint(c.notice, v.theme.failure, v.color)
	}
	return v.frame(c.title, c.tabs, badge, body, c.footer)
}

// 見出しと値に同じセル幅を使い、日本語で列がずれないようにする。
func tableLine(values []string, widths []int) string {
	var cells []string
	for i, width := range widths {
		value := ""
		if i < len(values) {
			value = cleanText(values[i])
		}
		cells = append(cells, paddedLine(value, width))
	}
	return strings.Join(cells, " ")
}
