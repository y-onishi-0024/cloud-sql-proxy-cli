package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

type FailureContext struct {
	Source, Operation, Project, Instance, GcloudCommand, CredentialMode string
	PrivateIP                                                           bool
	GcloudMissing                                                       bool
}
type Diagnostic struct{ Summary, Cause, Next, Detail string }
type diagnosticError struct {
	Context FailureContext
	Err     error
}

func (e diagnosticError) Error() string {
	return formatDiagnostic(explainFailure(e.Context, e.Err.Error()))
}
func (e diagnosticError) Unwrap() error { return e.Err }

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(\b(?:proxy-authorization|authorization)["']?\s*[:=]\s*)(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^\r\n]+)`),
	regexp.MustCompile(`(?i)(\b(?:access_token|refresh_token|id_token|password|client_secret)["']?\s*[:=]\s*)(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^\r\n,&}]+)`),
	regexp.MustCompile(`\bya29\.[A-Za-z0-9._-]+`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`),
	regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://[^\s/:@]+:)[^\s@]+@`),
}

// 外部ログは表示前に秘密値と端末制御列を落とす。認証ファイルは読まない。
func safeLog(s string) string {
	s = strings.ToValidUTF8(ansi.Strip(s), "�")
	for i, p := range secretPatterns {
		if i == 2 || i == 3 {
			s = p.ReplaceAllString(s, "[非表示]")
		} else if i == 4 {
			s = p.ReplaceAllString(s, "${1}[非表示]@")
		} else {
			s = p.ReplaceAllString(s, "${1}[非表示]")
		}
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' {
			if r == '\t' {
				return ' '
			}
			return -1
		}
		return r
	}, s)
}
func explainFailure(fc FailureContext, raw string) Diagnostic {
	lower := strings.ToLower(raw)
	gcloud := fc.GcloudCommand
	if gcloud == "" {
		gcloud = "gcloud"
	}
	d := Diagnostic{Summary: fc.Operation + "に失敗しました", Cause: "原因を特定できませんでした", Next: "csql doctor と詳細ログを確認してください", Detail: safeLog(raw)}
	contains := func(values ...string) bool {
		for _, v := range values {
			if strings.Contains(lower, v) {
				return true
			}
		}
		return false
	}
	switch {
	case contains("service_disabled", "accessnotconfigured", "api has not been used", "api has not been enabled", "it is disabled"):
		d.Cause = "Cloud SQL Admin APIが無効な可能性があります"
		d.Next = "対象プロジェクト " + fc.Project + " のCloud SQL Admin APIを管理者に確認してください（自動変更は行いません）"
	case contains("invalid_rapt", "invalid_grant", "reauthentication", "reauth related", "no active account", "not currently have an active account", "please run: $ gcloud auth login", "please run:\n\n  $ gcloud auth login", "could not find default credentials", "default credentials were not found", "credentials are not found", "token has been expired", "token has expired"):
		if fc.Source == "gcloud" {
			d.Cause = "検索に使うgcloudの認証が必要です"
			d.Next = gcloud + " auth login\n認証後、csql --refresh で一覧を再取得してください"
		} else if fc.Source == "proxy" {
			if fc.CredentialMode == "explicit" {
				d.Cause = "明示したADC認証設定で認証できません"
				d.Next = "GOOGLE_APPLICATION_CREDENTIALSで指定した認証ファイルの発行元・有効期限・アカウントを確認し、Proxyを再起動してください"
			} else {
				d.Cause = "Proxy用のGoogle認証（ADC）が未設定または再認証を必要としています"
				d.Next = gcloud + " auth application-default login\n認証後、Proxyを再起動してください。検索用のgcloud auth loginとは別の認証です"
			}
		}
	case fc.Source != "local" && contains("permission_denied", "permission denied", "not authorized", "does not have permission", "403 forbidden", "error 403"):
		d.Cause = "対象へのアクセス権限が不足している可能性があります"
		d.Next = "対象 " + fc.Project + " / " + fc.Instance + " と、利用中アカウントのCloud SQL Client権限（cloudsql.instances.get / connect）を管理者に確認してください。検索の失敗ではプロジェクト・インスタンスの参照権限も確認してください"
	case contains("address already in use", "only one usage of each socket", "port", "ポート", "使用中", "予約", "一致しません") && (fc.Source == "local" || contains("address already in use", "only one usage of each socket", "bind:")):
		d.Cause = "ローカルPortまたは保存済み予約と競合しています"
		d.Next = "csql status と csql profile list を確認してください。保存済みPortは自動変更しません"
	case contains("no such host", "network is unreachable", "i/o timeout", "timed out", "deadline exceeded", "connection refused", "connection reset", "tls handshake timeout", "時間切れ"):
		d.Cause = "通信または起動確認が完了しませんでした"
		d.Next = "インターネット接続・DNSを確認してください"
		if fc.PrivateIP {
			d.Next += "。Private IPを使うため、VPNとVPCへの経路も確認してください"
		}
	case contains("csql setup", "未導入", "proxy v2", "非対応"):
		d.Cause = "必要なコマンドが不足しているか非対応です"
		d.Next = "csql doctor\ncsql setup --install"
	case contains("profiles.json", "保存", "リンク", "permission denied") && fc.Source == "local":
		d.Cause = "ローカル設定を読み書きできません"
		d.Next = "詳細に表示されたパスの権限・リンク・JSON形式を確認してください。既存設定を退避してから修復してください"
	}
	if fc.GcloudMissing && strings.Contains(d.Next, " auth ") {
		d.Next = "gcloudが見つかりません。csql doctor / csql setup --install で導入してください。\n導入後: " + d.Next
	}
	return d
}
func formatDiagnostic(d Diagnostic) string {
	return fmt.Sprintf("%s\n原因: %s\n次の操作:\n%s\n詳細: %s", d.Summary, d.Cause, d.Next, d.Detail)
}

func (a *App) commandDisplay(e Executable) string {
	parts := append([]string{e.Path}, e.PrefixArgs...)
	for i, s := range parts {
		if a.OS == "windows" {
			parts[i] = "'" + strings.ReplaceAll(s, "'", "''") + "'"
		} else {
			parts[i] = shellQuote(s)
		}
	}
	result := strings.Join(parts, " ")
	if a.OS == "windows" {
		result = "& " + result
	}
	return result
}
func (a *App) failureContext(ctx context.Context, source, operation string, v Instance, private bool) FailureContext {
	f := FailureContext{Source: source, Operation: operation, Project: v.Project, Instance: v.Name, PrivateIP: private, CredentialMode: "user", GcloudCommand: "gcloud"}
	if a.Env("GOOGLE_APPLICATION_CREDENTIALS") != "" {
		f.CredentialMode = "explicit"
	}
	// 描画中にも使うため外部実行しない。.cmdも利用者のPowerShellから直接起動できる。
	if p, err := a.LookPath("gcloud"); err == nil {
		f.GcloudCommand = a.commandDisplay(Executable{Path: p})
	} else {
		f.GcloudMissing = true
		var meta installedTool
		if readJSONFile(filepath.Join(a.Paths.ToolsDir, "gcloud", "installed.json"), &meta) == nil && meta.Version != "" && safeRelative(meta.RelativePath) {
			p := filepath.Join(a.Paths.ToolsDir, "gcloud", meta.RelativePath)
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
				f.GcloudCommand = a.commandDisplay(Executable{Path: p})
				f.GcloudMissing = false
			}
		}
	}
	return f
}
