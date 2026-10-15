package main

import (
	"fmt"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"unsafe"
)

type registryUserPath struct{}

func newUserPathStore() userPathStore { return registryUserPath{} }
func (registryUserPath) Read() (userPathValue, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE)
	if err == registry.ErrNotExist {
		return userPathValue{}, nil
	}
	if err != nil {
		return userPathValue{}, err
	}
	defer k.Close()
	s, kind, err := k.GetStringValue("Path")
	if err == registry.ErrNotExist {
		return userPathValue{}, nil
	}
	if err != nil {
		return userPathValue{}, err
	}
	if kind != registry.SZ && kind != registry.EXPAND_SZ {
		return userPathValue{}, fmt.Errorf("ユーザーPathの種類が文字列ではありません")
	}
	return userPathValue{Value: s, Kind: kind, Exists: true}, nil
}
func (s registryUserPath) Save(expected, next userPathValue) error {
	actual, err := s.Read()
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("確認中にユーザーPathが変更されました")
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, `Environment`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if next.Kind == registry.EXPAND_SZ {
		return k.SetExpandStringValue("Path", next.Value)
	}
	return k.SetStringValue("Path", next.Value)
}
func (registryUserPath) Notify() error {
	// 既存端末のプロセス環境は書き換えず、新規プロセスへの更新を通知する。
	proc := windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")
	text, err := windows.UTF16PtrFromString("Environment")
	if err != nil {
		return err
	}
	var result uintptr
	ok, _, e := proc.Call(0xffff, 0x001a, 0, uintptr(unsafe.Pointer(text)), 0x0002, 5000, uintptr(unsafe.Pointer(&result)))
	if ok == 0 {
		return fmt.Errorf("環境変更通知: %w", e)
	}
	return nil
}
