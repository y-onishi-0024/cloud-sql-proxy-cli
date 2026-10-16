package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type ConnectOptions struct {
	Port      int
	PrivateIP bool
	Progress  *connectionProgress
}

type connectionSnapshot struct {
	State      string
	Revision   uint64
	Logs       []string
	Diagnostic *Diagnostic
	Session    SessionStatus
}
type connectionProgress struct {
	mu                           sync.Mutex
	snapshot                     connectionSnapshot
	ready                        chan struct{}
	readySeen, tested, announced bool
	logBytes                     int
	omitted                      bool
	context                      FailureContext
}

func (p *connectionProgress) reset(fc FailureContext) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snapshot = connectionSnapshot{State: "starting", Revision: p.snapshot.Revision + 1}
	p.ready = make(chan struct{})
	p.readySeen = false
	p.tested = false
	p.announced = false
	p.logBytes = 0
	p.omitted = false
	p.context = fc
}
func (p *connectionProgress) Snapshot() connectionSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.snapshot
	s.Logs = append([]string(nil), s.Logs...)
	if p.omitted {
		s.Logs = append([]string{"（保持上限を超えたログを省略しました）"}, s.Logs...)
	}
	if s.Diagnostic != nil {
		d := *s.Diagnostic
		s.Diagnostic = &d
	}
	return s
}
func (p *connectionProgress) state(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snapshot.State = s
	p.snapshot.Revision++
}

// ログと状態を同じsnapshotへ反映し、UIの速度で子プロセスを止めない。
func (p *connectionProgress) log(line string) (string, *Diagnostic, bool) {
	var entry struct{ Message, Severity, Timestamp string }
	message, severity := line, ""
	if json.Unmarshal([]byte(line), &entry) == nil && entry.Message != "" {
		message = entry.Message
		severity = entry.Severity
		line = strings.TrimSpace(entry.Timestamp + " " + entry.Severity + " " + entry.Message)
	}
	line = safeLog(line)
	if len(line) > 64<<10 {
		line = strings.ToValidUTF8(line[:(64<<10)-48], "") + "（行を省略）"
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snapshot.Logs = append(p.snapshot.Logs, line)
	p.logBytes += len(line) + 1
	for p.logBytes > (256<<10)-128 && len(p.snapshot.Logs) > 0 {
		p.logBytes -= len(p.snapshot.Logs[0]) + 1
		p.snapshot.Logs = p.snapshot.Logs[1:]
		p.omitted = true
	}
	lower := strings.ToLower(message)
	if strings.Contains(lower, "ready for new connections") {
		p.readySeen = true
		if !p.announced && p.snapshot.Diagnostic == nil {
			p.snapshot.State = "checking"
		}
	}
	if strings.Contains(lower, "connection test started") {
		if !p.announced && p.snapshot.Diagnostic == nil {
			p.snapshot.State = "checking"
		}
	}
	if strings.Contains(lower, "connection test passed") {
		p.tested = true
	}
	var diagnostic *Diagnostic
	if strings.EqualFold(severity, "ERROR") || strings.EqualFold(severity, "CRITICAL") || strings.Contains(lower, "failed to connect") || strings.Contains(lower, "invalid_grant") || strings.Contains(lower, "connection test failed") {
		d := explainFailure(p.context, message)
		p.snapshot.State = "error"
		if p.snapshot.Diagnostic == nil || p.snapshot.Diagnostic.Detail != d.Detail {
			diagnostic = &d
		}
		p.snapshot.Diagnostic = &d
	}
	// stdout/stderr間の到着順は不定。既知の失敗は新しい接続試行まで保持する。
	newReady := p.readySeen && p.tested && !p.announced && p.snapshot.Diagnostic == nil
	if newReady {
		p.announced = true
		p.snapshot.State = "ready"
		close(p.ready)
	}
	p.snapshot.Revision++
	return line, diagnostic, newReady
}

type proxyLogWriter struct {
	mu        sync.Mutex
	pending   []byte
	truncated bool
	progress  *connectionProgress
	out, errs io.Writer
}

func (w *proxyLogWriter) emit() {
	line := string(w.pending)
	if w.truncated {
		line += "（行を省略）"
	}
	w.pending = nil
	w.truncated = false
	line, d, ready := w.progress.log(line)
	if w.out != nil {
		fmt.Fprintln(w.out, line)
		if ready {
			fmt.Fprintln(w.out, "Proxy利用可能: Cloud SQLへの到達を確認しました。DBログインは未検証です。")
		}
		if d != nil {
			fmt.Fprintln(w.errs, formatDiagnostic(*d))
		}
	}
}
func (w *proxyLogWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(b)
	for len(b) > 0 {
		index := bytes.IndexByte(b, '\n')
		part := b
		if index >= 0 {
			part = b[:index]
		}
		room := (64 << 10) - len(w.pending)
		if len(part) > room {
			w.pending = append(w.pending, part[:room]...)
			w.truncated = true
		} else {
			w.pending = append(w.pending, part...)
		}
		if index < 0 {
			break
		}
		w.emit()
		b = b[index+1:]
	}
	return n, nil
}
func (w *proxyLogWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) > 0 || w.truncated {
		w.emit()
	}
}

type SessionStatus struct {
	ID, Project, Name, Region, DatabaseVersion, State string
	Port                                              int
}
type sessionRecord struct {
	SessionStatus
	ProtocolVersion int
	ControlPort     int
	Token           string
}

const sessionProtocolVersion = 2

func controlMAC(token, message string) string {
	key, err := hex.DecodeString(token)
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}
func requestMAC(token, nonce, method, path string) string {
	return controlMAC(token, "csql-request-v2\n"+nonce+"\n"+method+"\n"+path)
}
func responseMAC(token, nonce string, status int, body []byte) string {
	hash := sha256.Sum256(body)
	return controlMAC(token, fmt.Sprintf("csql-response-v2\n%s\n%d\n%x", nonce, status, hash))
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func validHex(s string, n int) bool {
	if len(s) != n*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func choosePort(db string, explicit int) (int, error) {
	start := explicit
	attempts := 1
	if explicit == 0 {
		attempts = 21
		// プロファイル保存時と同じく、ローカルDBの標準ポートを避ける。
		switch {
		case strings.HasPrefix(db, "POSTGRES"):
			start = 15432
		case strings.HasPrefix(db, "MYSQL"):
			start = 13306
		case strings.HasPrefix(db, "SQLSERVER"):
			start = 11433
		default:
			return 0, fmt.Errorf("未知のDB種別です。--port を指定してください")
		}
	}
	if start < 1 || start > 65535 {
		return 0, usageError{fmt.Errorf("ポートは1〜65535です")}
	}
	for p := start; p < start+attempts && p <= 65535; p++ {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			l.Close()
			return p, nil
		}
	}
	return 0, fmt.Errorf("127.0.0.1:%d のポートを利用できません。別の --port を指定してください", start)
}

// 子がまだbindしていない間も予約し、同じAppの並行起動による衝突を防ぐ。
func (a *App) reserveProxyPort(v Instance, explicit int) (int, func(), error) {
	a.proxyPortsMu.Lock()
	defer a.proxyPortsMu.Unlock()
	port, err := a.profilePort(v, explicit, a.proxyPorts)
	if err != nil {
		return 0, nil, err
	}
	if a.proxyPorts == nil {
		a.proxyPorts = make(map[int]bool)
	}
	a.proxyPorts[port] = true
	return port, func() {
		a.proxyPortsMu.Lock()
		defer a.proxyPortsMu.Unlock()
		delete(a.proxyPorts, port)
	}, nil
}

func (a *App) reservedProxyPorts() map[int]bool {
	a.proxyPortsMu.Lock()
	defer a.proxyPortsMu.Unlock()
	ports := make(map[int]bool, len(a.proxyPorts))
	for port := range a.proxyPorts {
		ports[port] = true
	}
	return ports
}

func (a *App) Connect(ctx context.Context, v Instance, opt ConnectOptions) (retErr error) {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !validInstance(v) {
		return fmt.Errorf("接続対象が不正です")
	}
	e, err := a.resolve(ctx, "cloud-sql-proxy")
	if err != nil {
		return err
	}
	// 設定を読む前に排他し、管理記録の公開までポート編集を待たせる。
	unlockProfiles, err := a.lockProfileStore(ctx)
	if err != nil {
		return err
	}
	defer unlockProfiles()
	port, releasePort, err := a.reserveProxyPort(v, opt.Port)
	if err != nil {
		return diagnosticError{a.failureContext(ctx, "local", "Proxy起動", v, opt.PrivateIP), err}
	}
	// 後から登録する子のWait・管理記録削除が完了してから解放する。
	defer releasePort()
	fc := a.failureContext(ctx, "proxy", "Cloud SQLへの接続", v, opt.PrivateIP)
	progress := opt.Progress
	if progress == nil {
		progress = &connectionProgress{}
	}
	progress.reset(fc)
	defer func() {
		if retErr != nil && !errors.Is(retErr, context.Canceled) {
			progress.state("error")
		} else {
			progress.state("stopped")
		}
	}()
	id, err := randomHex(16)
	if err != nil {
		return err
	}
	token, err := randomHex(32)
	if err != nil {
		return err
	}
	dir := filepath.Join(a.Paths.ConfigDir, "sessions")
	if err := secureDir(dir); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	record := sessionRecord{SessionStatus: SessionStatus{id, v.Project, v.Name, v.Region, v.DatabaseVersion, "running", port}, ControlPort: listener.Addr().(*net.TCPAddr).Port, Token: token, ProtocolVersion: sessionProtocolVersion}
	args := append([]string{}, e.PrefixArgs...)
	args = append(args, "--address", "127.0.0.1", "--port", fmt.Sprint(port))
	args = append(args, "--structured-logs", "--run-connection-test")
	if opt.PrivateIP {
		args = append(args, "--private-ip")
	}
	args = append(args, v.ConnectionName)
	cmd := exec.Command(e.Path, args...)
	output := &proxyLogWriter{progress: progress}
	errorOutput := &proxyLogWriter{progress: progress}
	if opt.Progress == nil {
		output.out = a.stdout()
		output.errs = a.stderr()
		errorOutput.out = a.stderr()
		errorOutput.errs = a.stderr()
	}
	cmd.Stdout = output
	cmd.Stderr = errorOutput
	prepareProxy(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("Proxyを起動できません: %w", err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); output.flush(); errorOutput.flush(); close(done) }()
	var stopOnce sync.Once
	var stopping atomic.Bool
	// 所有したプロセスのみ停止する。Waitは別goroutineの一度だけに集約する。
	stopChild := func() {
		stopOnce.Do(func() {
			progress.state("stopping")
			stopping.Store(true)
			select {
			case <-done:
				return
			default:
			}
			if err := gracefulStop(cmd.Process); err != nil {
				_ = cmd.Process.Kill()
			}
			go func() {
				timer := time.NewTimer(a.StopGrace)
				defer timer.Stop()
				select {
				case <-done:
				case <-timer.C:
					_ = cmd.Process.Kill()
				}
			}()
		})
	}
	defer func() { stopChild(); <-done }()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions/"+id || r.Host != listener.Addr().String() || r.Header.Get("Origin") != "" {
			http.Error(w, "対象が不正です", http.StatusForbidden)
			return
		}
		// 秘密を送信せず、要求と応答で異なる署名対象を使って反射を拒否する。
		nonce := r.Header.Get("X-CSQL-Nonce")
		if !validHex(nonce, 32) || !hmac.Equal([]byte(r.Header.Get("X-CSQL-Auth")), []byte(requestMAC(token, nonce, r.Method, r.URL.Path))) {
			http.Error(w, "認証失敗", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.ContentLength > 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		status := record.SessionStatus
		if r.Method == http.MethodPost {
			stopChild()
			select {
			case <-done:
				status.State = "stopped"
			case <-r.Context().Done():
				return
			}
		} else {
			select {
			case <-done:
				status.State = "stopped"
			default:
			}
		}
		w.Header().Set("Content-Type", "application/json")
		body, err := json.Marshal(status)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("X-CSQL-Proof", responseMAC(token, nonce, http.StatusOK, body))
		_, _ = w.Write(body)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 20 * time.Second, MaxHeaderBytes: 8192}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
		}
	}()
	file := filepath.Join(dir, id+".json")
	if err := writeJSON(file, record); err != nil {
		return fmt.Errorf("管理情報を公開できません: %w", err)
	}
	defer func() {
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			retErr = errors.Join(retErr, fmt.Errorf("管理情報を削除できません: %w", err))
		}
	}()
	unlockProfiles()
	progress.mu.Lock()
	progress.snapshot.Session = record.SessionStatus
	progress.snapshot.Revision++
	progress.mu.Unlock()
	if opt.Progress == nil {
		fmt.Fprintf(a.stdout(), "Proxy起動: %s / %s\nリージョン: %s  DB: %s\nHost: 127.0.0.1  Port: %d\nセッション: %s\n別端末の csql status / csql stop %s、またはCtrl+Cで終了できます。\nDBログインは未検証です。Cloud SQLへの到達を確認中です。\n", v.Project, v.Name, v.Region, v.DatabaseVersion, port, id, id)
	}
	timeout := a.StartupTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	startup := timer.C
	ready := progress.ready
	for {
		select {
		case <-ready:
			timer.Stop()
			startup = nil
			ready = nil
		case <-startup:
			stopChild()
			<-done
			if s := progress.Snapshot(); s.Diagnostic != nil {
				return fmt.Errorf("Proxy起動・接続確認が時間切れです（%s）\n%s", timeout, formatDiagnostic(*s.Diagnostic))
			}
			return diagnosticError{fc, fmt.Errorf("Proxy起動・接続確認が時間切れです（%s）。詳細ログを確認してください", timeout)}
		case <-ctx.Done():
			stopChild()
			<-done
			return ctx.Err()
		case <-done:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if stopping.Load() {
				return nil
			}
			if waitErr != nil {
				s := progress.Snapshot()
				if s.Diagnostic != nil {
					return fmt.Errorf("Proxyが異常終了しました: %w\n%s", waitErr, formatDiagnostic(*s.Diagnostic))
				}
				return diagnosticError{fc, fmt.Errorf("Proxyが異常終了しました: %w", waitErr)}
			}
			return nil
		case err := <-serveErr:
			stopChild()
			<-done
			return fmt.Errorf("制御サーバーが終了しました: %w", err)
		}
	}
}

func (a *App) records() ([]sessionRecord, error) {
	dir := filepath.Join(a.Paths.ConfigDir, "sessions")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []sessionRecord
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var r sessionRecord
		if err := readJSONFile(filepath.Join(dir, entry.Name()), &r); err != nil {
			return nil, fmt.Errorf("管理記録が不正です: %s", entry.Name())
		}
		if !validHex(r.ID, 16) || !validHex(r.Token, 32) || entry.Name() != r.ID+".json" || r.ControlPort < 1 || r.ControlPort > 65535 || r.Port < 1 || r.Port > 65535 {
			return nil, fmt.Errorf("管理記録の項目が不正です: %s", entry.Name())
		}
		records = append(records, r)
	}
	return records, nil
}

func (a *App) querySession(ctx context.Context, r sessionRecord, stop bool) (SessionStatus, error) {
	if r.ProtocolVersion != sessionProtocolVersion {
		return SessionStatus{}, fmt.Errorf("旧版または非対応の管理記録です。起動した端末でCtrl+Cで停止し、現在のCLIで再起動してください")
	}
	if !validHex(r.ID, 16) || !validHex(r.Token, 32) || r.ControlPort < 1 || r.ControlPort > 65535 {
		return SessionStatus{}, fmt.Errorf("制御先の情報が不正です")
	}
	method := http.MethodGet
	timeout := 2 * time.Second
	if stop {
		method = http.MethodPost
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: timeout}).DialContext}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("http://127.0.0.1:%d/v1/sessions/%s", r.ControlPort, r.ID), nil)
	if err != nil {
		return SessionStatus{}, err
	}
	nonce, err := randomHex(32)
	if err != nil {
		return SessionStatus{}, err
	}
	req.Header.Set("X-CSQL-Nonce", nonce)
	req.Header.Set("X-CSQL-Auth", requestMAC(r.Token, nonce, method, req.URL.Path))
	res, err := client.Do(req)
	if err != nil {
		return SessionStatus{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return SessionStatus{}, fmt.Errorf("制御通信 HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, (64<<10)+1))
	if err != nil {
		return SessionStatus{}, err
	}
	if len(body) > 64<<10 {
		return SessionStatus{}, fmt.Errorf("制御応答が容量上限を超えています")
	}
	if !hmac.Equal([]byte(res.Header.Get("X-CSQL-Proof")), []byte(responseMAC(r.Token, nonce, res.StatusCode, body))) {
		return SessionStatus{}, fmt.Errorf("制御応答の所有者を確認できません")
	}
	var status SessionStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return status, err
	}
	expected := r.SessionStatus
	expected.State = status.State
	if status != expected || stop && status.State != "stopped" || !stop && status.State != "running" && status.State != "stopped" {
		return status, fmt.Errorf("制御応答のID・接続情報・状態が不正です")
	}
	return status, nil
}

func (a *App) Status(ctx context.Context) ([]SessionStatus, error) {
	records, err := a.records()
	if err != nil {
		return nil, err
	}
	rows := make([]SessionStatus, len(records))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, r := range records {
		if r.ProtocolVersion != sessionProtocolVersion {
			fmt.Fprintf(a.stderr(), "%s: 旧版または非対応の管理記録です。起動した端末でCtrl+Cで停止し、現在のCLIで再起動してください\n", r.ID)
		}
		wg.Add(1)
		go func(i int, r sessionRecord) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				rows[i] = r.SessionStatus
				rows[i].State = "unresponsive"
				return
			}
			s, err := a.querySession(ctx, r, false)
			if err != nil {
				s = r.SessionStatus
				s.State = "unresponsive"
			}
			rows[i] = s
		}(i, r)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return rows, ctx.Err()
	}
	return rows, nil
}
func (a *App) Stop(ctx context.Context, id string, all bool) error {
	if !all && !validHex(id, 16) {
		return usageError{fmt.Errorf("セッションIDが不正です")}
	}
	records, err := a.records()
	if err != nil {
		return err
	}
	if !all {
		found := false
		for _, r := range records {
			if r.ID == id {
				records = []sessionRecord{r}
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("指定セッションの記録がありません")
		}
	}
	if len(records) == 0 {
		fmt.Fprintln(a.stdout(), "管理中のセッションはありません")
		return nil
	}
	results := make([]error, len(records))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, r := range records {
		wg.Add(1)
		go func(i int, r sessionRecord) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i] = ctx.Err()
				return
			}
			_, results[i] = a.querySession(ctx, r, true)
		}(i, r)
	}
	wg.Wait()
	var errs []error
	for i, r := range records {
		if results[i] != nil {
			err := fmt.Errorf("%s の停止を確認できません: %w", r.ID, results[i])
			fmt.Fprintln(a.stderr(), err)
			errs = append(errs, err)
		} else {
			fmt.Fprintf(a.stdout(), "%s: 停止を確認しました\n", r.ID)
		}
	}
	return errors.Join(errs...)
}
