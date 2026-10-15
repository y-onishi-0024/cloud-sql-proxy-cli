package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 保存・出力をCLIから通し、実装内部の関数構成には依存しない。
func TestProfileCLIAndExports(t *testing.T) {
	a, _ := discoveryApp(t, func(_ context.Context, project string) ([]byte, error) { return sqlJSON(project, "db"), nil })
	out, errs := &bytes.Buffer{}, &bytes.Buffer{}
	a.Out, a.Err = out, errs
	add := []string{"profile", "add", "dev", "--project", "alpha", "--instance", "db", "--database", "業務/db?#", "--user", "user@team", "--port", "55431"}
	if got := a.Run(context.Background(), add); got != 0 {
		t.Fatalf("登録失敗 code=%d: %s", got, errs)
	}
	// 再起動後も外部コマンドなしで取り出せる。
	b, _, _ := testApp(t)
	b.Paths = a.Paths
	b.Out, b.Err = out, errs
	out.Reset()
	if code := b.Run(context.Background(), []string{"profile", "export", "dev", "--format", "pgadmin"}); code != 0 {
		t.Fatalf("出力失敗: %s", errs)
	}
	var decoded struct{ Servers map[string]map[string]any }
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("JSONへログが混入: %s", out)
	}
	s := decoded.Servers["1"]
	if s["Host"] != "127.0.0.1" || s["Port"] != float64(55431) || s["MaintenanceDB"] != "業務/db?#" || s["Username"] != "user@team" || s["SSLMode"] != "disable" {
		t.Fatalf("接続情報が変化: %+v", s)
	}
	if strings.Contains(out.String(), "Password") {
		t.Fatal("パスワード項目を出力")
	}
	out.Reset()
	if code := b.Run(context.Background(), []string{"profile", "export", "dev", "--format", "uri"}); code != 0 {
		t.Fatal(errs)
	}
	u, err := url.Parse(strings.TrimSpace(out.String()))
	if err != nil || u.Scheme != "postgresql" || u.User.Username() != "user@team" || u.Host != "127.0.0.1:55431" || u.Path != "/業務/db?#" || u.Query().Get("sslmode") != "disable" {
		t.Fatalf("URIの値が変化: %s %v", out, err)
	}
	if _, has := u.User.Password(); has {
		t.Fatal("URIへパスワードを格納")
	}
	if strings.Contains(u.EscapedPath()[1:], "/") {
		t.Fatal("DB名を複数pathへ分割")
	}
	outputDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(outputDir, "servers.json")
	if code := b.Run(context.Background(), []string{"profile", "export", "dev", "--format", "pgadmin", "--output", filename}); code != 0 {
		t.Fatal(errs)
	}
	before, _ := os.ReadFile(filename)
	if code := b.Run(context.Background(), []string{"profile", "export", "dev", "--format", "pgadmin", "--output", filename}); code == 0 {
		t.Fatal("出力先を上書き")
	}
	after, _ := os.ReadFile(filename)
	if !bytes.Equal(before, after) {
		t.Fatal("既存出力先を破損")
	}
	if code := b.Run(context.Background(), []string{"profile", "remove", "dev"}); code == 0 {
		t.Fatal("非TTYで削除を承認")
	}
	out.Reset()
	if code := b.Run(context.Background(), []string{"profile", "list"}); code != 0 || !strings.Contains(out.String(), "dev") {
		t.Fatalf("保存値を喪失: %s %s", out, errs)
	}
	for _, args := range [][]string{{"--profile", "dev", "db"}, {"connect", "--profile", "dev", "--private-ip"}, {"profile", "add", "missing"}} {
		if code := b.Run(context.Background(), args); code != 2 {
			t.Errorf("引数衝突 %v: %d", args, code)
		}
	}
}

func TestProfilesPersistenceAndPortReuse(t *testing.T) {
	a, _, _ := testApp(t)
	p := Profile{Name: "dev", Instance: testInstance(), Database: "app", Username: "reader", Port: 55435}
	if err := a.AddProfile(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	p.Name = "report"
	p.Database = "reports"
	p.Port = 0
	prepared, err := a.PrepareProfile(p)
	if err != nil || prepared.Port != 55435 {
		t.Fatalf("同一instanceのPortを喪失: %+v %v", prepared, err)
	}
	if err := a.AddProfile(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	other := prepared
	other.Name = "other"
	other.Instance.Name = "other"
	other.ConnectionName = "alpha:asia-northeast1:other"
	if err := a.AddProfile(context.Background(), other); err == nil {
		t.Fatal("異なるinstanceへ同じPortを予約")
	}
	filename := filepath.Join(a.Paths.ConfigDir, "profiles.json")
	valid, _ := os.ReadFile(filename)
	for _, invalid := range []string{`{"Version":99,"Profiles":[]}`, `{"Version":1,"Profiles":[]} {}`, strings.Repeat("x", (1<<20)+1)} {
		if err := os.WriteFile(filename, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if err := a.AddProfile(context.Background(), other); err == nil {
			t.Fatal("不正な設定を空とみなして上書き")
		}
		after, _ := os.ReadFile(filename)
		if string(after) != invalid {
			t.Fatal("不正な既存設定を破壊")
		}
	}
	if err := os.WriteFile(filename, valid, 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dev", "report"} {
		if err := a.RemoveProfile(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.AddProfile(context.Background(), other); err != nil {
		t.Fatalf("最後の削除で予約を解放しない: %v", err)
	}
	before, _ := os.ReadFile(filename)
	for _, bad := range []Profile{{Name: "../bad", Instance: testInstance(), Database: "app", Username: "reader", Port: 55436}, {Name: "bad", Instance: testInstance(), Database: "app\n", Username: "reader", Port: 55436}} {
		if err := a.AddProfile(context.Background(), bad); err == nil {
			t.Fatal("不正な値を保存")
		}
	}
	after, _ := os.ReadFile(filename)
	if !bytes.Equal(before, after) {
		t.Fatal("無効な保存で既存設定を変更")
	}
	// 設定・lock・出力のリンク先は触らない。
	for _, name := range []string{"profiles.json", "profiles.lock"} {
		b, _, _ := testApp(t)
		if err := secureDir(b.Paths.ConfigDir); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(b.Paths.ConfigDir, "target")
		os.WriteFile(target, []byte("keep"), 0600)
		if err := os.Symlink(target, filepath.Join(b.Paths.ConfigDir, name)); err != nil {
			t.Fatal(err)
		}
		if err := b.AddProfile(context.Background(), prepared); err == nil {
			t.Fatal("設定リンクを許可: " + name)
		}
		v, _ := os.ReadFile(target)
		if string(v) != "keep" {
			t.Fatal("リンク先を変更")
		}
	}
}

func TestProfilePortConflicts(t *testing.T) {
	a, _, _ := testApp(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	p := Profile{Name: "dev", Instance: testInstance(), Database: "app", Username: "reader", Port: l.Addr().(*net.TCPAddr).Port}
	if err = a.AddProfile(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if _, err = a.profilePort(p.Instance, 0, nil); err == nil || !strings.Contains(err.Error(), "csql status") {
		t.Fatalf("使用中を別Portへ回避: %v", err)
	}
	if _, err = a.profilePort(p.Instance, p.Port+1, nil); err == nil {
		t.Fatal("保存済みPortと異なる明示Portを許可")
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	if port, err := a.profilePort(p.Instance, 0, nil); err != nil || port != p.Port {
		t.Fatalf("予約を復元できない: %d %v", port, err)
	}
}

func TestProfilesConcurrentProcesses(t *testing.T) {
	if mode := os.Getenv("CSQL_PROFILE_WORKER"); mode != "" {
		a := NewApp()
		a.Paths.ConfigDir = os.Getenv("CSQL_PROFILE_DIR")
		if mode == "lock" {
			f, err := os.OpenFile(filepath.Join(a.Paths.ConfigDir, "profiles.lock"), os.O_CREATE|os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			unlock, err := lockProfiles(context.Background(), f)
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			fmt.Println("locked")
			// 外部の親が終了させるまで待つ。空selectはGoのdeadlock検出で子を終了させる。
			timer := time.NewTimer(time.Hour)
			defer timer.Stop()
			<-timer.C
			return
		}
		p := Profile{Name: mode, Instance: testInstance(), Database: "app", Username: "reader", Port: 55435}
		fmt.Println("waiting")
		if err := a.AddProfile(context.Background(), p); err != nil {
			t.Fatal(err)
		}
		return
	}
	a, _, _ := testApp(t)
	if err := secureDir(a.Paths.ConfigDir); err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	start := func(mode string) *exec.Cmd {
		cmd := exec.Command(exe, "-test.run=^TestProfilesConcurrentProcesses$")
		cmd.Env = append(os.Environ(), "CSQL_PROFILE_WORKER="+mode, "CSQL_PROFILE_DIR="+a.Paths.ConfigDir)
		pipe, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stderr = os.Stderr
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		ready := make(chan bool, 1)
		go func() { scan := bufio.NewScanner(pipe); ready <- scan.Scan() }()
		select {
		case ok := <-ready:
			if !ok {
				t.Fatal("子プロセスの準備失敗")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("子プロセスの準備時間切れ")
		}
		return cmd
	}
	owner := start("lock")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := a.AddProfile(ctx, Profile{Name: "cancelled", Instance: testInstance(), Database: "app", Username: "reader", Port: 55435}); err != context.DeadlineExceeded {
		t.Fatalf("lock待ちを取消できない: %v", err)
	}
	one, two := start("one"), start("two")
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = owner.Wait()
	for _, cmd := range []*exec.Cmd{one, two} {
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("所有者終了後もlockが残留")
		}
	}
	rows, err := a.LoadProfiles()
	if err != nil || len(rows) != 2 {
		t.Fatalf("同時保存を喪失: %+v %v", rows, err)
	}
}
