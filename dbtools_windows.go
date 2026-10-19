//go:build windows

package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func dbToolRunning(ctx context.Context, t dbToolTarget) (bool, error) {
	h, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(h)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	err = windows.Process32First(h, &entry)
	for err == nil {
		if err = ctx.Err(); err != nil {
			return false, err
		}
		name := strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))
		if t.Tool == "dbeaver" && (name == "dbeaver.exe" || name == "dbeaverc.exe") || t.Tool == "pgadmin" && (strings.Contains(name, "pgadmin") || name == "python.exe" || name == "pythonw.exe") || t.Tool == "a5" && name == "a5m2.exe" {
			return true, nil
		}
		err = windows.Process32Next(h, &entry)
	}
	if err != windows.ERROR_NO_MORE_FILES {
		return false, err
	}
	return false, nil
}
func readA5Settings() ([]byte, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\mmatsubara\A5M2(x64)\DataSources`, registry.READ)
	if err != nil {
		return nil, fmt.Errorf("A5通常版の初期化済み設定が必要です（Portable・Store・workspaceは対象外）: %w", err)
	}
	defer k.Close()
	children, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return nil, err
	}
	if len(children) != 0 {
		return nil, fmt.Errorf("A5の登録保存形式が未対応です。ファイル出力を利用してください")
	}
	names, err := k.ReadValueNames(-1)
	if err != nil {
		return nil, err
	}
	if len(names) > 128 {
		return nil, fmt.Errorf("A5の設定数が上限を超えています")
	}
	s := a5RegistrySnapshot{Values: map[string]a5RegistryValue{}}
	total := 0
	for _, name := range names {
		n, typ, e := k.GetValue(name, nil)
		if e != nil {
			return nil, e
		}
		if n > dbToolJSONLimit {
			return nil, fmt.Errorf("A5設定が上限を超えています")
		}
		total += n
		if total > dbToolJSONLimit {
			return nil, fmt.Errorf("A5設定全体が上限を超えています")
		}
		data := make([]byte, n)
		n, typ, e = k.GetValue(name, data)
		if e != nil {
			return nil, e
		}
		s.Values[name] = a5RegistryValue{typ, data[:n]}
	}
	return json.Marshal(s)
}
func a5RegistryString(v a5RegistryValue) (string, error) {
	if v.Type != registry.SZ || len(v.Data)%2 != 0 {
		return "", fmt.Errorf("A5設定値の型が不正です")
	}
	u := make([]uint16, len(v.Data)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(v.Data[i*2:])
	}
	return windows.UTF16ToString(u), nil
}
func startA5Registration(t dbToolTarget, name, conn string) (bool, error) {
	// CommandContextでGUIを強制終了しない。Waitは別に回収し、登録は保存先を照合する。
	cmd := exec.Command(t.Executable, "/AddDB", name+"="+conn, "/Exit")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: false}
	if err := cmd.Start(); err != nil {
		return false, err
	}
	go func() { _ = cmd.Wait() }()
	return true, nil
}

// 継承ACLへ依存せず、実行ユーザーとSYSTEMだけにアクセスを許す。
func dbToolPrivate(path string, dir bool) error {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	inherit := ""
	if dir {
		inherit = "OICI"
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;" + inherit + ";FA;;;" + user.User.Sid.String() + ")(A;" + inherit + ";FA;;;SY)")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
