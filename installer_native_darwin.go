//go:build installer && darwin && cgo

package main

/*
#cgo LDFLAGS: -framework AppKit
#include <stdlib.h>
int csqlNativeRun(int shell);
void csqlNativeUpdate(const char *log, const char *status, const char *action, int enabled, int active, int busy);
*/
import "C"

import (
	"os"
	"path/filepath"
	"strings"
	"unsafe"
)

var macInstaller *installerController
var macDisplayed installerSnapshot
var macDisplayedRevision = ^uint64(0)

func init() {
	installerNativeMain = func() int {
		macInstallerEnvironment()
		macInstaller = newInstallerController(nil, nil)
		index := 0
		switch filepath.Base(os.Getenv("SHELL")) {
		case "bash":
			index = 1
		case "fish":
			index = 2
		}
		return int(C.csqlNativeRun(C.int(index)))
	}
}

// Finderの最小PATHだけを補完し、利用者のシェル設定を実行しない。
func macInstallerEnvironment() {
	current := os.Getenv("PATH")
	standard := true
	for _, p := range filepath.SplitList(current) {
		switch p {
		case "", "/usr/bin", "/bin", "/usr/sbin", "/sbin", "/Library/Apple/usr/bin":
		default:
			standard = false
		}
	}
	var extra []string
	for _, p := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/homebrew/share/google-cloud-sdk/bin", "/usr/local/share/google-cloud-sdk/bin"} {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			found := false
			for _, v := range filepath.SplitList(current) {
				if v == p {
					found = true
				}
			}
			if !found {
				extra = append(extra, p)
			}
		}
	}
	if len(extra) == 0 {
		return
	}
	parts := []string{current}
	if standard {
		parts = append(extra, current)
	} else {
		parts = append(parts, extra...)
	}
	os.Setenv("PATH", strings.Join(parts, string(os.PathListSeparator)))
}

//export goInstallerAction
func goInstallerAction(shell C.int) {
	if macDisplayed.State == installerAwaiting {
		macInstaller.Answer(macDisplayed.PromptID, true)
		return
	}
	names := []string{"zsh", "bash", "fish"}
	i := int(shell)
	if i < 0 || i >= len(names) {
		return
	}
	macInstaller.Start(names[i])
}

//export goInstallerCanClose
func goInstallerCanClose() C.int {
	if macInstaller.Snapshot().Active() {
		macInstaller.Cancel()
		return 0
	}
	return 1
}

//export goInstallerTick
func goInstallerTick() {
	s := macInstaller.Snapshot()
	if s.Revision == macDisplayedRevision {
		return
	}
	macDisplayedRevision = s.Revision
	macDisplayed = s
	status, action, enabled, busy := installerPresentation(s)
	log := C.CString(s.Log)
	text := C.CString(status)
	button := C.CString(action)
	defer C.free(unsafe.Pointer(log))
	defer C.free(unsafe.Pointer(text))
	defer C.free(unsafe.Pointer(button))
	flag := func(b bool) C.int {
		if b {
			return 1
		}
		return 0
	}
	C.csqlNativeUpdate(log, text, button, flag(enabled), flag(s.Active()), flag(busy))
}
