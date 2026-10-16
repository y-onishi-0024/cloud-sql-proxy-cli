package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
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

// CLI更新で同じインスタンスの設定をまとめて変更し、再読込・全出力へ反映する。
func TestProfilePortUpdateCLI(t *testing.T) {
	a, out, errs := testApp(t)
	p := Profile{Name: "dev", Instance: testInstance(), Database: "app", Username: "reader", Port: 5432}
	for _, name := range []string{"dev", "report"} {
		p.Name = name
		p.Database = name
		if err := a.AddProfile(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	p.Name = "other"
	p.Database = "other"
	p.Instance.Name = "other"
	p.ConnectionName = "alpha:asia-northeast1:other"
	p.Port = 65432
	if err := a.AddProfile(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	if code := a.Run(context.Background(), []string{"profile", "update", "dev", "--port", fmt.Sprint(port)}); code != 0 {
		t.Fatalf("更新できない: %d %s", code, errs)
	}
	b, _, _ := testApp(t)
	b.Paths = a.Paths
	rows, err := b.LoadProfiles()
	if err != nil || len(rows) != 3 {
		t.Fatalf("再読込: %+v %v", rows, err)
	}
	for _, row := range rows {
		want := port
		if row.Name == "other" {
			want = 65432
		}
		if row.Port != want || row.Database != row.Name || row.Username != "reader" {
			t.Fatalf("設定を破損: %+v", row)
		}
	}
	for _, format := range []string{"pgadmin", "dbeaver", "a5", "uri"} {
		out.Reset()
		if code := a.Run(context.Background(), []string{"profile", "export", "report", "--format", format}); code != 0 || !strings.Contains(out.String(), fmt.Sprint(port)) {
			t.Fatalf("%sが旧ポート: %s %s", format, out, errs)
		}
	}
	file := filepath.Join(a.Paths.ConfigDir, "profiles.json")
	before, _ := os.ReadFile(file)
	for _, args := range [][]string{{"profile", "update", "missing", "--port", fmt.Sprint(port)}, {"profile", "update", "dev", "--port", "0"}, {"profile", "update", "dev", "--port", "65536"}, {"profile", "update", "dev"}, {"profile", "update", "dev", "--port", "65432"}} {
		if code := a.Run(context.Background(), args); code == 0 {
			t.Fatalf("不正更新を許可: %v", args)
		}
		after, _ := os.ReadFile(file)
		if !bytes.Equal(before, after) {
			t.Fatal("失敗時に設定を変更")
		}
	}
}

// 拒否・同時編集・取消で既存設定を書き換えず、同じ値は無変更で成功する。
func TestProfilePortUpdateGuards(t *testing.T) {
	a, _, _ := testApp(t)
	p := Profile{Name: "dev", Instance: testInstance(), Database: "app", Username: "reader", Port: 5432}
	if err := a.AddProfile(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(a.Paths.ConfigDir, "profiles.json")
	before, _ := os.ReadFile(file)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	port := busy.Addr().(*net.TCPAddr).Port
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		ctx       context.Context
		old, port int
	}{{context.Background(), 5432, port}, {context.Background(), 5433, 15432}, {cancelled, 5432, 15432}} {
		if _, err := a.UpdateProfilePort(tc.ctx, "dev", tc.old, tc.port); err == nil {
			t.Fatal("拒否すべき更新が成功")
		}
		after, _ := os.ReadFile(file)
		if !bytes.Equal(before, after) {
			t.Fatal("拒否時に設定を変更")
		}
	}
	info, _ := os.Stat(file)
	if _, err := a.UpdateProfilePort(context.Background(), "dev", 5432, 5432); err != nil {
		t.Fatal(err)
	}
	afterInfo, _ := os.Stat(file)
	if !os.SameFile(info, afterInfo) {
		t.Fatal("同じ値で設定を再配置")
	}
	r := sessionRecord{SessionStatus: SessionStatus{ID: strings.Repeat("a", 32), Project: p.Project, Name: p.Instance.Name, Region: p.Region, DatabaseVersion: p.DatabaseVersion, State: "running", Port: p.Port}, ControlPort: 1, Token: strings.Repeat("b", 64), ProtocolVersion: sessionProtocolVersion}
	if err := writeJSON(filepath.Join(a.Paths.ConfigDir, "sessions", r.ID+".json"), r); err != nil {
		t.Fatal(err)
	}
	if _, err := a.UpdateProfilePort(context.Background(), "dev", 5432, 15432); err == nil || !strings.Contains(err.Error(), "csql stop") {
		t.Fatalf("応答不明を無視: %v", err)
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("応答不明で設定を変更")
	}
}

// 標準ポートを避け、使用中・別インスタンスの予約済みポートも飛ばして保存候補を選ぶ。
func TestProfileAutomaticPorts(t *testing.T) {
	for _, tc := range []struct {
		version string
		base    int
	}{
		{"POSTGRES_18", 15432},
		{"MYSQL_8_0", 13306},
		{"SQLSERVER_2019_STANDARD", 11433},
	} {
		t.Run(tc.version, func(t *testing.T) {
			a, _, _ := testApp(t)
			busy, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", tc.base))
			if err != nil {
				t.Skipf("検証用ポート%dを確保できません: %v", tc.base, err)
			}
			defer busy.Close()
			reserved := Profile{Name: "other", Instance: testInstance(), Database: "app", Username: "reader", Port: tc.base + 1}
			reserved.Instance.Name = "other"
			reserved.ConnectionName = "alpha:asia-northeast1:other"
			if err := a.AddProfile(context.Background(), reserved); err != nil {
				t.Fatal(err)
			}
			p := Profile{Name: "dev", Instance: testInstance(), Database: "app", Username: "reader"}
			p.DatabaseVersion = tc.version
			prepared, err := a.PrepareProfile(p)
			if err != nil || prepared.Port < tc.base+2 || prepared.Port > tc.base+20 {
				t.Fatalf("標準ポートを避けた空き・未予約の範囲%d〜%dから選択できない: %+v %v", tc.base+2, tc.base+20, prepared, err)
			}
		})
	}
}

func TestProfilesPersistenceAndPortReuse(t *testing.T) {
	a, _, _ := testApp(t)
	// 旧デフォルトで保存した設定も、新しい自動割り当てへ変更しない。
	p := Profile{Name: "dev", Instance: testInstance(), Database: "app", Username: "reader", Port: 5432}
	if err := a.AddProfile(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	p.Name = "report"
	p.Database = "reports"
	p.Port = 0
	prepared, err := a.PrepareProfile(p)
	if err != nil || prepared.Port != 5432 {
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
		if mode == "connect" {
			a = proxyApp(t)
			a.Paths.ConfigDir = os.Getenv("CSQL_PROFILE_DIR")
			fmt.Println("waiting")
			if err := a.Connect(context.Background(), testInstance(), ConnectOptions{Progress: &connectionProgress{}}); err != nil {
				t.Fatal(err)
			}
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
	// 別プロセスの起動も編集ロックを待ち、ロック内で確定した新しい設定を読む。
	owner = start("lock")
	connecting := start("connect")
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		records, err := a.records()
		if err != nil || len(records) != 0 {
			t.Fatalf("ロック中に子を起動: %+v %v", records, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	for i := range rows {
		rows[i].Port = 55436
	}
	if err := a.writeProfiles(rows); err != nil {
		t.Fatal(err)
	}
	_ = owner.Process.Kill()
	_ = owner.Wait()
	session := awaitSession(t, a)
	if session.Port != 55436 {
		t.Fatalf("別プロセスが旧設定で起動: %+v", session)
	}
	if _, err := a.UpdateProfilePort(context.Background(), "one", 55436, 55437); err == nil {
		t.Fatal("別プロセス稼働中の編集を許可")
	}
	if err := a.Stop(context.Background(), session.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := connecting.Wait(); err != nil {
		t.Fatal(err)
	}

}

// DBツールへ渡すファイルが、実ポート・日本語・引用符を壊さずパスワードを含めないことをCLIから確認する。
func TestProfileToolExports(t *testing.T) {
	for _, db := range []struct{ version, provider string }{{"POSTGRES_18", "PostgreSQL"}, {"MYSQL_8_0", "MySQL"}, {"SQLSERVER_2019_STANDARD", "SQL Server"}} {
		t.Run(db.provider, func(t *testing.T) {
			a, out, errs := testApp(t)
			p := Profile{Name: "dev", Instance: testInstance(), Database: "業務/db", Username: "reader@team", Port: 55431}
			p.DatabaseVersion = db.version
			if err := a.AddProfile(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			if code := a.Run(context.Background(), []string{"profile", "export", "dev", "--format", "dbeaver"}); code != 0 {
				t.Fatalf("DBeaver出力: %s", errs)
			}
			rows, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
			if err != nil || len(rows) != 2 {
				t.Fatalf("CSVとして解釈できない: %s %v", out, err)
			}
			expected := []string{"csql / alpha / dev", "127.0.0.1", "55431", "業務/db", "reader@team"}
			if strings.Join(rows[0], ",") != "name,host,port,database,user" || strings.Join(rows[1], "|") != strings.Join(expected, "|") {
				t.Fatalf("CSV項目が変化: %+v", rows)
			}
			out.Reset()
			if code := a.Run(context.Background(), []string{"profile", "export", "dev", "--format", "a5"}); code != 0 {
				t.Fatalf("A5出力: %s", errs)
			}
			text := out.String()
			if !strings.HasPrefix(text, "\ufeffcsql/alpha/dev=__ConnectionType=Internal;") || !strings.Contains(text, "ProviderName="+db.provider+";") || !strings.Contains(text, "ServerName=127.0.0.1;Port=55431;Database=業務/db;UserName=reader@team;") || !strings.HasSuffix(text, "\r\n") || strings.Contains(text, "Password=") {
				t.Fatalf("A5接続一覧が不正: %q", text)
			}
			// CLIのファイル出力でも同じバイトを保持し、既存ファイルを上書きしない。
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "servers.a5dblist")
			out.Reset()
			if code := a.Run(context.Background(), []string{"profile", "export", "dev", "--format", "a5", "--output", file}); code != 0 {
				t.Fatal(errs)
			}
			data, err := os.ReadFile(file)
			if err != nil || string(data) != text {
				t.Fatalf("ファイルと標準出力が不一致: %v", err)
			}
			if code := a.Run(context.Background(), []string{"profile", "export", "dev", "--format", "a5", "--output", file}); code == 0 {
				t.Fatal("A5の既存出力を上書き")
			}
			// CSVのカンマ・引用符はレコードの区切りや列になってはならない。
			p.Name, p.Database, p.Username = "quoted", "業務,\"db", "user,\"team"
			if err := a.AddProfile(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			out.Reset()
			if code := a.Run(context.Background(), []string{"profile", "export", "quoted", "--format", "dbeaver"}); code != 0 {
				t.Fatal(errs)
			}
			rows, err = csv.NewReader(strings.NewReader(out.String())).ReadAll()
			if err != nil || rows[1][3] != p.Database || rows[1][4] != p.Username {
				t.Fatal("CSVの特殊文字を破損: ", out)
			}
		})
	}
}

// 未解釈の区切り文字がA5の接続プロパティを増やしたり、別の接続へ書き換えたりしない。
func TestProfileA5RejectsAmbiguousValues(t *testing.T) {
	a, out, errs := testApp(t)
	for i, value := range []string{"db;ServerName=other", "user\"quoted"} {
		p := Profile{Name: fmt.Sprintf("bad%d", i), Instance: testInstance(), Database: value, Username: "reader", Port: 55431}
		if err := a.AddProfile(context.Background(), p); err != nil {
			t.Fatal(err)
		}
		out.Reset()
		errs.Reset()
		if code := a.Run(context.Background(), []string{"profile", "export", p.Name, "--format", "a5"}); code == 0 || out.Len() != 0 || !strings.Contains(errs.String(), "A5") {
			t.Fatal("曖昧な接続値をA5へ出力: ", out, errs)
		}
	}
}
