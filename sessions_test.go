package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("CSQL_TEST_CLIPBOARD") != "" {
		os.Exit(runClipboardFixture())
	}

	if os.Getenv("CSQL_TEST_UI_DIR") != "" {
		if len(os.Args) > 1 && os.Args[1] == "--address" {
			os.Setenv("CSQL_TEST_PROXY", "1")
		} else {
			os.Exit(runUIFixture())
		}
	}
	if os.Getenv("CSQL_TEST_SELECT") == "1" {
		a := NewApp()
		rows := []Instance{{Name: "db", Project: "alpha"}, {Name: "db-prod", Project: "beta"}}
		ctx, stop := signal.NotifyContext(context.Background(), terminationSignals()...)
		defer stop()
		v, err := a.SelectInstance(ctx, rows)
		if err != nil {
			fmt.Fprintln(a.Out, err)
			os.Exit(130)
		}
		fmt.Fprintln(a.Out, "選択:", v.Name)
		os.Exit(0)
	}
	if dir := os.Getenv("CSQL_TEST_CLIENT_DIR"); dir != "" {
		a := NewApp()
		a.Paths.ConfigDir = dir
		os.Exit(a.Run(context.Background(), os.Args[1:]))
	}
	if os.Getenv("CSQL_TEST_PROXY") == "1" {
		if p := os.Getenv("CSQL_TEST_ARGSFILE"); p != "" {
			b, _ := json.Marshal(os.Args[1:])
			os.WriteFile(p, b, 0600)
		}
		if os.Getenv("CSQL_TEST_EXIT") == "1" {
			os.Exit(7)
		}
		mode := os.Getenv("CSQL_TEST_LOG_MODE")
		if path := os.Getenv("CSQL_TEST_UI_AUTH_FLAG"); path != "" {
			if _, err := os.Stat(path); err == nil {
				mode = "auth-fail"
			}
		}
		if gate := os.Getenv("CSQL_TEST_BIND_GATE"); gate != "" {
			for {
				if _, err := os.Stat(gate); err == nil {
					break
				}
				time.Sleep(2 * time.Millisecond)
			}
		}
		if os.Getenv("CSQL_TEST_UI_DIR") != "" || os.Getenv("CSQL_TEST_BIND") == "1" {
			for i, arg := range os.Args {
				if arg == "--port" && i+1 < len(os.Args) {
					l, err := net.Listen("tcp", "127.0.0.1:"+os.Args[i+1])
					if err != nil {
						fmt.Println(err)
						os.Exit(7)
					}
					defer l.Close()
					go func() {
						for {
							c, err := l.Accept()
							if err != nil {
								return
							}
							c.Close()
						}
					}()
					break
				}
			}
		}
		fmt.Println(`{"severity":"INFO","message":"The proxy has started successfully and is ready for new connections!"}`)
		if mode == "auth-fail" {
			fmt.Println(`{"severity":"ERROR","message":"auth: invalid_grant reauth related error (invalid_rapt)"}`)
			os.Exit(7)
		}
		if mode != "ready-only" {
			fmt.Print(`{"severity":"INFO","message":"Connection test `)
			fmt.Println(`passed"}`)
		}
		if mode == "lateauth" {
			fmt.Println(`{"severity":"ERROR","message":"failed to connect: invalid_grant invalid_rapt"}`)
		}
		// PTYで一覧を残したログのページ送りを確認するためのfixture出力。
		if mode == "workspace-long" {
			for i := 0; i < 80; i++ {
				fmt.Printf("{\"severity\":\"INFO\",\"message\":\"fixture-log-%03d 日本語の接続ログを表示します\"}\n", i)
			}
		}
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, terminationSignals()...)
		for {
			<-ch
			if os.Getenv("CSQL_TEST_IGNORE") == "1" {
				continue
			}
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

func TestSessionRejectsForgedAndReplayedResponses(t *testing.T) {
	for _, mode := range []string{"unsigned", "reflect-request", "wrong-nonce", "wrong-port", "replay"} {
		t.Run(mode, func(t *testing.T) {
			a, _, _ := testApp(t)
			r := sessionRecord{SessionStatus: SessionStatus{ID: strings.Repeat("a", 32), Name: "db", Project: "alpha", Region: "asia-northeast1", DatabaseVersion: "POSTGRES_16", State: "running", Port: 5432}, Token: strings.Repeat("b", 64), ProtocolVersion: 2}
			headers := make(chan http.Header, 2)
			var mu sync.Mutex
			var previousBody []byte
			previousProof := ""
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				headers <- req.Header.Clone()
				status := r.SessionStatus
				status.State = "stopped"
				if mode == "wrong-port" {
					status.Port = 0
				}
				body, _ := json.Marshal(status)
				nonce := req.Header.Get("X-CSQL-Nonce")
				if mode == "wrong-nonce" {
					nonce = strings.Repeat("0", 64)
				}
				key, _ := hex.DecodeString(r.Token)
				hash := sha256.Sum256(body)
				mac := hmac.New(sha256.New, key)
				fmt.Fprintf(mac, "csql-response-v2\n%s\n200\n%x", nonce, hash)
				proof := hex.EncodeToString(mac.Sum(nil))
				switch mode {
				case "unsigned":
					proof = ""
				case "reflect-request":
					proof = req.Header.Get("X-CSQL-Auth")
				case "replay":
					mu.Lock()
					if previousBody == nil {
						previousBody = body
						previousProof = proof
					} else {
						body = previousBody
						proof = previousProof
					}
					mu.Unlock()
				}
				w.Header().Set("X-CSQL-Proof", proof)
				w.Write(body)
			}))
			defer srv.Close()
			r.ControlPort = srv.Listener.Addr().(*net.TCPAddr).Port
			if mode == "replay" {
				if _, err := a.querySession(context.Background(), r, true); err != nil {
					t.Fatalf("正しい最初の応答: %v", err)
				}
			}
			if _, err := a.querySession(context.Background(), r, true); err == nil {
				t.Fatal("偽装・反射・再利用・接続情報不一致を受理しました")
			}
			checks := 1
			if mode == "replay" {
				checks = 2
			}
			for i := 0; i < checks; i++ {
				h := <-headers
				if h.Get("Authorization") != "" || strings.Contains(fmt.Sprint(h), r.Token) {
					t.Fatal("未確認の制御先へ秘密を送信しました")
				}
			}
		})
	}
}

func TestSessionLegacyRecordDoesNotCommunicate(t *testing.T) {
	a, _, _ := testApp(t)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		json.NewEncoder(w).Encode(SessionStatus{ID: strings.Repeat("a", 32), State: "stopped"})
	}))
	defer srv.Close()
	r := sessionRecord{SessionStatus: SessionStatus{ID: strings.Repeat("a", 32), Port: 5432}, Token: strings.Repeat("b", 64), ControlPort: srv.Listener.Addr().(*net.TCPAddr).Port}
	if err := writeJSON(filepath.Join(a.Paths.ConfigDir, "sessions", r.ID+".json"), r); err != nil {
		t.Fatal(err)
	}
	rows, err := a.Status(context.Background())
	if err != nil || len(rows) != 1 || rows[0].State != "unresponsive" {
		t.Fatalf("旧版の状態: %+v %v", rows, err)
	}
	if err := a.Stop(context.Background(), r.ID, false); err == nil || !strings.Contains(err.Error(), "Ctrl+C") {
		t.Fatalf("旧版の停止案内: %v", err)
	}
	if requests.Load() != 0 {
		t.Fatal("旧版記録から通信しました")
	}
}

func TestCLIConnectAndSeparateProcessStop(t *testing.T) {
	a, _ := discoveryApp(t, func(_ context.Context, p string) ([]byte, error) { return sqlJSON(p, "db"), nil })
	exe, _ := os.Executable()
	lookup := a.LookPath
	original := a.Runner
	a.LookPath = func(n string) (string, error) {
		if n == "cloud-sql-proxy" {
			return exe, nil
		}
		return lookup(n)
	}
	a.Runner = runnerFunc(func(c context.Context, e Executable, args []string) ([]byte, error) {
		if e.Path == exe {
			if args[0] == "--help" {
				return []byte("--address --port --private-ip --structured-logs --run-connection-test"), nil
			}
			return []byte("cloud-sql-proxy version 2.24.1"), nil
		}
		return original.Run(c, e, args)
	})
	a.StopGrace = 100 * time.Millisecond
	t.Setenv("CSQL_TEST_PROXY", "1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- a.Run(ctx, []string{"connect", "db", "--project", "alpha", "-p", "55433"}) }()
	s := awaitSession(t, a)
	client := func(args ...string) []byte {
		cmd := exec.Command(exe, args...)
		cmd.Env = append(os.Environ(), "CSQL_TEST_CLIENT_DIR="+a.Paths.ConfigDir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("別CLI %v: %v %s", args, err, out)
		}
		return out
	}
	if out := client("status"); !strings.Contains(string(out), s.ID) {
		t.Fatalf("別プロセスで確認できない: %s", out)
	}
	client("stop", s.ID)
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("接続CLI終了: %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("別CLI停止後に終了しません")
	}
}

// 編集ロック待ちの起動は子を作らず、解除後の設定だけで起動する。
func TestSessionProfileEditSerialization(t *testing.T) {
	a := proxyApp(t)
	p := Profile{Name: "dev", Instance: testInstance(), Database: "app", Username: "reader", Port: 55434}
	if err := a.AddProfile(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(a.Paths.ConfigDir, "profiles.lock"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	unlock, err := lockProfiles(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	argsfile := filepath.Join(t.TempDir(), "args.json")
	t.Setenv("CSQL_TEST_ARGSFILE", argsfile)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err = a.Connect(ctx, p.Instance, ConnectOptions{Progress: &connectionProgress{}})
	unlock()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ロック待ちの取消: %v", err)
	}
	if _, err := os.Stat(argsfile); !os.IsNotExist(err) {
		t.Fatal("編集ロック中にProxyを起動")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	if _, err := a.UpdateProfilePort(context.Background(), p.Name, p.Port, port); err != nil {
		t.Fatal(err)
	}
	live, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan error, 1)
	go func() { done <- a.Connect(live, p.Instance, ConnectOptions{Progress: &connectionProgress{}}) }()
	session := awaitSession(t, a)
	if session.Port != port {
		t.Fatalf("編集後も旧Port: %+v", session)
	}
	before, _ := os.ReadFile(filepath.Join(a.Paths.ConfigDir, "profiles.json"))
	if _, err := a.UpdateProfilePort(context.Background(), p.Name, port, 55434); err == nil || !strings.Contains(err.Error(), session.ID) {
		t.Fatalf("稼働中を更新: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(a.Paths.ConfigDir, "profiles.json"))
	if string(before) != string(after) {
		t.Fatal("稼働中の拒否で設定を変更")
	}
	stop()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := a.UpdateProfilePort(context.Background(), p.Name, port, 55434); err != nil {
		t.Fatalf("停止後に変更できない: %v", err)
	}
}

func TestSessionAbnormalExit(t *testing.T) {
	a := proxyApp(t)
	t.Setenv("CSQL_TEST_EXIT", "1")
	if err := a.Connect(context.Background(), testInstance(), ConnectOptions{}); err == nil {
		t.Fatal("子の異常終了を成功扱い")
	}
	rows, err := a.Status(context.Background())
	if err != nil || len(rows) != 0 {
		t.Fatalf("異常終了後: %+v %v", rows, err)
	}
}

func proxyApp(t *testing.T) *App {
	a, _, _ := testApp(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	a.LookPath = func(n string) (string, error) {
		if n == "cloud-sql-proxy" {
			return exe, nil
		}
		return "", os.ErrNotExist
	}
	a.Runner = runnerFunc(func(_ context.Context, e Executable, args []string) ([]byte, error) {
		if args[0] == "--help" {
			return []byte("--address --port --private-ip --structured-logs --run-connection-test"), nil
		}
		return []byte("cloud-sql-proxy version 2.24.1"), nil
	})
	a.StopGrace = 30 * time.Millisecond
	t.Setenv("CSQL_TEST_PROXY", "1")
	return a
}
func testInstance() Instance {
	return Instance{"alpha", "db", "asia-northeast1", "alpha:asia-northeast1:db", "POSTGRES_16"}
}
func awaitSession(t *testing.T, a *App) SessionStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := a.Status(context.Background())
		if err == nil && len(rows) == 1 && rows[0].State == "running" {
			return rows[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("セッション公開の時間切れ")
	return SessionStatus{}
}

func awaitSessions(t *testing.T, a *App, count int) []SessionStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := a.Status(context.Background())
		if err == nil && len(rows) == count {
			return rows
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%d件のセッション公開を待機", count)
	return nil
}

func awaitProxyPort(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 20*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Port %dが待受しない", port)
}

// 子のbind前に別の接続を起動しても、同じポートを選ばない。
func TestConcurrentConnectPortReservations(t *testing.T) {
	a := proxyApp(t)
	gate := filepath.Join(t.TempDir(), "bind")
	t.Setenv("CSQL_TEST_BIND_GATE", gate)
	t.Setenv("CSQL_TEST_BIND", "1")
	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	t.Cleanup(func() { cancel(); workers.Wait() })
	start := func(v Instance) {
		workers.Add(1)
		go func() { defer workers.Done(); _ = a.Connect(ctx, v, ConnectOptions{Progress: &connectionProgress{}}) }()
	}
	start(testInstance())
	awaitSessions(t, a, 1)
	other := testInstance()
	other.Name, other.ConnectionName = "second", "alpha:asia-northeast1:second"
	start(other)
	rows := awaitSessions(t, a, 2)
	if rows[0].Port == rows[1].Port {
		t.Fatal("bind前の並行起動で同じポートを割り当てた")
	}
	// OS上はまだ空いていても、明示・保存済みの予約を別Portへ変えない。
	conflict := other
	conflict.Name, conflict.ConnectionName = "conflict", "alpha:asia-northeast1:conflict"
	if err := a.Connect(ctx, conflict, ConnectOptions{Port: rows[0].Port, Progress: &connectionProgress{}}); err == nil {
		t.Fatal("起動待ちの明示Portを重複利用")
	}
	row := rows[0]
	reserved := Profile{Name: "pending", Instance: Instance{row.Project, row.Name, row.Region, row.Project + ":" + row.Region + ":" + row.Name, row.DatabaseVersion}, Database: "app", Username: "reader", Port: row.Port}
	if err := a.AddProfile(ctx, reserved); err != nil {
		t.Fatal(err)
	}
	if err := a.Connect(ctx, reserved.Instance, ConnectOptions{Progress: &connectionProgress{}}); err == nil || !strings.Contains(err.Error(), "別Portへ自動変更しません") {
		t.Fatal("起動待ちの保存済みPortを変更: ", err)
	}
	if err := os.WriteFile(gate, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		awaitProxyPort(t, row.Port)
	}
	if err := a.Stop(context.Background(), rows[0].ID, false); err != nil {
		t.Fatal(err)
	}
	awaitSessions(t, a, 1)
	awaitProxyPort(t, rows[1].Port)
}

// 起動失敗でポートを予約したままにせず、次の起動で再利用できる。
func TestConcurrentConnectReservationRelease(t *testing.T) {
	a := proxyApp(t)
	argsFile := filepath.Join(t.TempDir(), "args.json")
	t.Setenv("CSQL_TEST_ARGSFILE", argsFile)
	t.Setenv("CSQL_TEST_EXIT", "1")
	if err := a.Connect(context.Background(), testInstance(), ConnectOptions{Progress: &connectionProgress{}}); err == nil {
		t.Fatal("異常終了を成功と判断")
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	if err := json.Unmarshal(data, &args); err != nil {
		t.Fatal(err)
	}
	port := 0
	for i := range args {
		if args[i] == "--port" && i+1 < len(args) {
			port, _ = strconv.Atoi(args[i+1])
		}
	}
	if port == 0 {
		t.Fatal("起動したポートを観測できない")
	}
	t.Setenv("CSQL_TEST_EXIT", "0")
	t.Setenv("CSQL_TEST_BIND", "1")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- a.Connect(ctx, testInstance(), ConnectOptions{Port: port, Progress: &connectionProgress{}})
	}()
	t.Cleanup(func() { cancel(); <-done })
	row := awaitSession(t, a)
	if row.Port != port {
		t.Fatal("失敗したポートを再利用できない")
	}
	awaitProxyPort(t, port)
}

func TestSessionOwnedLifecycle(t *testing.T) {
	a := proxyApp(t)
	argsFile := filepath.Join(t.TempDir(), "argv.json")
	t.Setenv("CSQL_TEST_ARGSFILE", argsFile)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Connect(ctx, testInstance(), ConnectOptions{Port: 55432, PrivateIP: true}) }()
	s := awaitSession(t, a)
	// 管理記録の公開は子の初期化より早い。argvの実在を待ってから停止する。
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(argsFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("子の初期化時間切れ")
		}
		time.Sleep(time.Millisecond)
	}
	if err := a.Stop(context.Background(), s.ID, false); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("停止要求が終了を待たない")
	}
	b, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	json.Unmarshal(b, &args)
	joined := strings.Join(args, " ")
	for _, want := range []string{"--address 127.0.0.1", "--port 55432", "--private-ip", "alpha:asia-northeast1:db"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("Proxy引数: %s", joined)
		}
	}
	rows, err := a.Status(context.Background())
	if err != nil || len(rows) != 0 {
		t.Fatalf("停止後記録: %v %v", rows, err)
	}
}

func TestProxyReadinessAndRecovery(t *testing.T) {
	for _, mode := range []string{"success", "ready-only", "auth-fail", "lateauth"} {
		t.Run(mode, func(t *testing.T) {
			a := proxyApp(t)
			a.StartupTimeout = 150 * time.Millisecond
			t.Setenv("CSQL_TEST_LOG_MODE", mode)
			progress := &connectionProgress{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- a.Connect(ctx, testInstance(), ConnectOptions{Progress: progress}) }()
			deadline := time.Now().Add(3 * time.Second)
			for {
				s := progress.Snapshot()
				if mode == "success" && s.State == "ready" {
					cancel()
					break
				}
				if mode == "lateauth" && s.State == "error" {
					if s.Diagnostic == nil || !strings.Contains(s.Diagnostic.Next, "application-default login") {
						t.Fatalf("利用中の再認証案内なし: %+v", s)
					}
					cancel()
					break
				}
				if mode == "auth-fail" || mode == "ready-only" {
					select {
					case err := <-done:
						if err == nil {
							t.Fatal("失敗を成功扱い")
						}
						if mode == "auth-fail" && !strings.Contains(err.Error(), "application-default login") {
							t.Fatalf("対処なし: %v", err)
						}
						if s.State == "ready" {
							t.Fatal("readyログだけで利用可能")
						}
						return
					default:
					}
				}
				if time.Now().After(deadline) {
					cancel()
					t.Fatalf("状態が進まない: %+v", s)
				}
				time.Sleep(time.Millisecond)
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("取消してもProxyが残留")
			}
		})
	}
}
func TestSessionCancelAndForcedStop(t *testing.T) {
	a := proxyApp(t)
	t.Setenv("CSQL_TEST_IGNORE", "1")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Connect(ctx, testInstance(), ConnectOptions{}) }()
	awaitSession(t, a)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("子を強制終了しません")
	}
	rows, _ := a.Status(context.Background())
	if len(rows) != 0 {
		t.Fatal("記録が残りました")
	}
}
func TestSessionAuthenticationAndDuplicateStop(t *testing.T) {
	a := proxyApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Connect(ctx, testInstance(), ConnectOptions{}) }()
	s := awaitSession(t, a)
	file := filepath.Join(a.Paths.ConfigDir, "sessions", s.ID+".json")
	var record sessionRecord
	if err := readJSONFile(file, &record); err != nil {
		t.Fatal(err)
	}
	bad := record
	bad.Token = strings.Repeat("0", 64)
	if _, err := a.querySession(context.Background(), bad, true); err == nil {
		t.Fatal("無認証の停止を受け付けました")
	}
	rows, _ := a.Status(context.Background())
	if len(rows) != 1 || rows[0].State != "running" {
		t.Fatal("認証失敗で停止しました")
	}
	errCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := a.querySession(context.Background(), record, true); errCh <- err }()
	}
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestPortAndUnresponsiveRecord(t *testing.T) {
	a, _, _ := testApp(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	p := ln.Addr().(*net.TCPAddr).Port
	if _, err := choosePort("POSTGRES_16", p); err == nil {
		t.Fatal("明示ポートを勝手に変更")
	}
	if _, err := choosePort("UNKNOWN", 0); err == nil {
		t.Fatal("未知DBの暗黙ポート")
	}
	r := sessionRecord{SessionStatus: SessionStatus{ID: strings.Repeat("a", 32), Name: "db", Project: "alpha", Port: 5432}, ControlPort: 1, Token: strings.Repeat("b", 64)}
	if err := writeJSON(filepath.Join(a.Paths.ConfigDir, "sessions", r.ID+".json"), r); err != nil {
		t.Fatal(err)
	}
	rows, err := a.Status(context.Background())
	if err != nil || len(rows) != 1 || rows[0].State != "unresponsive" {
		t.Fatalf("古い記録: %v %v", rows, err)
	}
	if err := a.Stop(context.Background(), r.ID, false); err == nil {
		t.Fatal("応答なしを停止成功扱い")
	}
}
