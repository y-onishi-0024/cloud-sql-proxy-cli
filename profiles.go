package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"
)

type Profile struct {
	Name               string
	Instance           `json:"Instance"`
	Database, Username string
	Port               int
	PrivateIP          bool
}
type profileStore struct {
	Version  int
	Profiles []Profile
}

const profileLimit = 1 << 20

var profileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func validateProfile(p Profile) error {
	if !profileName.MatchString(p.Name) || !validInstance(p.Instance) || p.Port < 1 || p.Port > 65535 {
		return fmt.Errorf("プロファイル名・接続先・Portが不正です（名前は英数字で始まる64文字以内の英数字・_・-）")
	}
	for _, v := range []string{p.Database, p.Username} {
		if !utf8.ValidString(v) || strings.TrimSpace(v) == "" || utf8.RuneCountInString(v) > 256 || strings.ContainsFunc(v, unicode.IsControl) {
			return fmt.Errorf("DB名・ユーザー名は空でない256文字以内の文字列を指定してください（制御文字不可）")
		}
	}
	return nil
}

// 設定・出力の親にもリンクを許さず、意図しない場所への読書きを拒否する。
func realParents(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for p := abs; ; p = filepath.Dir(p) {
		fi, err := os.Lstat(p)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && (!fi.IsDir() || fi.Mode()&os.ModeSymlink != 0) {
			return fmt.Errorf("親ディレクトリが不正です: %s", p)
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return nil
}
func (a *App) LoadProfiles() ([]Profile, error) {
	rows, _, err := a.loadProfilesSnapshot()
	return rows, err
}

// 確認時の行と原本を同じ読込で捕捉し、移行の再検査とバックアップに渡す。
func (a *App) loadProfilesSnapshot() ([]Profile, pathFile, error) {
	file := filepath.Join(a.Paths.ConfigDir, "profiles.json")
	snapshot := pathFile{Path: file, Mode: 0600}
	if err := realParents(filepath.Dir(file)); err != nil {
		return nil, snapshot, err
	}
	fi, err := os.Lstat(file)
	if os.IsNotExist(err) {
		return nil, snapshot, nil
	}
	if err != nil {
		return nil, snapshot, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > profileLimit {
		return nil, snapshot, fmt.Errorf("設定が通常ファイルでないか1MiBを超えています: %s", file)
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, snapshot, err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil {
		return nil, snapshot, err
	}
	if !os.SameFile(fi, actual) {
		return nil, snapshot, fmt.Errorf("読込み中に設定が変わりました: %s", file)
	}
	b, err := io.ReadAll(io.LimitReader(f, profileLimit+1))
	if err != nil {
		return nil, snapshot, err
	}
	snapshot.Before, snapshot.Mode, snapshot.Exists = b, fi.Mode().Perm(), true
	var store profileStore
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if len(b) > profileLimit || decoder.Decode(&store) != nil || decoder.Decode(new(any)) != io.EOF || store.Version != 1 {
		return nil, snapshot, fmt.Errorf("プロファイル設定が不正です。ファイルを退避・修復してください: %s", file)
	}
	names, ports, instances := map[string]bool{}, map[int]string{}, map[string]int{}
	for _, p := range store.Profiles {
		if err := validateProfile(p); err != nil {
			return nil, snapshot, fmt.Errorf("%s: %w", file, err)
		}
		if names[p.Name] || (ports[p.Port] != "" && ports[p.Port] != p.ConnectionName) || (instances[p.ConnectionName] != 0 && instances[p.ConnectionName] != p.Port) {
			return nil, snapshot, fmt.Errorf("プロファイルの名前・Port予約が矛盾しています: %s", file)
		}
		names[p.Name] = true
		ports[p.Port] = p.ConnectionName
		instances[p.ConnectionName] = p.Port
	}
	return store.Profiles, snapshot, nil
}

// 設定更新とProxy起動で同じ排他を使い、予約参照から管理記録公開までを保護する。
func (a *App) lockProfileStore(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := secureDir(a.Paths.ConfigDir); err != nil {
		return nil, err
	}
	file := filepath.Join(a.Paths.ConfigDir, "profiles.lock")
	if fi, err := os.Lstat(file); err == nil && !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("lockファイルが不正です: %s", file)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	actual, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	fi, err := os.Lstat(file)
	if err != nil || !fi.Mode().IsRegular() || !os.SameFile(fi, actual) {
		f.Close()
		return nil, fmt.Errorf("lockファイルが変わりました: %s", file)
	}
	unlock, err := lockProfiles(ctx, f)
	if err != nil {
		f.Close()
		return nil, err
	}
	var once sync.Once
	release := func() { once.Do(func() { unlock(); _ = f.Close() }) }
	fi, err = os.Lstat(file)
	if err != nil || !fi.Mode().IsRegular() || !os.SameFile(fi, actual) {
		release()
		return nil, fmt.Errorf("待機中にlockファイルが変わりました: %s", file)
	}
	if err := realParents(a.Paths.ConfigDir); err != nil {
		release()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}
func (a *App) changeProfiles(ctx context.Context, change func([]Profile) ([]Profile, error)) error {
	unlock, err := a.lockProfileStore(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	rows, err := a.LoadProfiles()
	if err != nil {
		return err
	}
	rows, err = change(rows)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return a.writeProfiles(rows)
}

// 呼び出し元が設定ロックを所有したまま、全設定を原子的に保存する。
func (a *App) writeProfiles(rows []Profile) error {
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	store := profileStore{1, rows}
	b, err := json.Marshal(store)
	if err != nil {
		return err
	}
	if len(b) > profileLimit {
		return fmt.Errorf("保存する設定が1MiBを超えます")
	}
	return writeJSON(filepath.Join(a.Paths.ConfigDir, "profiles.json"), store)
}

// 同一インスタンスの予約を一括更新し、別プロセスの起動・編集とも直列化する。
func (a *App) UpdateProfilePort(ctx context.Context, name string, expectedPort, newPort int) ([]Profile, error) {
	if newPort < 1 || newPort > 65535 {
		return nil, usageError{fmt.Errorf("Portは1〜65535を指定してください")}
	}
	unlock, err := a.lockProfileStore(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	rows, err := a.LoadProfiles()
	if err != nil {
		return nil, err
	}
	p, err := findProfile(rows, name)
	if err != nil {
		return nil, err
	}
	if p.Port != expectedPort {
		return nil, fmt.Errorf("保存済みPortが %d から %d に変更されています。設定を再表示してから操作してください", expectedPort, p.Port)
	}
	if p.Port == newPort {
		return rows, nil
	}
	records, err := a.records()
	if err != nil {
		return nil, err
	}
	unavailable := a.reservedProxyPorts()
	if unavailable == nil {
		unavailable = map[int]bool{}
	}
	for _, r := range records {
		if r.Project == p.Project && r.Name == p.Instance.Name && r.Region == p.Region {
			check, cancel := context.WithTimeout(ctx, 2*time.Second)
			status, e := a.querySession(check, r, false)
			cancel()
			if e != nil || status.State != "stopped" {
				return nil, fmt.Errorf("セッション %s の停止を確認できません。csql status で確認し、csql stop %s または起動した端末のCtrl+Cで停止してから変更してください", r.ID, r.ID)
			}
		} else {
			unavailable[r.Port] = true
		}
	}
	if _, err := availableProfilePort(p.Instance, newPort, rows, unavailable); err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].ConnectionName == p.ConnectionName {
			rows[i].Port = newPort
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := a.writeProfiles(rows); err != nil {
		return nil, err
	}
	return rows, nil
}
func (a *App) AddProfile(ctx context.Context, p Profile) error {
	if err := validateProfile(p); err != nil {
		return usageError{err}
	}
	return a.changeProfiles(ctx, func(rows []Profile) ([]Profile, error) {
		for _, r := range rows {
			if r.Name == p.Name {
				return nil, fmt.Errorf("プロファイル %s は登録済みです。別名を指定してください", p.Name)
			}
			if (r.ConnectionName == p.ConnectionName && r.Port != p.Port) || (r.ConnectionName != p.ConnectionName && r.Port == p.Port) {
				return nil, fmt.Errorf("Port %d の予約が変わりました。設定を確認して再操作してください", p.Port)
			}
		}
		return append(rows, p), nil
	})
}
func (a *App) RemoveProfile(ctx context.Context, name string) error {
	return a.changeProfiles(ctx, func(rows []Profile) ([]Profile, error) {
		for i, p := range rows {
			if p.Name == name {
				return append(rows[:i], rows[i+1:]...), nil
			}
		}
		return nil, fmt.Errorf("プロファイル %s はありません", name)
	})
}
func findProfile(rows []Profile, name string) (Profile, error) {
	for _, p := range rows {
		if p.Name == name {
			return p, nil
		}
	}
	return Profile{}, fmt.Errorf("プロファイル %s はありません。csql profile list を確認してください", name)
}
func (a *App) PrepareProfile(p Profile) (Profile, error) {
	rows, err := a.LoadProfiles()
	if err != nil {
		return p, err
	}
	for _, r := range rows {
		if r.ConnectionName == p.ConnectionName {
			if p.Port != 0 && p.Port != r.Port {
				return p, fmt.Errorf("登録済みPort %d と一致しません", r.Port)
			}
			p.Port = r.Port
			return p, validateProfile(p)
		}
	}
	requested := p.Port
	p.Port, err = availableProfilePort(p.Instance, p.Port, rows, a.reservedProxyPorts())
	if err != nil {
		// 接続中の「保存」では、署名で確認した自分のセッションのPortだけ再利用する。
		if requested > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			sessions, statusErr := a.Status(ctx)
			if statusErr == nil {
				for _, s := range sessions {
					if s.State == "running" && s.Port == requested && s.Project == p.Project && s.Name == p.Instance.Name && s.Region == p.Region {
						p.Port = requested
						return p, validateProfile(p)
					}
				}
			}
		}
		return p, err
	}
	return p, validateProfile(p)
}
func availableProfilePort(v Instance, explicit int, rows []Profile, unavailable map[int]bool) (int, error) {
	if explicit != 0 {
		if unavailable[explicit] {
			return 0, fmt.Errorf("Port %d は起動中・稼働中の接続が予約しています。csql status を確認してください", explicit)
		}
		for _, p := range rows {
			if p.Port == explicit && p.ConnectionName != v.ConnectionName {
				return 0, fmt.Errorf("Port %d は %s の保存済み予約です", explicit, p.ConnectionName)
			}
		}
		return choosePort(v.DatabaseVersion, explicit)
	}
	// ローカルDBの標準ポートを避けて自動割り当てを始める。
	base := 0
	switch {
	case strings.HasPrefix(v.DatabaseVersion, "POSTGRES"):
		base = 15432
	case strings.HasPrefix(v.DatabaseVersion, "MYSQL"):
		base = 13306
	case strings.HasPrefix(v.DatabaseVersion, "SQLSERVER"):
		base = 11433
	default:
		return 0, fmt.Errorf("未知のDB種別です。--port を指定してください")
	}
	for n := base; n < base+21; n++ {
		reserved := unavailable[n]
		for _, p := range rows {
			if p.Port == n {
				reserved = true
				break
			}
		}
		if !reserved {
			if port, err := choosePort(v.DatabaseVersion, n); err == nil {
				return port, nil
			}
		}
	}
	return 0, fmt.Errorf("空きPortがありません。csql status と保存済み予約を確認し、--port を指定してください")
}
func (a *App) profilePort(v Instance, explicit int, unavailable map[int]bool) (int, error) {
	rows, err := a.LoadProfiles()
	if err != nil {
		return 0, err
	}
	for _, p := range rows {
		if p.ConnectionName == v.ConnectionName {
			if explicit != 0 && explicit != p.Port {
				return 0, fmt.Errorf("指定Port %d は登録済みPort %d と一致しません。DBツールの設定を維持するため変更しません", explicit, p.Port)
			}
			if unavailable[p.Port] {
				return 0, fmt.Errorf("保存済みPort %d は起動中・稼働中です。csql status を確認してください。別Portへ自動変更しません", p.Port)
			}
			port, err := choosePort(v.DatabaseVersion, p.Port)
			if err != nil {
				return 0, fmt.Errorf("保存済みPort %d は使用中です。csql status で既存セッションを確認してください。別Portへ自動変更しません", p.Port)
			}
			return port, nil
		}
	}
	return availableProfilePort(v, explicit, rows, unavailable)
}

// 出力形式・拡張子を画面選択と自動保存先で共用する。
var exportTools = []struct{ format, label, suffix string }{
	{"pgadmin", "pgAdmin", ".json"},
	{"dbeaver", "DBeaver", ".csv"},
	{"a5", "A5:SQL Mk-2", ".a5dblist"},
	{"uri", "接続URI", ".txt"},
}

func exportGuide(format string) string {
	switch format {
	case "pgadmin":
		return "pgAdmin: Tools → Import/Export Serversで取込み（既存サーバーの削除は無効）"
	case "dbeaver":
		return "DBeaver: File → Import → Third Party Configuration → Custom。DBドライバーを選び、CSV・UTF-8で取込みます"
	case "a5":
		return "A5: データベース → データベースの追加と削除 → インポート。パスワードはA5側で入力してください"
	default:
		return "接続URI: 対応するDBツールに取り込んでください。パスワードはDBツール側で入力します"
	}
}

func profileExport(p Profile, format string) ([]byte, error) {
	if err := validateProfile(p); err != nil {
		return nil, err
	}
	switch format {
	case "pgadmin":
		if !strings.HasPrefix(p.DatabaseVersion, "POSTGRES") {
			return nil, fmt.Errorf("pgAdmin出力はPostgreSQLだけに対応します")
		}
		server := map[string]any{"Name": "csql / " + p.Project + " / " + p.Name, "Group": "csql", "Host": "127.0.0.1", "Port": p.Port, "MaintenanceDB": p.Database, "Username": p.Username, "SSLMode": "disable", "ConnectionParameters": map[string]string{"sslmode": "disable"}}
		return json.MarshalIndent(map[string]any{"Servers": map[string]any{"1": server}}, "", "  ")
	case "dbeaver":
		// DBeaverのCustom取り込み用。引用は標準CSVへ任せ、driverは取り込み時に選択する。
		var data bytes.Buffer
		writer := csv.NewWriter(&data)
		writer.UseCRLF = true
		if err := writer.WriteAll([][]string{{"name", "host", "port", "database", "user"}, {"csql / " + p.Project + " / " + p.Name, "127.0.0.1", strconv.Itoa(p.Port), p.Database, p.Username}}); err != nil {
			return nil, err
		}
		return data.Bytes(), nil
	case "a5":
		name, conn, err := a5Connection(p)
		if err != nil {
			return nil, err
		}
		// BOMと改行を含む従来の出力形式を維持する。
		return []byte("\ufeff" + name + "=" + conn + "\r\n"), nil
	case "uri":
		scheme := ""
		switch {
		case strings.HasPrefix(p.DatabaseVersion, "POSTGRES"):
			scheme = "postgresql"
		case strings.HasPrefix(p.DatabaseVersion, "MYSQL"):
			scheme = "mysql"
		default:
			return nil, fmt.Errorf("このDB種別の汎用URIは未対応です。Host 127.0.0.1 / Port %d / DB %s / User %s を設定してください", p.Port, p.Database, p.Username)
		}
		u := url.URL{Scheme: scheme, User: url.User(p.Username), Host: fmt.Sprintf("127.0.0.1:%d", p.Port), Path: "/" + p.Database, RawPath: "/" + url.PathEscape(p.Database)}
		if scheme == "postgresql" {
			u.RawQuery = "sslmode=disable"
		}
		return []byte(u.String()), nil
	default:
		return nil, usageError{fmt.Errorf("--format pgadmin|dbeaver|a5|uri を指定してください")}
	}
}
func writeProfileExport(path string, b []byte) (retErr error) {
	if err := realParents(filepath.Dir(path)); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		closeErr := f.Close()
		if retErr == nil {
			retErr = closeErr
		}
		if retErr != nil {
			_ = os.Remove(path)
		}
	}()
	if len(b) == 0 || b[len(b)-1] != '\n' {
		b = append(b, '\n')
	}
	_, retErr = f.Write(b)
	return retErr
}

func (a *App) profileCommand() *cobra.Command {
	argCount := func(n int) cobra.PositionalArgs {
		return func(c *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(n)(c, args); err != nil {
				return usageError{err}
			}
			return nil
		}
	}
	root := &cobra.Command{Use: "profile", Short: "接続設定の保存・再利用", Args: argCount(0), RunE: func(c *cobra.Command, _ []string) error { return c.Help() }}
	add := &cobra.Command{Use: "add NAME", Short: "接続設定を保存", Args: argCount(1), RunE: func(c *cobra.Command, args []string) error {
		project, _ := c.Flags().GetString("project")
		name, _ := c.Flags().GetString("instance")
		db, _ := c.Flags().GetString("database")
		user, _ := c.Flags().GetString("user")
		port, _ := c.Flags().GetInt("port")
		priv, _ := c.Flags().GetBool("private-ip")
		if project == "" || name == "" || db == "" || user == "" {
			if a.TTY {
				return a.runConnectModel(c.Context(), connectRequest{Project: project, Query: name}, &Profile{Name: args[0], Database: db, Username: user, Port: port, PrivateIP: priv})
			}
			return usageError{fmt.Errorf("--project / --instance / --database / --user を指定してください")}
		}
		result, err := a.Discover(c.Context(), project, false)
		if err != nil {
			return err
		}
		var selected *Instance
		for _, v := range result.Instances {
			if v.Name == name {
				if selected != nil {
					return fmt.Errorf("同名の候補が複数あります")
				}
				v := v
				selected = &v
			}
		}
		if selected == nil {
			return fmt.Errorf("指定project内に一致するインスタンスがありません")
		}
		p, err := a.PrepareProfile(Profile{Name: args[0], Instance: *selected, Database: db, Username: user, Port: port, PrivateIP: priv})
		if err != nil {
			return err
		}
		fmt.Fprintf(a.stdout(), "保存する設定: %s / %s / 127.0.0.1:%d / DB %s / User %s\n", p.Name, p.ConnectionName, p.Port, cleanText(db), cleanText(user))
		if err = a.AddProfile(c.Context(), p); err != nil {
			return err
		}
		fmt.Fprintln(a.stdout(), "保存しました。DBログインは未検証です。次回: csql --profile "+p.Name)
		return nil
	}}
	for _, flag := range []string{"project", "instance", "database", "user"} {
		add.Flags().String(flag, "", map[string]string{"project": "プロジェクトID", "instance": "インスタンス名（完全一致）", "database": "DB名", "user": "DBユーザー名"}[flag])
	}
	add.Flags().IntP("port", "p", 0, "保存するローカルPort（省略時は選択）")
	add.Flags().Bool("private-ip", false, "Private IPで接続")
	update := &cobra.Command{Use: "update NAME", Short: "停止済みの保存設定のPortを変更", Args: argCount(1), RunE: func(c *cobra.Command, args []string) error {
		if !c.Flags().Changed("port") {
			return usageError{fmt.Errorf("--port を指定してください")}
		}
		port, _ := c.Flags().GetInt("port")
		if port < 1 || port > 65535 {
			return usageError{fmt.Errorf("Portは1〜65535を指定してください")}
		}
		rows, err := a.LoadProfiles()
		if err != nil {
			return err
		}
		p, err := findProfile(rows, args[0])
		if err != nil {
			return err
		}
		var names []string
		for _, row := range rows {
			if row.ConnectionName == p.ConnectionName {
				names = append(names, row.Name)
			}
		}
		fmt.Fprintf(a.stdout(), "更新対象: %s / Port %d → %d / 設定: %s\n", p.ConnectionName, p.Port, port, strings.Join(names, ", "))
		if _, err = a.UpdateProfilePort(c.Context(), p.Name, p.Port, port); err != nil {
			return err
		}
		if p.Port == port {
			fmt.Fprintln(a.stdout(), "変更はありません")
			return nil
		}
		fmt.Fprintln(a.stdout(), "Portを更新しました。DBツール側のPortを変更するか、csql profile exportで再出力してください")
		return nil
	}}
	update.Flags().IntP("port", "p", 0, "変更先のローカルPort（必須）")
	list := &cobra.Command{Use: "list", Short: "保存した接続設定", Args: argCount(0), RunE: func(c *cobra.Command, _ []string) error {
		rows, err := a.LoadProfiles()
		if err != nil {
			return err
		}
		for _, p := range rows {
			fmt.Fprintf(a.stdout(), "%s\t%s\t127.0.0.1:%d\t%s\t%s\n", p.Name, p.ConnectionName, p.Port, p.Database, p.Username)
		}
		return nil
	}}
	remove := &cobra.Command{Use: "remove NAME", Short: "接続設定を削除", Args: argCount(1), RunE: func(c *cobra.Command, args []string) error {
		rows, err := a.LoadProfiles()
		if err != nil {
			return err
		}
		p, err := findProfile(rows, args[0])
		if err != nil {
			return err
		}
		ok, err := a.confirm(c.Context(), fmt.Sprintf("%s (%s / Port %d) を削除しますか？ [y/N]", p.Name, p.ConnectionName, p.Port))
		if err != nil || !ok {
			return err
		}
		return a.RemoveProfile(c.Context(), p.Name)
	}}
	export := &cobra.Command{Use: "export NAME", Short: "DBツール用の接続設定・URIを出力", Args: argCount(1), RunE: func(c *cobra.Command, args []string) error {
		rows, err := a.LoadProfiles()
		if err != nil {
			return err
		}
		p, err := findProfile(rows, args[0])
		if err != nil {
			return err
		}
		format, _ := c.Flags().GetString("format")
		b, err := profileExport(p, format)
		if err != nil {
			return err
		}
		output, _ := c.Flags().GetString("output")
		if output != "" {
			if err = writeProfileExport(output, b); err != nil {
				return err
			}
			fmt.Fprintln(a.stdout(), "出力しました: "+output)
			return nil
		}
		if len(b) == 0 || b[len(b)-1] != '\n' {
			b = append(b, '\n')
		}
		_, err = a.stdout().Write(b)
		return err
	}}
	export.Flags().String("format", "", "出力形式（pgadmin|dbeaver|a5|uri）")
	export.Flags().String("output", "", "新規出力ファイル（省略時は標準出力）")
	root.AddCommand(add, list, remove, export, update, a.profileRegisterCommand())
	return root
}
