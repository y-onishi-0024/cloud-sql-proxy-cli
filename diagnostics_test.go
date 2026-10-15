package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiagnosticGcloudCLI(t *testing.T) {
	a, _ := discoveryApp(t, func(context.Context, string) ([]byte, error) {
		return nil, fmt.Errorf("auth: invalid_grant reauth related error (invalid_rapt)")
	})
	errs := &bytes.Buffer{}
	a.Err = errs
	if code := a.Run(context.Background(), []string{"list"}); code != 1 || !strings.Contains(errs.String(), "auth login") {
		t.Fatalf("検索認証の復旧手順なし: code=%d %s", code, errs)
	}
	if strings.Contains(errs.String(), "application-default login") {
		t.Fatal("検索認証にADCの再認証を案内")
	}
}

func TestProxyLogBounds(t *testing.T) {
	p := &connectionProgress{}
	p.reset(FailureContext{Source: "proxy", Operation: "接続", CredentialMode: "user"})
	w := &proxyLogWriter{progress: p}
	for i := 0; i < 200; i++ {
		w.Write([]byte(strings.Repeat("あ", 1000) + "\n"))
	}
	w.Write([]byte(strings.Repeat("x", 100<<10) + "\n"))
	w.Write([]byte(`{"severity":"ERROR","message":"invalid_`))
	w.Write([]byte("rapt password=private-secret\"}\n"))
	s := p.Snapshot()
	joined := strings.Join(s.Logs, "\n")
	if len(joined) > 257<<10 || !strings.Contains(joined, "省略") || strings.Contains(joined, "private-secret") || s.State != "error" || s.Diagnostic == nil || !strings.Contains(s.Diagnostic.Next, "application-default login") {
		t.Fatalf("上限/末尾の対処/秘密除去を喪失: bytes=%d state=%s diagnostic=%+v", len(joined), s.State, s.Diagnostic)
	}
}

func TestDiagnosticRecovery(t *testing.T) {
	for _, tc := range []struct{ source, mode, raw, want, avoid string }{
		{"proxy", "user", `auth: invalid_grant reauth related error (invalid_rapt)`, "auth application-default login", "auth login\n"},
		{"gcloud", "user", `invalid_grant invalid_rapt`, "auth login", "application-default"},
		{"proxy", "explicit", `invalid_grant`, "GOOGLE_APPLICATION_CREDENTIALS", "application-default login"},
		{"proxy", "user", `googleapi: Error 403: permission_denied`, "Cloud SQL Client", "APIが無効"},
		{"gcloud", "user", `googleapi: Error 403: SERVICE_DISABLED`, "Cloud SQL Admin API", "auth login"},
		{"proxy", "user", `dial tcp: i/o timeout`, "VPN", "auth application-default"},
		{"local", "user", `bind: address already in use`, "csql status", "application-default"},
		{"local", "user", `permission denied: profiles.json`, "権限", "Cloud SQL Client"},
		{"proxy", "user", `unrecognized upstream failure`, "詳細ログ", "auth login"},
	} {
		t.Run(tc.source+tc.raw, func(t *testing.T) {
			d := explainFailure(FailureContext{Source: tc.source, CredentialMode: tc.mode, GcloudCommand: "'/managed path/gcloud'", PrivateIP: true}, tc.raw)
			text := formatDiagnostic(d)
			if !strings.Contains(text, tc.want) || strings.Contains(text, tc.avoid) {
				t.Fatalf("対処が違う: %s", text)
			}
		})
	}
	text := safeLog("\x1b[31mAuthorization: Bearer test-secret\x1b[0m\n{\"access_token\":\"token-secret\",\"password\":\"pass-secret\"} postgresql://u:uri-secret@localhost/db")
	for _, secret := range []string{"test-secret", "token-secret", "pass-secret", "uri-secret", "\x1b"} {
		if strings.Contains(text, secret) {
			t.Fatalf("秘密/制御文字が残留: %s", text)
		}
	}
}

// 実際に出力される詳細と保持ログの両方で、値の一部分も漏らさない。
func TestDiagnosticSecretValues(t *testing.T) {
	for _, raw := range []string{
		`{"Authorization":"Bearer opaque-secret"}`,
		`Authorization: Basic basic-secret`,
		`{"password":"first second-secret"}`,
		`password='first \' second-secret'`,
		`{"client_secret":"first \"second-secret\" last"}`,
		`Authorization: Digest username="secret-user", response="secret-response"`,
	} {
		p := &connectionProgress{}
		p.reset(FailureContext{Source: "proxy", Operation: "接続"})
		out, errs := &bytes.Buffer{}, &bytes.Buffer{}
		w := &proxyLogWriter{progress: p, out: out, errs: errs}
		w.Write([]byte(raw + "\n"))
		all := out.String() + errs.String() + strings.Join(p.Snapshot().Logs, "\n") + formatDiagnostic(explainFailure(FailureContext{}, raw))
		for _, secret := range []string{"opaque-secret", "basic-secret", "second-secret", "first", "secret-user", "secret-response"} {
			if strings.Contains(all, secret) {
				t.Errorf("秘密値が残留: %s", all)
			}
		}
	}
}

func TestDiagnosticMissingGcloudAndWindowsUI(t *testing.T) {
	a, _, _ := testApp(t)
	d := explainFailure(a.failureContext(context.Background(), "proxy", "接続", testInstance(), false), "invalid_rapt")
	if !strings.Contains(d.Next, "csql setup --install") || !strings.Contains(d.Next, "auth application-default login") {
		t.Fatalf("未導入時の復旧手順なし: %s", d.Next)
	}
	a.OS = "windows"
	root := filepath.Join(a.Paths.ToolsDir, "fixture-sdk")
	if err := os.MkdirAll(filepath.Join(root, "lib"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "lib", "gcloud.py"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	a.LookPath = func(name string) (string, error) {
		if name == "gcloud" {
			return filepath.Join(root, "bin", "gcloud.cmd"), nil
		}
		return "/fixture/python", nil
	}
	// 画面の失敗処理ではPython候補の照会を含む外部実行を許さない。
	a.Runner = runnerFunc(func(context.Context, Executable, []string) ([]byte, error) {
		t.Error("エラー描画中に外部コマンドを実行")
		return nil, fmt.Errorf("実行禁止")
	})
	f := a.failureContext(context.Background(), "proxy", "接続", testInstance(), false)
	if !strings.Contains(f.GcloudCommand, "gcloud.cmd") {
		t.Fatalf("Windowsの実行可能な復旧コマンドなし: %+v", f)
	}
}

func TestProxyRetainsKnownFailure(t *testing.T) {
	for _, lines := range [][]string{
		{"ready for new connections", "invalid_grant", "Connection test passed"},
		{"invalid_grant", "Connection test passed", "ready for new connections"},
		{"Connection test passed", "invalid_grant", "Connection test started", "ready for new connections"},
	} {
		p := &connectionProgress{}
		p.reset(FailureContext{Source: "proxy", Operation: "接続"})
		out, errs := &bytes.Buffer{}, &bytes.Buffer{}
		stdout := &proxyLogWriter{progress: p, out: out, errs: errs}
		stderr := &proxyLogWriter{progress: p, out: out, errs: errs}
		for _, line := range lines {
			w, severity := stdout, "INFO"
			if line == "invalid_grant" {
				w, severity = stderr, "ERROR"
			}
			data := fmt.Sprintf("{\"severity\":%q,\"message\":%q}\n", severity, line)
			w.Write([]byte(data[:len(data)/2]))
			w.Write([]byte(data[len(data)/2:]))
		}
		s := p.Snapshot()
		if s.State != "error" || s.Diagnostic == nil || strings.Contains(out.String(), "Proxy利用可能") {
			t.Errorf("遅着成功で既知エラーが消失: %+v %s", s, out)
		}
	}
}
