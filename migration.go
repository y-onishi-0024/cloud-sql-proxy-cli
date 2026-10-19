package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const migrationLimit = 2 << 20

type migrationDocument struct {
	Format      string    `json:"format"`
	Version     int       `json:"version"`
	CreatedAt   string    `json:"createdAt"`
	CSQLVersion string    `json:"csqlVersion"`
	Profiles    []Profile `json:"profiles"`
}
type migrationPreview struct {
	Source                     string
	Before                     pathFile
	Existing, Incoming, Merged []Profile
	Added, Identical           []Profile
	Conflicts                  []string
}
type migrationResult struct {
	Path, Backup     string
	Added, Identical int
	Committed        bool
}

func (a *App) prepareMigrationExport() ([]byte, int, error) {
	rows, err := a.LoadProfiles()
	if err != nil {
		return nil, 0, err
	}
	if rows == nil {
		rows = []Profile{}
	}
	data, err := json.MarshalIndent(migrationDocument{"csql-migration", 1, a.Now().UTC().Format(time.RFC3339), version, rows}, "", "  ")
	if len(data) > migrationLimit {
		return nil, 0, fmt.Errorf("移行ファイルが2MiBを超えます")
	}
	return data, len(rows), err
}
func (a *App) defaultMigrationOutput() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	base := filepath.Join(dir, "csql-migration-"+a.Now().UTC().Format("20060102T150405Z"))
	for i := 1; i <= 1000; i++ {
		path := base + ".json"
		if i > 1 {
			path = fmt.Sprintf("%s-%d.json", base, i)
		}
		if _, err = os.Lstat(path); os.IsNotExist(err) {
			return path, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("出力先の同名ファイルが多すぎます。--outputで別の保存先を指定してください")
}
func (a *App) exportMigration(ctx context.Context, path string, data []byte, count int) (migrationResult, error) {
	var result migrationResult
	abs, err := filepath.Abs(path)
	if err != nil {
		return result, err
	}
	result.Path = abs
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if len(data) > migrationLimit {
		return result, fmt.Errorf("移行ファイルが2MiBを超えます")
	}
	if err = writeProfileExport(abs, data); err != nil {
		return result, fmt.Errorf("移行ファイルを出力できません。既存ファイルや親フォルダを確認してください: %w", err)
	}
	result.Added, result.Committed = count, true
	return result, nil
}

// 重複キーはstructへのdecodeで失われるため、深さを制限したトークン検査を先に行う。
func scanMigrationJSON(d *json.Decoder, depth int) error {
	if depth > 16 {
		return fmt.Errorf("JSONの入れ子が深すぎます")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		keys := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || keys[name] {
				return fmt.Errorf("JSONキーが重複しています")
			}
			keys[name] = true
			if err = scanMigrationJSON(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err = scanMigrationJSON(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("JSON構造が不正です")
	}
	_, err = d.Token()
	return err
}
func migrationKeys(data []byte, names ...string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil || len(fields) != len(names) {
		return nil, fmt.Errorf("移行JSONの項目が不正です")
	}
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			return nil, fmt.Errorf("移行JSONの項目が不正です")
		}
	}
	return fields, nil
}
func decodeMigration(data []byte) (migrationDocument, error) {
	var doc migrationDocument
	bad := fmt.Errorf("移行ファイルの形式が不正です。csql migrate exportで出力したファイルを確認してください")
	if len(data) > migrationLimit || !utf8.Valid(data) {
		return doc, bad
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if scanMigrationJSON(decoder, 0) != nil {
		return doc, bad
	}
	if _, err := decoder.Token(); err != io.EOF {
		return doc, bad
	}
	fields, err := migrationKeys(data, "format", "version", "createdAt", "csqlVersion", "profiles")
	if err != nil {
		return doc, bad
	}
	var raw []json.RawMessage
	if json.Unmarshal(fields["profiles"], &raw) != nil || raw == nil {
		return doc, bad
	}
	for _, r := range raw {
		profile, err := migrationKeys(r, "Name", "Instance", "Database", "Username", "Port", "PrivateIP")
		if err != nil {
			return doc, bad
		}
		if string(profile["PrivateIP"]) != "true" && string(profile["PrivateIP"]) != "false" {
			return doc, bad
		}
		if _, err = migrationKeys(profile["Instance"], "Project", "Name", "Region", "ConnectionName", "DatabaseVersion"); err != nil {
			return doc, bad
		}
	}
	if json.Unmarshal(data, &doc) != nil || doc.Format != "csql-migration" || doc.Version != 1 {
		return doc, bad
	}
	stamp, err := time.Parse(time.RFC3339, doc.CreatedAt)
	_, validVersion := parseReleaseVersion("v" + doc.CSQLVersion)
	if err != nil || !strings.HasSuffix(doc.CreatedAt, "Z") || stamp.Location() != time.UTC || !validVersion || len(doc.CSQLVersion) > 64 {
		return doc, bad
	}
	names, ports, instances := map[string]bool{}, map[int]string{}, map[string]int{}
	for _, p := range doc.Profiles {
		if err = validateProfile(p); err != nil {
			return doc, err
		}
		if names[p.Name] || (ports[p.Port] != "" && ports[p.Port] != p.ConnectionName) || (instances[p.ConnectionName] != 0 && instances[p.ConnectionName] != p.Port) {
			return doc, fmt.Errorf("移行ファイル内の名前・Port予約が矛盾しています")
		}
		names[p.Name] = true
		ports[p.Port] = p.ConnectionName
		instances[p.ConnectionName] = p.Port
	}
	return doc, nil
}
func readMigration(path string) ([]byte, error) {
	if err := realParents(filepath.Dir(path)); err != nil {
		return nil, err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > migrationLimit {
		return nil, fmt.Errorf("移行ファイルは2MiB以下の通常ファイルを指定してください")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(fi, actual) || !actual.Mode().IsRegular() {
		return nil, fmt.Errorf("読込み中に移行ファイルが変わりました")
	}
	data, err := io.ReadAll(io.LimitReader(f, migrationLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > migrationLimit {
		return nil, fmt.Errorf("移行ファイルが2MiBを超えます")
	}
	return data, nil
}
func (a *App) previewMigration(ctx context.Context, path string) (*migrationPreview, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	data, err := readMigration(abs)
	if err != nil {
		return nil, err
	}
	doc, err := decodeMigration(data)
	if err != nil {
		return nil, err
	}
	rows, before, err := a.loadProfilesSnapshot()
	if err != nil {
		return nil, err
	}
	preview := &migrationPreview{Source: abs, Before: before, Existing: rows, Incoming: doc.Profiles, Merged: append([]Profile{}, rows...)}
	for _, p := range doc.Profiles {
		identical, conflict := false, false
		for _, old := range rows {
			if old.Name == p.Name {
				if old == p {
					identical = true
					break
				}
				preview.Conflicts = append(preview.Conflicts, fmt.Sprintf("%s: 同名の保存済み設定と内容が異なります", p.Name))
				conflict = true
			} else if old.ConnectionName != p.ConnectionName && old.Port == p.Port {
				preview.Conflicts = append(preview.Conflicts, fmt.Sprintf("%s / %s: Port %d は %s の保存済み予約です", p.Name, p.ConnectionName, p.Port, old.Name))
				conflict = true
			} else if old.ConnectionName == p.ConnectionName && old.Port != p.Port {
				preview.Conflicts = append(preview.Conflicts, fmt.Sprintf("%s: 同じ接続先のPortが %d と %d で異なります（保存済み %s）", p.Name, p.Port, old.Port, old.Name))
				conflict = true
			}
		}
		if identical {
			preview.Identical = append(preview.Identical, p)
		} else if !conflict {
			preview.Added = append(preview.Added, p)
			preview.Merged = append(preview.Merged, p)
		}
	}
	sort.Slice(preview.Merged, func(i, j int) bool { return preview.Merged[i].Name < preview.Merged[j].Name })
	merged, err := json.Marshal(profileStore{1, preview.Merged})
	if err != nil {
		return nil, err
	}
	if len(merged) > profileLimit {
		return nil, fmt.Errorf("取込後の設定が1MiBを超えます。移行対象を減らしてください")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return preview, nil
}

// 保存障害だけをテストから再現し、製品の移行元や対象領域は差し替えない。
type migrationFileOps struct {
	Backup func(string, []byte) (string, error)
	Save   func([]Profile) error
}

func backupMigration(dir string, data []byte) (path string, resultErr error) {
	if err := realParents(dir); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "profiles.pre-import-*.json")
	if err != nil {
		return "", err
	}
	path = f.Name()
	defer func() {
		if resultErr != nil {
			_ = os.Remove(path)
			path = ""
		}
	}()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return path, err
}

// 確認した原本を排他下で再検査し、バックアップが完了してから全件を確定する。
func (a *App) applyMigration(ctx context.Context, p *migrationPreview) (migrationResult, error) {
	result := migrationResult{Path: filepath.Join(a.Paths.ConfigDir, "profiles.json")}
	if p == nil {
		return result, fmt.Errorf("取込内容を再確認してください")
	}
	result.Identical = len(p.Identical)
	if len(p.Conflicts) > 0 {
		return result, fmt.Errorf("競合があるため取り込みません。設定名・Portを修正して再確認してください")
	}
	if len(p.Added) == 0 {
		return result, nil
	}
	unlock, err := a.lockProfileStore(ctx)
	if err != nil {
		return result, err
	}
	defer unlock()
	_, now, err := a.loadProfilesSnapshot()
	if err != nil {
		return result, err
	}
	if now.Exists != p.Before.Exists || now.Mode != p.Before.Mode || !bytes.Equal(now.Before, p.Before.Before) {
		return result, fmt.Errorf("確認中に保存済み設定が変更されました。移行ファイルを指定して再確認してください")
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if now.Exists {
		backup := backupMigration
		if a.MigrationOps != nil && a.MigrationOps.Backup != nil {
			backup = a.MigrationOps.Backup
		}
		result.Backup, err = backup(a.Paths.ConfigDir, now.Before)
		if err != nil {
			return result, fmt.Errorf("バックアップできないため取り込みません: %w", err)
		}
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	save := a.writeProfiles
	if a.MigrationOps != nil && a.MigrationOps.Save != nil {
		save = a.MigrationOps.Save
	}
	if err = save(p.Merged); err != nil {
		return result, fmt.Errorf("設定を保存できませんでした。元の設定を保持しています: %w", err)
	}
	// 確定後の取消は成功を取り消せない。結果配送より実ファイルの確定を優先する。
	result.Added, result.Committed = len(p.Added), true
	return result, nil
}
func migrationPreviewText(p *migrationPreview) string {
	lines := []string{"移行元: " + p.Source, "取込先: " + p.Before.Path, fmt.Sprintf("追加: %d / 同一: %d / 競合: %d", len(p.Added), len(p.Identical), len(p.Conflicts)), "バックアップ先: " + filepath.Dir(p.Before.Path)}
	for _, r := range p.Added {
		lines = append(lines, fmt.Sprintf("追加: %s / %s / DB %s / User %s / Port %d", r.Name, r.ConnectionName, r.Database, r.Username, r.Port))
	}
	for _, r := range p.Identical {
		lines = append(lines, "同一: "+r.Name)
	}
	lines = append(lines, p.Conflicts...)
	if len(p.Conflicts) > 0 {
		lines = append(lines, "競合を解消して再確認してください。Portの変更: csql profile update NAME --port PORT", "名前や内容が異なる場合は移行元の設定を修正して再出力してください。部分取込や上書きはしません")
	}
	return safeLog(strings.Join(lines, "\n"))
}
func (a *App) migrationResultText(kind string, r migrationResult, err error) string {
	var lines []string
	if r.Committed {
		if kind == "export" {
			return safeLog(fmt.Sprintf("移行ファイルを出力しました: %s\n対象: %d件（プロジェクト・DBユーザー名を含みます。認証・パスワードは含みません）", r.Path, r.Added))
		}
		lines = append(lines, fmt.Sprintf("取り込みました: 追加 %d件 / 同一 %d件\n保存先: %s", r.Added, r.Identical, r.Path))
		if r.Backup == "" {
			lines = append(lines, "元の設定ファイルがない初回取込のため、バックアップはありません")
		}
		lines = append(lines, "新PCの準備: csql doctor / csql setup（不足ツールの導入は csql setup --install）", "Googleログインはdoctor/setupの案内に従って実行してください。認証は移行していません", "DBツールの設定は、新PCでCtrl+Eから必要な形式へ再出力してください")
	} else if err != nil {
		lines = append(lines, "移行処理は完了していません: "+err.Error())
	} else {
		lines = append(lines, fmt.Sprintf("変更なし（同一 %d件）", r.Identical))
	}
	if r.Backup != "" {
		lines = append(lines, "元設定のバックアップ: "+r.Backup, "復元する場合はcsqlを終了し、バックアップを次の設定ファイルへ戻してください: "+r.Path)
		if a.OS == "windows" {
			lines = append(lines, "Copy-Item -LiteralPath "+selfUpdatePSQuote(r.Backup)+" -Destination "+selfUpdatePSQuote(r.Path)+" -Force")
		} else {
			lines = append(lines, "cp "+shellQuote(r.Backup)+" "+shellQuote(r.Path))
		}
	}
	return safeLog(strings.Join(lines, "\n"))
}
func (a *App) migrationCommand() *cobra.Command {
	args := func(n int) cobra.PositionalArgs {
		return func(c *cobra.Command, v []string) error {
			if err := cobra.ExactArgs(n)(c, v); err != nil {
				return usageError{err}
			}
			return nil
		}
	}
	root := &cobra.Command{Use: "migrate", Short: "PC間の接続設定の出力・取込", Args: args(0), RunE: func(c *cobra.Command, _ []string) error { return c.Help() }}
	export := &cobra.Command{Use: "export", Short: "保存済み接続設定を移行ファイルへ出力", Args: args(0), RunE: func(c *cobra.Command, _ []string) error {
		path, _ := c.Flags().GetString("output")
		var err error
		if path == "" {
			path, err = a.defaultMigrationOutput()
			if err != nil {
				return err
			}
		}
		data, count, err := a.prepareMigrationExport()
		if err != nil {
			return err
		}
		result, err := a.exportMigration(c.Context(), path, data, count)
		if err != nil {
			return err
		}
		fmt.Fprintln(a.stdout(), a.migrationResultText("export", result, nil))
		return nil
	}}
	export.Flags().String("output", "", "保存先（省略時は起動フォルダへ日時付きJSON）")
	imp := &cobra.Command{Use: "import FILE", Short: "移行ファイルを確認して取込", Args: args(1), RunE: func(c *cobra.Command, v []string) error {
		dry, _ := c.Flags().GetBool("dry-run")
		if !dry && !a.TTY {
			return usageError{fmt.Errorf("実際の取込は対話端末から実行してください。確認のみは --dry-run を指定できます")}
		}
		p, err := a.previewMigration(c.Context(), v[0])
		if err != nil {
			return err
		}
		fmt.Fprintln(a.stdout(), migrationPreviewText(p))
		if len(p.Conflicts) > 0 {
			return fmt.Errorf("競合があるため取り込みません。設定名・Portを修正して再確認してください")
		}
		if len(p.Added) == 0 {
			fmt.Fprintf(a.stdout(), "変更なし（同一 %d件）\n", len(p.Identical))
			return nil
		}
		if dry {
			fmt.Fprintln(a.stdout(), "確認のみです。設定は変更していません")
			return nil
		}
		yes, err := a.confirm(c.Context(), "この内容で接続設定を取り込みますか？ [y/N]")
		if err != nil {
			return err
		}
		if !yes {
			fmt.Fprintln(a.stdout(), "取込を取り消しました")
			return nil
		}
		result, err := a.applyMigration(c.Context(), p)
		fmt.Fprintln(a.stdout(), a.migrationResultText("import", result, err))
		return err
	}}
	imp.Flags().Bool("dry-run", false, "追加・同一・競合を確認するだけで設定を変更しない")
	root.AddCommand(export, imp)
	return root
}
