package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/pe"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"github.com/spf13/cobra"
)

const dbToolJSONLimit = 16 << 20
const dbToolOutputLimit = 64 << 10

type dbToolOptions struct{ Tool, Executable, PGAdminSQLite, PGAdminUser, DBeaverWorkspace string }
type dbToolTarget struct{ Tool, Version, Executable, Python, SetupScript, Store, User, Project, Identity string }
type dbToolRegistrationPlan struct {
	Profile                                      Profile
	Target                                       dbToolTarget
	Name, Fingerprint, BeforeDigest, Disposition string
	Options                                      dbToolOptions
	Files                                        []pathFile
	Data                                         []byte
	ID                                           string
}
type dbToolRegistrationResult struct {
	Status, Name, Target, Backup, Detail string
	Submitted, Committed, Verified       bool
}
type dbToolReceipt struct {
	Version int
	Entries map[string]dbToolRegistrationResult
}

func dbToolHash(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func dbToolName(p Profile) string { return "csql / " + p.Project + " / " + p.Name }
func dbToolAbsolute(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("保存先または実行ファイルを指定してください")
	}
	return filepath.Abs(filepath.Clean(p))
}
func dbToolRegular(p string) error { _, _, err := pathRead(p, 128<<20); return err }

// 検出では実行ファイルを起動せず、既知のパッケージ配置だけを採用する。
func (a *App) detectDBTool(o dbToolOptions) (dbToolTarget, error) {
	t := dbToolTarget{Tool: o.Tool, User: o.PGAdminUser}
	if a.OS != "darwin" && a.OS != "windows" {
		return t, fmt.Errorf("直接登録はmacOS・Windowsが対象です。profile exportで出力してください")
	}
	if o.Tool != "pgadmin" && o.Tool != "dbeaver" && o.Tool != "a5" {
		return t, fmt.Errorf("--tool pgadmin|dbeaver|a5 を指定してください")
	}
	if o.Tool != "pgadmin" && (o.PGAdminSQLite != "" || o.PGAdminUser != "") || o.Tool != "dbeaver" && o.DBeaverWorkspace != "" {
		return t, fmt.Errorf("別ツールの保存先オプションは併用できません")
	}
	home := a.Env("HOME")
	if a.OS == "windows" {
		home = a.Env("USERPROFILE")
	}
	exe := o.Executable
	if exe == "" {
		var candidates []string
		if a.OS == "darwin" {
			if o.Tool == "pgadmin" {
				candidates = []string{"/Applications/pgAdmin 4.app", filepath.Join(home, "Applications", "pgAdmin 4.app")}
			}
			if o.Tool == "dbeaver" {
				candidates = []string{"/Applications/DBeaver.app/Contents/MacOS/dbeaver", filepath.Join(home, "Applications/DBeaver.app/Contents/MacOS/dbeaver")}
			}
		} else {
			pf := a.Env("ProgramFiles")
			if pf != "" {
				switch o.Tool {
				case "pgadmin":
					candidates = []string{filepath.Join(pf, "pgAdmin 4")}
					more, _ := filepath.Glob(filepath.Join(pf, "pgAdmin 4", "v*"))
					candidates = append(candidates, more...)
				case "dbeaver":
					candidates = []string{filepath.Join(pf, "DBeaver", "dbeaver.exe")}
				case "a5":
					candidates = []string{filepath.Join(pf, "A5M2", "A5M2.exe")}
				}
			}
		}
		if o.Tool != "pgadmin" && a.LookPath != nil {
			name := map[string]string{"dbeaver": "dbeaver", "a5": "A5M2.exe"}[o.Tool]
			if p, e := a.LookPath(name); e == nil {
				candidates = append(candidates, p)
			}
		}
		seen := map[string]bool{}
		for _, p := range candidates {
			if _, e := os.Stat(p); e == nil {
				if o.Tool == "pgadmin" && a.OS == "windows" {
					if _, e := os.Stat(filepath.Join(p, "python/python.exe")); e != nil {
						continue
					}
					if _, e := os.Stat(filepath.Join(p, "web/setup.py")); e != nil {
						continue
					}
				}
				resolved, e := filepath.EvalSymlinks(p)
				if e == nil && !seen[resolved] {
					seen[resolved] = true
					exe = resolved
				}
			}
		}
		if len(seen) != 1 {
			return t, fmt.Errorf("%sの配置を一意に検出できません。--executableで指定してください（pgAdminはインストールフォルダー）", o.Tool)
		}
	}
	var err error
	exe, err = filepath.Abs(exe)
	if err != nil {
		return t, err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return t, fmt.Errorf("ツールが見つかりません。--executableを確認してください: %w", err)
	}
	t.Executable = exe
	switch o.Tool {
	case "dbeaver":
		if strings.HasSuffix(strings.ToLower(exe), ".app") {
			t.Executable = filepath.Join(exe, "Contents/MacOS/dbeaver")
		}
		root := filepath.Dir(t.Executable)
		if a.OS == "darwin" {
			root = filepath.Join(filepath.Dir(root), "Eclipse")
		}
		product, _, e := pathRead(filepath.Join(root, ".eclipseproduct"), 64<<10)
		if e != nil || !bytes.Contains(product, []byte("id=org.jkiss.dbeaver.product")) {
			return t, fmt.Errorf("DBeaver Communityの標準配置を確認できません。Portable・Store・PRO版は対象外です")
		}
		versionRE := regexp.MustCompile(`(?m)^version=([^\r\n]+)`)
		m := versionRE.FindSubmatch(product)
		if len(m) != 2 || string(m[1]) != "26.2.3" {
			return t, fmt.Errorf("直接登録の形式を確認したDBeaver版は26.2.3です。既存のファイル出力を利用してください")
		}
		t.Version = string(m[1])
		t.Store = o.DBeaverWorkspace
		if t.Store == "" {
			if a.OS == "darwin" {
				if home != "" {
					t.Store = filepath.Join(home, "Library/DBeaverData/workspace6")
				}
			} else if appdata := a.Env("APPDATA"); appdata != "" {
				t.Store = filepath.Join(appdata, "DBeaverData/workspace6")
			}
		}
		if t.Store == "" {
			return t, fmt.Errorf("--dbeaver-workspaceに利用中のworkspaceを指定してください")
		}
		t.Store, err = dbToolAbsolute(t.Store)
		t.Project = "General"
		if err == nil {
			err = realParents(filepath.Join(t.Store, t.Project, ".dbeaver"))
		}
		if err == nil {
			_, err = os.Stat(filepath.Join(t.Store, t.Project, ".dbeaver"))
		}
		if err != nil {
			return t, fmt.Errorf("DBeaverでGeneralプロジェクトを初期化し、終了してから実行してください: %w", err)
		}
	case "pgadmin":
		root := exe
		if a.OS == "darwin" {
			t.Python = filepath.Join(root, "Contents/Frameworks/Python.framework/Versions/Current/bin/python3")
			t.SetupScript = filepath.Join(root, "Contents/Resources/web/setup.py")
		} else {
			t.Python = filepath.Join(root, "python/python.exe")
			t.SetupScript = filepath.Join(root, "web/setup.py")
		}
		t.Python, err = filepath.EvalSymlinks(t.Python)
		if err != nil {
			return t, fmt.Errorf("pgAdmin同梱Pythonを検出できません: %w", err)
		}
		if err = dbToolRegular(t.SetupScript); err != nil {
			return t, err
		}
		web := filepath.Dir(t.SetupScript)
		v, _, e := pathRead(filepath.Join(web, "version.py"), 64<<10)
		if e != nil || !regexp.MustCompile(`(?m)^APP_RELEASE\s*=\s*9\s*$`).Match(v) || !regexp.MustCompile(`(?m)^APP_REVISION\s*=\s*18\s*$`).Match(v) {
			return t, fmt.Errorf("直接登録の互換性を確認したpgAdmin版は9.18です。ファイル出力を利用してください")
		}
		t.Version = "9.18"
		custom := []string{filepath.Join(web, "config_local.py")}
		if a.OS == "darwin" {
			custom = append(custom, "/Library/Preferences/pgadmin/config_system.py")
		} else if a.Env("CommonProgramFiles") != "" {
			custom = append(custom, filepath.Join(a.Env("CommonProgramFiles"), "pgadmin/config_system.py"))
		}
		for _, p := range custom {
			if _, e := os.Lstat(p); e == nil || !os.IsNotExist(e) {
				return t, fmt.Errorf("pgAdminの独自設定があるため直接登録できません。ファイル出力を利用してください")
			}
		}
		t.Store = o.PGAdminSQLite
		if t.Store == "" {
			if a.OS == "darwin" && home != "" {
				t.Store = filepath.Join(home, ".pgadmin/pgadmin4.db")
			} else if a.Env("APPDATA") != "" {
				t.Store = filepath.Join(a.Env("APPDATA"), "pgAdmin/pgadmin4.db")
			}
		}
		t.Store, err = dbToolAbsolute(t.Store)
		if err != nil {
			return t, err
		}
		if t.User == "" {
			t.User = "pgadmin4@pgadmin.org"
		}
		if strings.ContainsAny(t.User, "\r\n\x00") || len(t.User) > 256 {
			return t, fmt.Errorf("pgAdminユーザー名が不正です")
		}
		if err = dbToolRegular(t.Store); err != nil {
			return t, fmt.Errorf("初期化済みのpgAdmin設定DBを指定してください: %w", err)
		}
	case "a5":
		if a.OS != "windows" || a.Arch != "amd64" {
			return t, fmt.Errorf("A5への直接登録はWindows amd64の通常版が対象です。ファイル出力を利用してください")
		}
		if !strings.EqualFold(filepath.Base(exe), "A5M2.exe") || strings.Contains(strings.ToLower(exe), "windowsapps") {
			return t, fmt.Errorf("A5通常版のA5M2.exeを指定してください")
		}
		binary, err := pe.Open(exe)
		if err != nil {
			return t, fmt.Errorf("A5実行ファイルの形式を確認できません: %w", err)
		}
		machine := binary.Machine
		_ = binary.Close()
		if machine != pe.IMAGE_FILE_MACHINE_AMD64 {
			return t, fmt.Errorf("A5直接登録はamd64版A5M2.exeを指定してください")
		}
		portable, err := filepath.Glob(filepath.Join(filepath.Dir(exe), "Portable*"))
		if err != nil {
			return t, err
		}
		if len(portable) > 0 {
			return t, fmt.Errorf("A5のPortable/workspace構成は直接登録の対象外です")
		}
		for _, p := range []string{filepath.Join(filepath.Dir(exe), "Portable"), filepath.Join(filepath.Dir(exe), "A5M2.ini")} {
			if _, e := os.Lstat(p); e == nil {
				return t, fmt.Errorf("A5ポータブル構成は直接登録の対象外です")
			}
		}
		t.Store = `HKCU\Software\mmatsubara\A5M2(x64)\DataSources`
		t.Version = "Windows実機確認待ち"
	}
	if o.Tool != "pgadmin" {
		if err = dbToolRegular(t.Executable); err != nil {
			return t, err
		}
	}
	t.Identity = dbToolHash([]string{a.OS, t.Tool, t.Executable, t.Store, t.User, t.Project})
	return t, nil
}

// 読取り対象集合の全バイトを指紋化し、確認後の変更を拒否する。
func dbToolFiles(t dbToolTarget) ([]pathFile, error) {
	var paths []string
	switch t.Tool {
	case "pgadmin":
		paths = []string{t.Store, t.Store + "-wal", t.Store + "-shm", t.Store + "-journal"}
	case "dbeaver":
		dir := filepath.Join(t.Store, t.Project, ".dbeaver")
		var err error
		paths, err = filepath.Glob(filepath.Join(dir, "data-sources*.json"))
		if err != nil {
			return nil, err
		}
		paths = append(paths, filepath.Join(dir, "credentials-config.json"))
		if len(paths) > 128 {
			return nil, fmt.Errorf("DBeaverの設定ファイルが128個を超えています")
		}
	}
	sort.Strings(paths)
	var files []pathFile
	total := 0
	for _, p := range paths {
		f, e := pathReadChange(p, dbToolJSONLimit)
		if e != nil {
			return nil, e
		}
		total += len(f.Before)
		if total > 64<<20 {
			return nil, fmt.Errorf("設定ファイル全体が64MiBを超えています")
		}
		files = append(files, f)
	}
	return files, nil
}
func dbeaverConnection(p Profile) (map[string]any, error) {
	provider, driver, jdbc := "", "", ""
	switch {
	case strings.HasPrefix(p.DatabaseVersion, "POSTGRES"):
		provider, driver, jdbc = "postgresql", "postgres-jdbc", "jdbc:postgresql://127.0.0.1:"
	case strings.HasPrefix(p.DatabaseVersion, "MYSQL"):
		provider, driver, jdbc = "mysql", "mysql8", "jdbc:mysql://127.0.0.1:"
	case strings.HasPrefix(p.DatabaseVersion, "SQLSERVER"):
		provider, driver, jdbc = "sqlserver", "microsoft", "jdbc:sqlserver://127.0.0.1:"
	default:
		return nil, fmt.Errorf("このDB種別のDBeaver登録は未対応です")
	}
	jdbc += strconv.Itoa(p.Port)
	if provider == "sqlserver" {
		// JDBCの区切りを値から混入させない。曖昧な引用を推測しない。
		if strings.ContainsAny(p.Database, ";{}") {
			return nil, fmt.Errorf("SQL Serverの直接登録ではDB名に ; { } を使用できません。ファイル出力を利用してください")
		}
		jdbc += ";databaseName=" + p.Database
	} else {
		jdbc += "/" + url.PathEscape(p.Database)
	}
	config := map[string]any{"host": "127.0.0.1", "port": strconv.Itoa(p.Port), "database": p.Database, "user": p.Username, "url": jdbc, "type": "dev", "auth-model": "native", "configurationType": "MANUAL"}
	if provider == "postgresql" {
		config["provider-properties"] = map[string]string{"sslmode": "disable"}
	}
	return map[string]any{"provider": provider, "driver": driver, "name": dbToolName(p), "save-password": false, "configuration": config}, nil
}
func dbeaverDisposition(plan *dbToolRegistrationPlan) (string, error) {
	expected, err := dbeaverConnection(plan.Profile)
	if err != nil {
		return "", err
	}
	var found int
	for _, f := range plan.Files {
		if !f.Exists || !strings.HasPrefix(filepath.Base(f.Path), "data-sources") {
			continue
		}
		var doc struct {
			Connections map[string]map[string]any `json:"connections"`
		}
		if err = json.Unmarshal(f.Before, &doc); err != nil || doc.Connections == nil {
			return "", fmt.Errorf("DBeaverの設定形式を確認できません: %s", f.Path)
		}
		for id, c := range doc.Connections {
			if id != plan.ID && c["name"] != plan.Name {
				continue
			}
			found++
			cfg, _ := c["configuration"].(map[string]any)
			want := expected["configuration"].(map[string]any)
			// Userが暗号化領域にしかない場合は同じと断定せず、復号もしない。
			for _, key := range []string{"host", "port", "database", "user", "url"} {
				if fmt.Sprint(cfg[key]) != fmt.Sprint(want[key]) {
					return "conflict", nil
				}
			}
			if c["provider"] != expected["provider"] || c["driver"] != expected["driver"] {
				return "conflict", nil
			}
		}
	}
	if found == 1 {
		return "already-present", nil
	}
	if found > 1 {
		return "conflict", nil
	}
	for _, f := range plan.Files {
		if f.Exists && filepath.Base(f.Path) == "data-sources-"+plan.ID+".json" {
			return "conflict", nil
		}
	}
	return "add", nil
}
func (a *App) planDBToolRegistration(ctx context.Context, p Profile, o dbToolOptions) (*dbToolRegistrationPlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateProfile(p); err != nil {
		return nil, err
	}
	t, err := a.detectDBTool(o)
	if err != nil {
		return nil, err
	}
	plan := &dbToolRegistrationPlan{Profile: p, Target: t, Name: dbToolName(p), Fingerprint: dbToolHash(p), Options: o}
	plan.ID = "csql-" + dbToolHash([]string{t.Identity, p.ConnectionName, p.Name})[:24]
	switch t.Tool {
	case "pgadmin":
		plan.Data, err = profileExport(p, "pgadmin")
		plan.Disposition = "needs-inspection"
	case "dbeaver":
		var conn map[string]any
		conn, err = dbeaverConnection(p)
		if err == nil {
			plan.Data, err = json.MarshalIndent(map[string]any{"connections": map[string]any{plan.ID: conn}}, "", "  ")
		}
	case "a5":
		var name, conn string
		name, conn, err = a5Connection(p)
		plan.Name = name
		plan.Data = []byte(conn)
		plan.Disposition = "needs-inspection"
	}
	if err != nil {
		return nil, err
	}
	if t.Tool == "a5" {
		var raw []byte
		raw, err = a.readA5Settings()
		plan.Files = []pathFile{{Path: t.Store, Before: raw, Exists: err == nil}}
	} else {
		plan.Files, err = dbToolFiles(t)
	}
	if err != nil {
		return nil, err
	}
	if t.Tool == "a5" {
		plan.Disposition = a5Disposition(plan.Files[0].Before, plan.Name, string(plan.Data))
		if plan.Disposition == "unsupported" {
			return nil, fmt.Errorf("A5の登録保存形式を確認できません。空の保存先・未知の値形式では直接登録しません。ファイル出力を利用してください")
		}
	}
	plan.BeforeDigest = dbToolHash(plan.Files)
	if t.Tool == "dbeaver" {
		plan.Disposition, err = dbeaverDisposition(plan)
	}
	return plan, err
}
func (a *App) dbToolPlanText(p *dbToolRegistrationPlan) string {
	disposition := map[string]string{"add": "追加予定", "already-present": "同じ設定を登録済み（変更なし）", "conflict": "同名/IDの設定と競合。ツール側で変更するか別の保存名を使用", "needs-inspection": "確認後にスキーマ・重複を検査"}[p.Disposition]
	text := fmt.Sprintf("ツール: %s / %s\n登録先: %s\n登録名: %s\nHost: 127.0.0.1  Port: %d\nDB: %s  User: %s\n判定: %s\n対象ツールを終了してください。パスワードは渡さず、DB接続は開始しません。\nバックアップ: %s\n", p.Target.Tool, p.Target.Version, p.Target.Store, p.Name, p.Profile.Port, p.Profile.Database, p.Profile.Username, disposition, filepath.Join(a.Paths.ConfigDir, "db-tool-backups", p.Target.Tool))
	if p.Target.Tool == "pgadmin" {
		text += "pgAdmin所有ユーザー: " + p.Target.User + "\n"
	}
	if p.Target.Tool == "dbeaver" {
		text += "専用追加JSONを保存します。DBeaverでの読込みは次回起動後に確認してください。\n"
	}
	return safeLog(text)
}
func dbToolResultText(r dbToolRegistrationResult, err error) string {
	s := map[string]string{"saved": "追加JSONを保存・照合しました。DBeaverを起動し、Generalで接続設定を確認してください（アプリでの読込みは未検証）", "registered": "登録先の接続設定を照合しました", "already-present": "同じ設定を登録済みです。変更はありません", "cancelled": "登録を取り消しました", "failed": "登録を開始できませんでした", "unknown": "登録要求後の結果を確認できません。ツール側を確認し、再送前に重複を確認してください", "conflict": "既存設定と競合しています。ツール側で変更するか別の保存名を使用してください"}[r.Status]
	if r.Name != "" {
		s += "\n登録名: " + r.Name
	}
	if r.Target != "" {
		s += "\n登録先: " + r.Target
	}
	if r.Backup != "" {
		s += "\nバックアップ: " + r.Backup + "\n復元手順: " + filepath.Join(r.Backup, "復元手順.txt")
	}
	if r.Detail != "" {
		s += "\n" + r.Detail
	}
	if err != nil {
		s += "\n" + err.Error()
	}
	return safeLog(s)
}

func (a *App) profileRegisterCommand() *cobra.Command {
	var o dbToolOptions
	var dry bool
	c := &cobra.Command{Use: "register NAME", Short: "保存済み設定をDBツールへ直接登録", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		rows, err := a.LoadProfiles()
		if err != nil {
			return err
		}
		p, err := findProfile(rows, args[0])
		if err != nil {
			return err
		}
		plan, err := a.planDBToolRegistration(c.Context(), p, o)
		if err != nil {
			return err
		}
		fmt.Fprintln(a.stdout(), a.dbToolPlanText(plan))
		if dry {
			return nil
		}
		if !a.TTY {
			return fmt.Errorf("実登録は対話端末で実行してください。確認だけなら--dry-runを使用してください")
		}
		if plan.Disposition == "conflict" {
			return fmt.Errorf("既存設定との競合を解消してください")
		}
		if plan.Disposition == "already-present" {
			fmt.Fprintln(a.stdout(), "同じ設定を登録済みです。変更はありません")
			return nil
		}
		yes, err := a.confirm(c.Context(), "表示した登録先へ追加しますか？ [y/N]")
		if err != nil {
			return err
		}
		if !yes {
			fmt.Fprintln(a.stdout(), "登録を取り消しました")
			return nil
		}
		result, err := a.applyDBToolRegistration(c.Context(), plan)
		fmt.Fprintln(a.stdout(), dbToolResultText(result, err))
		return err
	}}
	c.Flags().StringVar(&o.Tool, "tool", "", "対象ツール（pgadmin|dbeaver|a5、必須）")
	c.Flags().StringVar(&o.Executable, "executable", "", "実行ファイル（pgAdminはインストールフォルダー）")
	c.Flags().StringVar(&o.PGAdminSQLite, "pgadmin-sqlite", "", "pgAdminの初期化済み設定DB")
	c.Flags().StringVar(&o.PGAdminUser, "pgadmin-user", "", "pgAdmin所有ユーザー（既定pgadmin4@pgadmin.org）")
	c.Flags().StringVar(&o.DBeaverWorkspace, "dbeaver-workspace", "", "DBeaverのworkspace（Generalへ追加）")
	c.Flags().BoolVar(&dry, "dry-run", false, "外部起動・書込みなしで対象と内容を確認")
	return c
}

// 標準出力と標準エラーを別々に制限し、外部ツールの出力や秘密値を画面へ流さない。
type dbToolBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (b *dbToolBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := dbToolOutputLimit - b.Len()
	if remaining < len(p) {
		b.exceeded = true
		p = p[:max(0, remaining)]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}
func (a *App) runDBTool(ctx context.Context, exe string, args ...string) ([]byte, error) {
	if a.DBToolRun != nil {
		return a.DBToolRun(ctx, exe, args)
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.WaitDelay = 2 * time.Second
	var out, errout dbToolBuffer
	cmd.Stdout, cmd.Stderr = &out, &errout
	err := cmd.Run()
	if out.exceeded || errout.exceeded {
		return nil, fmt.Errorf("外部ツールの出力が64KiBを超えました")
	}
	if err != nil {
		return nil, fmt.Errorf("外部ツールの処理を完了できませんでした（詳細出力は秘密情報保護のため非表示）: %w", err)
	}
	return out.Bytes(), nil
}
func (a *App) dbToolIsRunning(ctx context.Context, t dbToolTarget) (bool, error) {
	if a.DBToolRunning != nil {
		return a.DBToolRunning(ctx, t)
	}
	return dbToolRunning(ctx, t)
}

// pendingを原本書込みより先に永続化する。結果不明の要求は自動再送しない。
func (a *App) dbToolReceiptFile() string {
	return filepath.Join(a.Paths.ConfigDir, "db-tool-registrations.json")
}
func (a *App) readDBToolReceipt() (dbToolReceipt, error) {
	r := dbToolReceipt{Version: 1, Entries: map[string]dbToolRegistrationResult{}}
	b, _, err := pathRead(a.dbToolReceiptFile(), profileLimit)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if json.Unmarshal(b, &r) != nil || r.Version != 1 || r.Entries == nil {
		return r, fmt.Errorf("登録履歴が不正です。自動再送せず登録先を確認してください")
	}
	return r, nil
}
func (a *App) writeDBToolReceipt(r dbToolReceipt) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(b) > profileLimit {
		return fmt.Errorf("登録履歴が1MiBを超えています")
	}
	return dbToolWriteState(a.dbToolReceiptFile(), b)
}
func (a *App) applyDBToolRegistration(ctx context.Context, p *dbToolRegistrationPlan) (r dbToolRegistrationResult, retErr error) {
	r = dbToolRegistrationResult{Status: "failed", Name: p.Name, Target: p.Target.Store}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	// 保存設定の変更と登録を同じ排他内で再検査する。外部アプリには排他を強制しない。
	unlock, err := a.lockProfileStore(ctx)
	if err != nil {
		return r, err
	}
	defer unlock()
	profiles, err := a.LoadProfiles()
	if err != nil {
		return r, err
	}
	current, err := findProfile(profiles, p.Profile.Name)
	if err != nil || dbToolHash(current) != p.Fingerprint {
		return r, fmt.Errorf("保存設定が変更されました。対象を選び直してください")
	}
	fresh, err := a.planDBToolRegistration(ctx, current, p.Options)
	if err != nil {
		return r, err
	}
	if fresh.Target.Identity != p.Target.Identity || fresh.BeforeDigest != p.BeforeDigest {
		return r, fmt.Errorf("確認中に登録先が変更されました。内容を確認し直してください")
	}
	running, err := a.dbToolIsRunning(ctx, p.Target)
	if err != nil {
		return r, fmt.Errorf("対象ツールの起動状態を確認できません: %w", err)
	}
	if running {
		return r, fmt.Errorf("対象ツールを終了してから実行してください。自動終了は行いません")
	}
	receipt, err := a.readDBToolReceipt()
	if err != nil {
		return r, err
	}
	old, pending := receipt.Entries[p.ID]
	pending = pending && old.Status == "unknown"
	if pending {
		r.Backup = old.Backup
	}
	reconcile := func() (dbToolRegistrationResult, error) {
		r.Status = "already-present"
		r.Verified = p.Target.Tool != "dbeaver"
		receipt.Entries[p.ID] = r
		if pending {
			return r, a.writeDBToolReceipt(receipt)
		}
		return r, nil
	}
	refusePending := func() (dbToolRegistrationResult, error) {
		r.Status = "unknown"
		return r, fmt.Errorf("前回の登録結果が未確認です。登録先と履歴 %s を確認してください。自動再送しません", a.dbToolReceiptFile())
	}
	if pending && p.Target.Tool != "pgadmin" && fresh.Disposition != "already-present" {
		return refusePending()
	}
	if fresh.Disposition == "conflict" {
		r.Status = "conflict"
		return r, fmt.Errorf("同名またはIDの異なる設定があります")
	}
	if fresh.Disposition == "already-present" {
		return reconcile()
	}
	if err = secureDir(filepath.Join(a.Paths.ConfigDir, "db-tool-backups", p.Target.Tool)); err != nil {
		return r, err
	}
	backup, err := os.MkdirTemp(filepath.Join(a.Paths.ConfigDir, "db-tool-backups", p.Target.Tool), a.Now().UTC().Format("20060102T150405Z")+"-")
	if err != nil {
		return r, err
	}
	r.Backup = backup
	if err = dbToolPrivate(backup, true); err != nil {
		return r, err
	}
	if err = dbToolWriteNew(filepath.Join(backup, "復元手順.txt"), []byte(dbToolRestoreText(p))); err != nil {
		return r, err
	}

	if err = secureDir(filepath.Join(a.Paths.CacheDir, "db-tool-register")); err != nil {
		return r, err
	}
	work, err := os.MkdirTemp(filepath.Join(a.Paths.CacheDir, "db-tool-register"), "operation-")
	if err != nil {
		return r, err
	}
	defer os.RemoveAll(work)
	if err = dbToolPrivate(work, true); err != nil {
		return r, err
	}
	var before map[string]json.RawMessage
	if p.Target.Tool == "pgadmin" {
		before, err = a.pgAdminSnapshot(ctx, p.Target, work, filepath.Join(backup, "pgadmin4.db"))
		if err != nil {
			return r, err
		}
		disposition := pgAdminDisposition(before, p)
		if disposition == "already-present" {
			return reconcile()
		}
		if pending {
			return refusePending()
		}
		if disposition == "conflict" {
			r.Status = disposition
			return r, fmt.Errorf("同名の設定と競合しています")
		}
	} else {
		for i, f := range p.Files {
			if f.Exists {
				name := filepath.Base(f.Path)
				if p.Target.Tool == "a5" {
					name = "registry.json"
				}
				if err = dbToolWriteNew(filepath.Join(backup, fmt.Sprintf("%03d-%s", i, name)), f.Before); err != nil {
					return r, err
				}
			}
		}
	}
	// バックアップ中の別アプリによる更新も、pending前に再検査する。
	latest, err := a.planDBToolRegistration(ctx, current, p.Options)
	if err != nil {
		return r, err
	}
	if latest.BeforeDigest != p.BeforeDigest {
		return r, fmt.Errorf("バックアップ中に登録先が変わりました。追加していません")
	}
	running, err = a.dbToolIsRunning(ctx, p.Target)
	if err != nil {
		return r, err
	}
	if running {
		return r, fmt.Errorf("対象ツールが起動しました。終了してから内容を確認し直してください")
	}
	if err = ctx.Err(); err != nil {
		return r, err
	}
	r.Status = "unknown"
	receipt.Entries[p.ID] = r
	if err = a.writeDBToolReceipt(receipt); err != nil {
		r.Status = "failed"
		return r, err
	}
	// 要求後のキャンセルや履歴保存失敗でも、副作用の事実を結果から消さない。
	defer func() {
		receipt.Entries[p.ID] = r
		if e := a.writeDBToolReceipt(receipt); e != nil {
			r.Detail += "\n履歴更新に失敗しました。次回は登録先を先に確認してください"
			retErr = errors.Join(retErr, e)
		}
	}()
	if err = ctx.Err(); err != nil {
		r.Status = "cancelled"
		return r, err
	}
	switch p.Target.Tool {
	case "dbeaver":
		path := filepath.Join(p.Target.Store, p.Target.Project, ".dbeaver", "data-sources-"+p.ID+".json")
		if err = dbToolWriteNew(path, p.Data); err != nil {
			return r, err
		}
		r.Committed = true
		data, _, e := pathRead(path, dbToolJSONLimit)
		if e != nil || !bytes.Equal(data, p.Data) {
			return r, fmt.Errorf("保存後の追加JSONを照合できません")
		}
		r.Status = "saved"
		r.Detail = "保存先: " + path
	case "pgadmin":
		input := filepath.Join(work, "servers.json")
		if err = dbToolWriteNew(input, p.Data); err != nil {
			r.Status = "failed"
			return r, err
		}
		r.Submitted = true
		err = a.pgAdminSetup(ctx, p.Target, work, "load-servers", input, "--sqlite-path", p.Target.Store, "--user", p.Target.User)
		if err != nil {
			return r, err
		}
		var after map[string]json.RawMessage
		after, err = a.pgAdminSnapshot(ctx, p.Target, work, filepath.Join(work, "after.db"))
		if err != nil {
			return r, err
		}
		if pgAdminDisposition(after, p) != "already-present" || !pgAdminPreserved(before, after) {
			return r, fmt.Errorf("要求後の接続設定・既存設定の保持を確認できません")
		}
		r.Status, r.Verified = "registered", true
	case "a5":
		r.Submitted, err = a.startA5Registration(p.Target, p.Name, string(p.Data))
		if err != nil {
			if !r.Submitted {
				r.Status = "failed"
			}
			return r, err
		}
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			raw, e := a.readA5Settings()
			if e == nil {
				status := a5Disposition(raw, p.Name, string(p.Data))
				if status == "already-present" {
					if !a5Preserved(p.Files[0].Before, raw) {
						return r, fmt.Errorf("A5の既存設定の保持を照合できません")
					}
					r.Status, r.Verified = "registered", true
					break
				}
				if status == "conflict" {
					return r, fmt.Errorf("A5登録先が異なる内容です")
				}
			}
			select {
			case <-ctx.Done():
				return r, ctx.Err()
			case <-ticker.C:
			}
		}
	}
	// パスの選択は端末固有。PC移行ファイルには含めない。
	if err = dbToolWriteOptions(filepath.Join(a.Paths.ConfigDir, "db-tools.json"), p.Options); err != nil {
		r.Detail += "\n登録先の既定値を保存できませんでした"
		return r, err
	}
	return r, nil
}

// Pythonのコードは固定し、パスやユーザー名はargvだけで渡す。原本DBの初期化・移行は禁止する。
const pgAdminSnapshotPython = `import sys,sqlite3,json,base64,pathlib,os
os.umask(0o077)
src,dst,user=sys.argv[1:4]
con=sqlite3.connect(pathlib.Path(src).as_uri()+'?mode=ro',uri=True,timeout=5)
if con.execute("select value from version where name='ConfigDB'").fetchone() != (52,): raise RuntimeError('unsupported schema')
if con.execute('select count(*) from user where email=?',(user,)).fetchone()[0]!=1: raise RuntimeError('ambiguous user')
copy=sqlite3.connect(dst);con.backup(copy);copy.close();con.close()
print('{}')
`
const pgAdminSetupPython = `import os,sys,runpy,pathlib
os.umask(0o077)
setup,work=sys.argv[1:3]
config=pathlib.Path(work)/'config_distro.py'
config.write_text('SERVER_MODE=False\nDATA_DIR='+repr(str(pathlib.Path(work)/'data'))+'\nUSE_OS_SECRET_STORAGE=False\n',encoding='utf-8')
systemroot=os.environ.get('SystemRoot','C:\\Windows')
os.environ.clear()
os.environ.update({'HOME':work,'USERPROFILE':work,'APPDATA':work,'LOCALAPPDATA':work,'TMPDIR':work,'TMP':work,'TEMP':work,'XDG_CONFIG_HOME':work,'XDG_CACHE_HOME':work,'XDG_DATA_HOME':work,'PATH':os.path.dirname(sys.executable)+os.pathsep+os.path.join(systemroot,'System32'),'SystemRoot':systemroot,'CONFIG_DISTRO_FILE_PATH':str(config),'PYTHON_KEYRING_BACKEND':'keyring.backends.null.Keyring','LANG':'en_US.UTF-8'})
sys.path.insert(0,str(pathlib.Path(setup).parent))
sys.argv=[setup]+sys.argv[3:]
runpy.run_path(setup,run_name='__main__')
`

func (a *App) pgAdminSetup(ctx context.Context, t dbToolTarget, work string, args ...string) error {
	argv := append([]string{"-I", "-B", "-c", pgAdminSetupPython, t.SetupScript, work}, args...)
	_, err := a.runDBTool(ctx, t.Python, argv...)
	return err
}
func (a *App) pgAdminSnapshot(ctx context.Context, t dbToolTarget, work, dest string) (map[string]json.RawMessage, error) {
	if _, err := a.runDBTool(ctx, t.Python, "-I", "-B", "-c", pgAdminSnapshotPython, t.Store, dest, t.User); err != nil {
		return nil, fmt.Errorf("pgAdmin設定DBのschema52・ユーザー・バックアップを確認できません: %w", err)
	}
	if err := dbToolPrivate(dest, false); err != nil {
		return nil, err
	}
	output := dest + ".json"
	if err := a.pgAdminSetup(ctx, t, work, "dump-servers", output, "--sqlite-path", dest, "--user", t.User); err != nil {
		return nil, err
	}
	if err := dbToolPrivate(output, false); err != nil {
		return nil, err
	}
	b, _, err := pathRead(output, dbToolJSONLimit)
	if err != nil {
		return nil, err
	}
	var doc struct{ Servers map[string]json.RawMessage }
	if json.Unmarshal(b, &doc) != nil || doc.Servers == nil {
		return nil, fmt.Errorf("pgAdminの登録一覧を読み取れません")
	}
	return doc.Servers, nil
}
func pgAdminDisposition(rows map[string]json.RawMessage, p *dbToolRegistrationPlan) string {
	count := 0
	for _, raw := range rows {
		var s map[string]any
		if json.Unmarshal(raw, &s) != nil {
			return "conflict"
		}
		if s["Name"] != p.Name {
			continue
		}
		count++
		if s["Host"] != "127.0.0.1" || s["Port"] != float64(p.Profile.Port) || s["MaintenanceDB"] != p.Profile.Database || s["Username"] != p.Profile.Username {
			return "conflict"
		}
	}
	if count == 1 {
		return "already-present"
	}
	if count > 1 {
		return "conflict"
	}
	return "add"
}
func pgAdminPreserved(before, after map[string]json.RawMessage) bool {
	if len(after) != len(before)+1 {
		return false
	}
	for id, b := range before {
		var v, w any
		if json.Unmarshal(b, &v) != nil || json.Unmarshal(after[id], &w) != nil || dbToolHash(v) != dbToolHash(w) {
			return false
		}
	}
	return true
}

// A5の文字列は既存エクスポートと共有し、追加プロパティを許さない。
func a5Connection(p Profile) (string, string, error) {
	if strings.ContainsAny(p.Database+p.Username, ";\"") {
		return "", "", fmt.Errorf("A5形式ではDB名・ユーザー名にセミコロンや二重引用符を含められません。DBeaver形式またはURIを使用してください")
	}
	provider := ""
	switch {
	case strings.HasPrefix(p.DatabaseVersion, "POSTGRES"):
		provider = "PostgreSQL"
	case strings.HasPrefix(p.DatabaseVersion, "MYSQL"):
		provider = "MySQL"
	case strings.HasPrefix(p.DatabaseVersion, "SQLSERVER"):
		provider = "SQL Server"
	default:
		return "", "", fmt.Errorf("このDB種別のA5出力は未対応です")
	}
	name := "csql/" + p.Project + "/" + p.Name
	data := fmt.Sprintf("__ConnectionType=Internal;ProviderName=%s;ServerName=127.0.0.1;Port=%d;Database=%s;UserName=%s;", provider, p.Port, p.Database, p.Username)
	if provider == "PostgreSQL" {
		data += "ProtocolVersion=30;SSLMode=Disable;"
	}
	return name, data, nil
}

// レジストリの文字列以外の値も原形で退避する。秘密値は表示・復号しない。
type a5RegistryValue struct {
	Type uint32
	Data []byte
}
type a5RegistrySnapshot struct{ Values map[string]a5RegistryValue }

func a5Disposition(raw []byte, name, connection string) string {
	var s a5RegistrySnapshot
	if json.Unmarshal(raw, &s) != nil || len(s.Values) == 0 {
		return "unsupported"
	}
	// 未実測の保存形式を推測で書き換えない。対象名以外の値も構造を確認する。
	for _, v := range s.Values {
		text, err := a5RegistryString(v)
		if err != nil || !validA5Connection(text) {
			return "unsupported"
		}
	}
	found := false
	for n, v := range s.Values {
		if !strings.EqualFold(n, name) {
			continue
		}
		found = true
		text, err := a5RegistryString(v)
		if err != nil || text != connection {
			return "conflict"
		}
	}
	if found {
		return "already-present"
	}
	return "add"
}
func a5Preserved(before, after []byte) bool {
	var b, a a5RegistrySnapshot
	if json.Unmarshal(before, &b) != nil || json.Unmarshal(after, &a) != nil || len(a.Values) != len(b.Values)+1 {
		return false
	}
	for k, v := range b.Values {
		if dbToolHash(v) != dbToolHash(a.Values[k]) {
			return false
		}
	}
	return true
}

// コンパイル時にもioの上限付き読取りを固定する。
var _ io.Writer = (*dbToolBuffer)(nil)

// バックアップは改行等を加えず、既存ファイルを置換せずに確定する。
func dbToolWriteNew(path string, b []byte) error {
	if err := realParents(filepath.Dir(path)); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".csql-register-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = dbToolPrivate(f.Name(), false); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// 完成済みファイルを排他的に公開する。既存名への上書きと部分JSONの露出を防ぐ。
	return os.Link(f.Name(), path)
}
func dbToolWriteOptions(path string, o dbToolOptions) error {
	b, err := json.Marshal(o)
	if err != nil {
		return err
	}
	return dbToolWriteState(path, b)
}
func dbToolWriteState(path string, b []byte) error {
	if err := secureDir(filepath.Dir(path)); err != nil {
		return err
	}
	if err := pathNoLinks(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".csql-register-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = dbToolPrivate(f.Name(), false); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

func (a *App) readA5Settings() ([]byte, error) {
	if a.DBToolA5Read != nil {
		return a.DBToolA5Read()
	}
	return readA5Settings()
}
func (a *App) startA5Registration(t dbToolTarget, name, conn string) (bool, error) {
	if a.DBToolA5Start != nil {
		return a.DBToolA5Start(t, name, conn)
	}
	return startA5Registration(t, name, conn)
}
func a5RegistryString(v a5RegistryValue) (string, error) {
	if v.Type != 1 || len(v.Data) < 2 || len(v.Data)%2 != 0 {
		return "", fmt.Errorf("A5設定値の型が不正です")
	}
	u := make([]uint16, len(v.Data)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(v.Data[i*2:])
	}
	if u[len(u)-1] != 0 {
		return "", fmt.Errorf("A5設定値の終端が不正です")
	}
	for _, c := range u[:len(u)-1] {
		if c == 0 {
			return "", fmt.Errorf("A5設定値に途中の終端があります")
		}
	}
	return string(utf16.Decode(u[:len(u)-1])), nil
}
func validA5Connection(s string) bool {
	if strings.ContainsFunc(s, unicode.IsControl) {
		return false
	}
	values := map[string]string{}
	for _, part := range strings.Split(strings.TrimSuffix(s, ";"), ";") {
		key, value, ok := strings.Cut(part, "=")
		if !ok || key == "" {
			return false
		}
		key = strings.ToLower(key)
		if _, exists := values[key]; exists {
			return false
		}
		values[key] = value
	}
	port, err := strconv.Atoi(values["port"])
	provider := values["providername"]
	return err == nil && port > 0 && port <= 65535 && values["__connectiontype"] == "Internal" && (provider == "PostgreSQL" || provider == "MySQL" || provider == "SQL Server") && values["servername"] != "" && values["database"] != "" && values["username"] != ""
}

// 復元は人の判断で行う。csqlは自動復元や既存設定の削除を実行しない。
func dbToolRestoreText(p *dbToolRegistrationPlan) string {
	text := "DBツール設定の手動復元\n\n対象: " + p.Target.Tool + "\n登録先: " + p.Target.Store + "\n\n対象ツールとcsqlを終了してください。別端末のProxy停止は不要です。\n現在の設定一式を先に別の私有フォルダーへ退避し、バックアップと混在させないでください。バックアップは資格情報を含み得るため共有しないでください。\n復元はバックアップ時点へ戻す操作です。後から追加した設定は保持できない場合があります。判断できない場合は、原本を残したままツールの管理者へ相談してください。\n\n"
	switch p.Target.Tool {
	case "pgadmin":
		text += "pgadmin4.db がSQLite backup APIで作成した一貫した設定DBです。元の設定DBと同じ名前でコピーします。原本DBと同じ場所にある -wal / -shm / -journal も先に現在状態の退避先へ移動し、復元DBと古い補助ファイルを混在させないでください。pgadmin4.db.json は確認用の定義一覧で、SQLiteの代わりにはなりません。復元後、同じpgAdmin版で一覧を確認し、DBへは自動接続しないでください。\n"
	case "dbeaver":
		text += "csqlは既存ファイルを変更しません。追加だけを取り消す場合は、次の専用ファイルだけを別の私有フォルダーへ移動し、DBeaverを起動して確認してください。既存JSONやcredentials-config.jsonを削除しないでください。\n追加ファイル: " + filepath.Join(p.Target.Store, p.Target.Project, ".dbeaver", "data-sources-"+p.ID+".json") + "\n\n既存設定の復元が必要な場合だけ、次の対応でバックアップを元の名前へコピーしてください。\n"
		for i, f := range p.Files {
			if f.Exists {
				text += fmt.Sprintf("%03d-%s → %s\n", i, filepath.Base(f.Path), f.Path)
			}
		}
	case "a5":
		text += "000-registry.jsonはレジストリ値の型と生バイトを記録したJSONです。.regファイルとしてはインポートできません。追加だけを取り消す場合は、A5のDB登録一覧で対象名だけを手動削除してください。\n対象登録名: " + p.Name + "\n\n既存値の復元が必要な場合は、Windows PowerShellで次の手順を実行します。現在の同名値を上書きするため、先にreg exportで現在状態をこの私有フォルダー内の別名へ退避します。後から増えた別名は削除しません。\n\n" + a5RestoreInstructions
	}
	return text
}

const a5RestoreInstructions = `# 対象ツールを閉じ、バックアップフォルダーの絶対パスを入力する。
$restoreDir = Read-Host 'バックアップフォルダー'
$currentExport = Join-Path $restoreDir 'current-before-restore.reg'
if (Test-Path $currentExport) { throw '退避先が既存です。別名へ退避してください' }
reg export 'HKCU\Software\mmatsubara\A5M2(x64)\DataSources' $currentExport
if ($LASTEXITCODE -ne 0) { throw '現在状態の退避に失敗しました' }
$backup = Get-Content -LiteralPath (Join-Path $restoreDir '000-registry.json') -Raw | ConvertFrom-Json
$values = @($backup.Values.PSObject.Properties)
if ($values.Count -eq 0) { throw '復元対象がありません' }
foreach ($value in $values) {
 if ($value.Value.Type -ne 1) { throw '未対応の値型です。変更せず管理者へ確認してください' }
}
Write-Host ('復元する既存値: ' + $values.Count + '件。後から増えた別名は削除しません。')
if ((Read-Host '実行する場合だけRESTOREを入力') -cne 'RESTORE') { throw '取消しました' }
$registryKey = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Software\mmatsubara\A5M2(x64)\DataSources', $true)
if ($null -eq $registryKey) { throw '指定した通常モードの登録先がありません' }
try {
 foreach ($value in $values) {
  $bytes = [Convert]::FromBase64String($value.Value.Data)
  $text = [Text.Encoding]::Unicode.GetString($bytes).TrimEnd([char]0)
  $registryKey.SetValue($value.Name, $text, [Microsoft.Win32.RegistryValueKind]::String)
 }
} finally { $registryKey.Dispose() }
Write-Host 'A5を起動し既存定義を確認してください。DB接続は手動です。'
`
