//go:build installer && windows

package main

import (
	"fmt"
	"golang.org/x/sys/windows"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"
)

var setupUser32 = windows.NewLazySystemDLL("user32.dll")
var setupGdi32 = windows.NewLazySystemDLL("gdi32.dll")
var setupKernel32 = windows.NewLazySystemDLL("kernel32.dll")

func init() {
	installerPrepareCommand = func(cmd *exec.Cmd) {
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	}
	installerNativeMain = windowsInstallerMain
}

type setupPoint struct{ X, Y int32 }
type setupRect struct{ Left, Top, Right, Bottom int32 }
type setupMessage struct {
	Window         uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	Point          setupPoint
	Private        uint32
}
type setupWindowClass struct {
	Size, Style                        uint32
	Procedure                          uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	MenuName, ClassName                *uint16
	SmallIcon                          uintptr
}
type setupMinMax struct{ Reserved, MaxSize, MaxPosition, MinTrack, MaxTrack setupPoint }

func setupUTF16(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(strings.ReplaceAll(s, "\x00", "�"))
	return p
}
func setupBool(v bool) uintptr {
	if v {
		return 1
	}
	return 0
}

func windowsInstallerMain() int {
	c := newInstallerController(nil, nil)
	get := func(name string) *windows.LazyProc { return setupUser32.NewProc(name) }
	create := get("CreateWindowExW")
	send := get("SendMessageW")
	setText := get("SetWindowTextW")
	enable := get("EnableWindow")
	move := get("MoveWindow")
	def := get("DefWindowProcW")
	destroy := get("DestroyWindow")
	postQuit := get("PostQuitMessage")
	killTimer := get("KillTimer")
	messageBox := get("MessageBoxW")
	fail := func(err error) int {
		messageBox.Call(0, uintptr(unsafe.Pointer(setupUTF16("セットアップ画面を開けませんでした。\n"+err.Error()))), uintptr(unsafe.Pointer(setupUTF16("csql セットアップ"))), 0x10)
		return 1
	}
	instance, _, err := setupKernel32.NewProc("GetModuleHandleW").Call(0)
	if instance == 0 {
		return fail(err)
	}
	cursor, _, _ := get("LoadCursorW").Call(0, 32512)
	var window, logView, statusView, primary, secondary, progress uintptr
	var displayed installerSnapshot
	revision := ^uint64(0)
	layout := func(width, height int) {
		if logView == 0 {
			return
		}
		if width < 650 {
			width = 650
		}
		if height < 400 {
			height = 400
		}
		move.Call(logView, 20, 110, uintptr(width-40), uintptr(height-215), 1)
		move.Call(statusView, 20, uintptr(height-94), uintptr(width-40), 42, 1)
		move.Call(progress, 20, uintptr(height-39), 110, 18, 1)
		move.Call(secondary, uintptr(width-270), uintptr(height-47), 110, 32, 1)
		move.Call(primary, uintptr(width-150), uintptr(height-47), 130, 32, 1)
	}
	refresh := func() {
		s := c.Snapshot()
		if s.Revision == revision {
			return
		}
		revision = s.Revision
		displayed = s
		status, action, ready, busy := installerPresentation(s)
		text := strings.ReplaceAll(strings.ReplaceAll(s.Log, "\r\n", "\n"), "\n", "\r\n")
		setText.Call(logView, uintptr(unsafe.Pointer(setupUTF16(text))))
		// 更新時だけ末尾へ移動し、確認待ちでは利用者のスクロールを維持する。
		send.Call(logView, 0x00b1, ^uintptr(0), ^uintptr(0))
		send.Call(logView, 0x00b7, 0, 0)
		setText.Call(statusView, uintptr(unsafe.Pointer(setupUTF16(status))))
		setText.Call(primary, uintptr(unsafe.Pointer(setupUTF16(action))))
		enable.Call(primary, setupBool(ready))
		closeLabel := "閉じる"
		if s.Active() {
			closeLabel = "取り消す"
		}
		setText.Call(secondary, uintptr(unsafe.Pointer(setupUTF16(closeLabel))))
		send.Call(progress, 0x040a, setupBool(busy), 30)
	}
	procedure := windows.NewCallback(func(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
		switch msg {
		case 0x0111: // WM_COMMAND: 画面に提示した確認IDだけへ回答する。
			if uint16(wParam>>16) != 0 {
				return 0
			}
			switch uint16(wParam) {
			case 1:
				if displayed.State == installerAwaiting {
					c.Answer(displayed.PromptID, true)
				} else {
					c.Start("")
				}
			case 2:
				send.Call(hwnd, 0x0010, 0, 0)
			}
			return 0
		case 0x0113:
			if logView != 0 {
				refresh()
			}
			return 0
		case 0x0005:
			layout(int(uint16(lParam)), int(uint16(lParam>>16)))
			return 0
		case 0x0024:
			m := (*setupMinMax)(unsafe.Pointer(lParam))
			m.MinTrack = setupPoint{700, 480}
			return 0
		case 0x0010: // 取消後もworkerの終了と一時ファイル削除まで画面を保持する。
			if c.Snapshot().Active() {
				c.Cancel()
				return 0
			}
			destroy.Call(hwnd)
			return 0
		case 0x0002:
			killTimer.Call(hwnd, 1)
			postQuit.Call(0)
			return 0
		}
		result, _, _ := def.Call(hwnd, uintptr(msg), wParam, lParam)
		return result
	})
	name := setupUTF16("CSQLSetupWindow")
	class := setupWindowClass{Size: uint32(unsafe.Sizeof(setupWindowClass{})), Procedure: procedure, Instance: instance, Cursor: cursor, Background: 6, ClassName: name}
	registered, _, err := get("RegisterClassExW").Call(uintptr(unsafe.Pointer(&class)))
	if registered == 0 {
		return fail(err)
	}
	defer get("UnregisterClassW").Call(uintptr(unsafe.Pointer(name)), instance)
	window, _, err = create.Call(0, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(setupUTF16("csql セットアップ"))), 0x00cf0000, 0x80000000, 0x80000000, 850, 650, 0, 0, instance, 0)
	if window == 0 {
		return fail(err)
	}
	font, _, _ := setupGdi32.NewProc("CreateFontW").Call(^uintptr(15), 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 0, 0, uintptr(unsafe.Pointer(setupUTF16("Segoe UI"))))
	if font != 0 {
		defer setupGdi32.NewProc("DeleteObject").Call(font)
	}
	child := func(class, text string, style uintptr, x, y, width, height int, id uintptr) (uintptr, error) {
		h, _, e := create.Call(0, uintptr(unsafe.Pointer(setupUTF16(class))), uintptr(unsafe.Pointer(setupUTF16(text))), 0x50000000|style, uintptr(x), uintptr(y), uintptr(width), uintptr(height), window, id, instance, 0)
		if h == 0 {
			return 0, e
		}
		if font != 0 {
			send.Call(h, 0x0030, font, 1)
		}
		return h, nil
	}
	if _, err = child("STATIC", "Cloud SQL接続CLIと不足ツールをユーザー領域へ導入します。\r\n導入とPATH設定の前に、それぞれ変更内容を確認します。\r\nGoogleへのログインは、セットアップ完了後に別途行います。", 0, 20, 20, 780, 78, 10); err != nil {
		return fail(err)
	}
	logView, err = child("EDIT", "", 0x00200000|0x00100000|0x00010000|0x00800000|0x0004|0x0040|0x0080|0x0800, 20, 110, 780, 370, 11)
	if err != nil {
		return fail(err)
	}
	send.Call(logView, 0x00c5, installerLogLimit+1024, 0)
	statusView, err = child("STATIC", "", 0, 20, 500, 780, 42, 12)
	if err != nil {
		return fail(err)
	}
	secondary, err = child("BUTTON", "閉じる", 0x00010000, 550, 550, 110, 32, 2)
	if err != nil {
		return fail(err)
	}
	primary, err = child("BUTTON", "セットアップ開始", 0x00010001, 670, 550, 130, 32, 1)
	if err != nil {
		return fail(err)
	}
	common := windows.NewLazySystemDLL("comctl32.dll").NewProc("InitCommonControlsEx")
	initControls := struct{ Size, Classes uint32 }{8, 0x20}
	common.Call(uintptr(unsafe.Pointer(&initControls)))
	progress, _ = child("msctls_progress32", "", 0x08, 20, 555, 110, 18, 13)
	var rect setupRect
	get("GetClientRect").Call(window, uintptr(unsafe.Pointer(&rect)))
	layout(int(rect.Right), int(rect.Bottom))
	refresh()
	get("SetTimer").Call(window, 1, 150, 0)
	get("ShowWindow").Call(window, 5)
	get("UpdateWindow").Call(window)
	var msg setupMessage
	for {
		result, _, e := get("GetMessageW").Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(result) == -1 {
			return fail(fmt.Errorf("画面イベント待受: %w", e))
		}
		if result == 0 {
			break
		}
		handled, _, _ := get("IsDialogMessageW").Call(window, uintptr(unsafe.Pointer(&msg)))
		if handled != 0 {
			continue
		}
		get("TranslateMessage").Call(uintptr(unsafe.Pointer(&msg)))
		get("DispatchMessageW").Call(uintptr(unsafe.Pointer(&msg)))
	}
	return 0
}
