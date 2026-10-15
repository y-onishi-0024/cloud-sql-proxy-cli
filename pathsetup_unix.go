//go:build !windows

package main

import "fmt"

type unavailableUserPath struct{}

func newUserPathStore() userPathStore { return unavailableUserPath{} }
func (unavailableUserPath) Read() (userPathValue, error) {
	return userPathValue{}, fmt.Errorf("WindowsユーザーPATHはこのOSで操作できません")
}
func (unavailableUserPath) Save(userPathValue, userPathValue) error {
	return fmt.Errorf("WindowsユーザーPATHはこのOSで操作できません")
}
func (unavailableUserPath) Notify() error { return nil }
