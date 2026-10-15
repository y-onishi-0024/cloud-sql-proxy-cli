package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

const pathStart = "# >>> csql PATH >>>"
const pathEnd = "# <<< csql PATH <<<"
const settingsLimit = 1 << 20

type userPathValue struct {
	Value  string
	Kind   uint32
	Exists bool
}

// OS境界だけを差し替え、一時領域でもユーザーPATHの保存契約を検証する。
type userPathStore interface {
	Read() (userPathValue, error)
	Save(expected, next userPathValue) error
	Notify() error
}
type pathReceipt struct{ WindowsOwned []string }
type binaryReceipt struct{ SHA256 string }
type pathFile struct {
	Path          string
	Before, After []byte
	Mode          os.FileMode
	NewMode       os.FileMode
	Exists        bool
}

func (a *App) pathDirs(ctx context.Context) ([]string, error) {
	dirs := []string{filepath.Join(a.Paths.ConfigDir, "bin")}
	for _, name := range []string{"gcloud", "cloud-sql-proxy"} {
		var m installedTool
		err := readJSONFile(filepath.Join(a.Paths.ToolsDir, name, "installed.json"), &m)
		if os.IsNotExist(err) {
			fmt.Fprintf(a.stdout(), "%s: 管理版未導入（追加対象外）\n", name)
			continue
		}
		if err != nil || m.Version == "" || !safeRelative(m.RelativePath) {
			return nil, fmt.Errorf("%s の管理記録が不正です。csql setup --install を確認してください", name)
		}
		p := filepath.Join(a.Paths.ToolsDir, name, filepath.FromSlash(m.RelativePath))
		if err = pathNoLinks(p); err != nil {
			return nil, err
		}
		if _, _, err = a.checkPath(ctx, p, name); err != nil {
			return nil, err
		}
		dirs = append(dirs, filepath.Dir(p))
	}
	for _, d := range dirs {
		if err := validPathDir(d, a.OS); err != nil {
			return nil, err
		}
	}
	return dirs, nil
}
func validPathDir(d, goos string) error {
	separator := ":"
	if goos == "windows" {
		separator = ";"
	}
	if !filepath.IsAbs(d) || strings.Contains(d, separator) || strings.IndexFunc(d, unicode.IsControl) >= 0 {
		return fmt.Errorf("PATHへ安全に登録できないディレクトリ: %q", d)
	}
	return nil
}

func (a *App) SetupPath(ctx context.Context, shell string) error {
	var toolRecords []pathFile
	for _, n := range []string{"gcloud", "cloud-sql-proxy"} {
		f, e := pathReadChange(filepath.Join(a.Paths.ToolsDir, n, "installed.json"), settingsLimit)
		if e != nil {
			return e
		}
		toolRecords = append(toolRecords, f)
	}
	dirs, err := a.pathDirs(ctx)
	if err != nil {
		return err
	}
	sourceFn := a.Executable
	if sourceFn == nil {
		sourceFn = os.Executable
	}
	source, err := sourceFn()
	if err != nil {
		return err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	sourceBytes, _, err := pathRead(source, 256<<20)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(sourceBytes)
	hash := hex.EncodeToString(sum[:])
	name := "csql"
	if a.OS == "windows" {
		name += ".exe"
	}
	destination := filepath.Join(dirs[0], name)
	binary, err := pathReadChange(destination, 256<<20)
	if err != nil {
		return err
	}
	binary.After = sourceBytes
	if a.OS != "windows" {
		binary.NewMode = 0700
	}
	if binary.Exists && !bytes.Equal(binary.Before, sourceBytes) {
		var receipt binaryReceipt
		old := sha256.Sum256(binary.Before)
		if readJSONFile(filepath.Join(a.Paths.ConfigDir, "binary-install.json"), &receipt) != nil || receipt.SHA256 != hex.EncodeToString(old[:]) {
			return fmt.Errorf("%s はcsqlが配置した版と確認できないため上書きしません。別の場所へ退避して再実行してください", destination)
		}
	}
	var files []pathFile
	var store userPathStore
	var previous, next userPathValue
	var receipt pathReceipt
	receiptPath := filepath.Join(a.Paths.ConfigDir, "path-install.json")
	receiptSnapshot, err := pathReadChange(receiptPath, settingsLimit)
	if err != nil {
		return err
	}
	if receiptSnapshot.Exists {
		if err = readJSONFile(receiptPath, &receipt); err != nil {
			return err
		}
	}
	if a.OS == "windows" {
		if shell != "" {
			return usageError{fmt.Errorf("Windowsでは --shell を指定できません")}
		}
		store = a.UserPath
		if store == nil {
			store = newUserPathStore()
		}
		previous, err = store.Read()
		if err != nil {
			return err
		}
		// REG_EXPAND_SZではリテラルの%を安全に保持できないため、保存前に拒否する。
		if previous.Kind == 2 {
			for _, d := range dirs {
				if strings.Contains(d, "%") {
					return fmt.Errorf("登録先 %q はユーザーPathの環境変数展開で別の場所になる可能性があります。%%を含まない場所へ配置して手動でPATHを設定してください（既存Pathの種類は変更しません）", d)
				}
			}
		}
		next, receipt.WindowsOwned, err = a.updatedUserPath(previous, receipt.WindowsOwned, dirs)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.stdout(), "変更先: HKCU\\Environment\\Path（ユーザーのみ）\n変更前: %s\n変更後: %s\nバックアップ: %s 以下へ元の値・種類を保存\n", previous.Value, next.Value, a.Paths.ConfigDir)
	} else {
		if shell == "" {
			shell = filepath.Base(a.Env("SHELL"))
		}
		files, err = a.shellPathFiles(shell, dirs)
		if err != nil {
			fmt.Fprintf(a.stderr(), "手動設定: シェルのPATH末尾へ次を追加してください:\n%s\n", strings.Join(dirs, "\n"))
			return err
		}
		for _, f := range files {
			begin := bytes.Index(f.After, []byte(pathStart))
			end := bytes.Index(f.After, []byte(pathEnd)) + len(pathEnd)
			fmt.Fprintf(a.stdout(), "変更先: %s\n設定内容:\n%s\nバックアップ: 既存ファイルは %s.csql-backup-*\n", f.Path, f.After[begin:end], f.Path)
		}
	}
	fmt.Fprintf(a.stdout(), "csqlのコピー: %s → %s\n追加ディレクトリ（既存PATHを優先）:\n%s\n", source, destination, strings.Join(dirs, "\n"))
	for _, n := range []string{"csql", "gcloud", "cloud-sql-proxy"} {
		if p, e := a.LookPath(n); e == nil {
			fmt.Fprintf(a.stdout(), "既存PATHの %s が優先されます: %s\n", n, p)
		}
	}
	ok, err := a.confirm(ctx, "この内容でPATHを設定しますか？ [y/N]")
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(a.stdout(), "PATH設定を取り消しました")
		return nil
	}
	if err = secureDir(a.Paths.ConfigDir); err != nil {
		return err
	}
	lock := filepath.Join(a.Paths.ConfigDir, ".path-lock")
	if err = os.Mkdir(lock, 0700); err != nil {
		return fmt.Errorf("別のPATH設定が進行中、またはロック作成不能: %w", err)
	}
	defer os.Remove(lock)
	// 確認中の利用者編集や別setupによる版変更を上書きしない。
	for _, f := range toolRecords {
		if err = pathUnchanged(f, settingsLimit); err != nil {
			return err
		}
	}
	if err = pathUnchanged(receiptSnapshot, settingsLimit); err != nil {
		return err
	}
	for _, f := range files {
		if err = pathUnchanged(f, settingsLimit); err != nil {
			return err
		}
	}
	if store != nil {
		actual, e := store.Read()
		if e != nil {
			return e
		}
		if actual != previous {
			return fmt.Errorf("確認中にユーザーPATHが変更されました。再実行してください")
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = pathApply(binary, 256<<20, false, a.stdout()); err != nil {
		return err
	}
	if err = writeJSON(filepath.Join(a.Paths.ConfigDir, "binary-install.json"), binaryReceipt{hash}); err != nil {
		return fmt.Errorf("csqlは配置済みですが記録保存に失敗: %w", err)
	}
	fmt.Fprintf(a.stdout(), "csql配置済み: %s\n", destination)
	for _, f := range files {
		if err = ctx.Err(); err == nil {
			err = pathApply(f, settingsLimit, true, a.stdout())
		}
		if err != nil {
			return fmt.Errorf("PATH設定が部分完了です。csql配置済み、未完了: %s。表示済みバックアップを確認してください: %w", f.Path, err)
		}
		fmt.Fprintf(a.stdout(), "設定済み: %s\n", f.Path)
	}
	if store != nil && next != previous {
		backup, e := pathBackup(filepath.Join(a.Paths.ConfigDir, "windows-path"), previous)
		if e != nil {
			return e
		}
		fmt.Fprintf(a.stdout(), "ユーザーPATHバックアップ: %s\n", backup)
		if err = ctx.Err(); err == nil {
			err = store.Save(previous, next)
		}
		if err != nil {
			return fmt.Errorf("csql配置済み、ユーザーPATHの保存は未完了: %w", err)
		}
	}
	if store != nil {
		if err = writeJSON(receiptPath, receipt); err != nil {
			return fmt.Errorf("ユーザーPATHは保存済みですが登録記録の保存失敗: %w", err)
		}
		if err = store.Notify(); err != nil {
			return fmt.Errorf("ユーザーPATHは保存済みですが環境変更通知に失敗しました。サインアウト後に再確認してください: %w", err)
		}
	}
	fmt.Fprintln(a.stdout(), "PATH設定を保存しました。現在の端末へは未反映です。ターミナルアプリを終了して開き直し、csql version / gcloud --version / cloud-sql-proxy --version で確認してください。既存コマンドがある場合はそちらが優先されます。")
	return nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func (a *App) shellPathFiles(shell string, dirs []string) ([]pathFile, error) {
	home := a.Env("HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return nil, err
		}
	}
	var paths []string
	switch shell {
	case "zsh":
		base := a.Env("ZDOTDIR")
		if base == "" {
			base = home
		}
		if !filepath.IsAbs(base) {
			return nil, fmt.Errorf("ZDOTDIRは絶対パスで指定してください")
		}
		paths = []string{filepath.Join(base, ".zshrc")}
	case "bash":
		login := filepath.Join(home, ".bash_profile")
		for _, n := range []string{".bash_profile", ".bash_login", ".profile"} {
			p := filepath.Join(home, n)
			if _, err := os.Lstat(p); err == nil {
				login = p
				break
			} else if !os.IsNotExist(err) {
				return nil, err
			}
		}
		paths = []string{filepath.Join(home, ".bashrc"), login}
	case "fish":
		base := a.Env("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(home, ".config")
		}
		paths = []string{filepath.Join(base, "fish", "conf.d", "csql.fish")}
	default:
		return nil, fmt.Errorf("シェル %q の自動PATH設定は非対応です。--shell zsh|bash|fish を指定してください", shell)
	}
	block := pathStart + "\n"
	for _, d := range dirs {
		if shell == "fish" {
			q := "'" + strings.NewReplacer("\\", "\\\\", "'", "\\'").Replace(d) + "'"
			block += "fish_add_path --append --path " + q + "\n"
		} else {
			q := shellQuote(d)
			block += "case \":${PATH-}:\" in\n  *:" + q + ":*) ;;\n  *) export PATH=\"${PATH:+$PATH:}\"" + q + " ;;\nesac\n"
		}
	}
	block += pathEnd + "\n"
	var files []pathFile
	for _, p := range paths {
		f, err := pathReadChange(p, settingsLimit)
		if err != nil {
			return nil, err
		}
		content := string(f.Before)
		start, end := strings.Index(content, pathStart), strings.Index(content, pathEnd)
		if start < 0 && end < 0 {
			if shell == "fish" && f.Exists {
				return nil, fmt.Errorf("%s はcsql所有と確認できません", p)
			}
			if content != "" && !strings.HasSuffix(content, "\n") {
				content += "\n"
			}
			content += block
		} else {
			if strings.Count(content, pathStart) != 1 || start+len(pathStart) >= len(content) || content[start+len(pathStart)] != '\n' || strings.Count(content, pathEnd) != 1 || start > end || (start > 0 && content[start-1] != '\n') || (end > 0 && content[end-1] != '\n') {
				return nil, fmt.Errorf("%s のcsqlマーカーが不正です", p)
			}
			tail := end + len(pathEnd)
			if tail < len(content) {
				if content[tail] != '\n' {
					return nil, fmt.Errorf("%s のマーカー末尾が不正です", p)
				}
				tail++
			}
			if shell == "fish" && (strings.TrimSpace(content[:start]) != "" || strings.TrimSpace(content[tail:]) != "") {
				return nil, fmt.Errorf("%s にcsql以外の設定があります", p)
			}
			content = content[:start] + block + content[tail:]
		}
		f.After = []byte(content)
		files = append(files, f)
	}
	return files, nil
}

func (a *App) updatedUserPath(old userPathValue, owned, dirs []string) (userPathValue, []string, error) {
	normalize := func(s string) string {
		return strings.ToLower(strings.TrimRight(strings.ReplaceAll(s, "/", "\\"), "\\"))
	}
	references := regexp.MustCompile(`%([^%]+)%`)
	existingMeaning := func(s string) string {
		// REG_SZの%は文字列のまま比較し、REG_EXPAND_SZだけを展開する。
		if old.Kind == 2 {
			s = references.ReplaceAllStringFunc(s, func(m string) string {
				if v := a.Env(m[1 : len(m)-1]); v != "" {
					return v
				}
				return m
			})
		}
		return normalize(s)
	}
	var entries []string
	if old.Value != "" {
		entries = strings.Split(old.Value, ";")
	}
	// 同じ原文が複数あると、利用者が先頭へ追加した項目と元の所有項目を区別できない。
	// 位置を推測して優先順位を変える代わりに、確認前に手動設定へ戻す。
	ownedCounts := map[string]int{}
	for _, p := range owned {
		ownedCounts[p]++
	}
	for p, count := range ownedCounts {
		actual := 0
		for _, v := range entries {
			if v == p {
				actual++
			}
		}
		if actual > count {
			return userPathValue{}, nil, fmt.Errorf("ユーザーPATHの %q が重複し、csqlの登録位置を特定できません。PATHは変更しません。ユーザー環境変数の編集でこの項目と追加先（%s）を確認し、手動設定してください", p, strings.Join(dirs, ";"))
		}
	}
	// 削除は記録した原文・個数だけ。意味上等価な利用者項目は所有と推定しない。
	remainingOwned := append([]string{}, owned...)
	var kept []string
	for _, v := range entries {
		match := -1
		for i, p := range remainingOwned {
			if v == p {
				match = i
				break
			}
		}
		if match >= 0 {
			remainingOwned = append(remainingOwned[:match], remainingOwned[match+1:]...)
		} else {
			kept = append(kept, v)
		}
	}
	var added []string
	for _, d := range dirs {
		found := false
		for _, v := range kept {
			if existingMeaning(v) == normalize(d) {
				found = true
				break
			}
		}
		if !found {
			kept = append(kept, d)
			added = append(added, d)
		}
	}
	next := old
	next.Value = strings.Join(kept, ";")
	next.Exists = true
	if !old.Exists {
		next.Kind = 1
	}
	return next, added, nil
}

// 既存ディレクトリの権限は変更せず、リンクをたどる設定書き込みを拒否する。
func pathNoLinks(p string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("絶対パスが必要です: %q", p)
	}
	for cur := filepath.Clean(p); ; cur = filepath.Dir(cur) {
		fi, err := os.Lstat(cur)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("リンクへのPATH設定は行いません: %s", cur)
		}
		if filepath.Dir(cur) == cur {
			break
		}
	}
	return nil
}
func pathRead(p string, limit int64) ([]byte, os.FileMode, error) {
	if err := pathNoLinks(p); err != nil {
		return nil, 0, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > limit {
		return nil, 0, fmt.Errorf("設定/実行ファイルが通常ファイルでないか容量超過: %s", p)
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(b)) > limit {
		return nil, 0, fmt.Errorf("容量超過: %s", p)
	}
	return b, fi.Mode().Perm(), err
}
func pathReadChange(p string, limit int64) (pathFile, error) {
	b, m, err := pathRead(p, limit)
	f := pathFile{Path: p, Before: b, Mode: m, Exists: err == nil}
	if os.IsNotExist(err) {
		f.Mode = 0600
		return f, nil
	}
	return f, err
}
func pathUnchanged(f pathFile, limit int64) error {
	now, err := pathReadChange(f.Path, limit)
	if err != nil {
		return err
	}
	if now.Exists != f.Exists || now.Mode != f.Mode || !bytes.Equal(now.Before, f.Before) {
		return fmt.Errorf("確認中にファイルが変更されました: %s", f.Path)
	}
	return nil
}
func pathApply(f pathFile, limit int64, backup bool, out io.Writer) error {
	if err := pathUnchanged(f, limit); err != nil {
		return err
	}
	if f.Exists && bytes.Equal(f.Before, f.After) && (f.NewMode == 0 || f.Mode == f.NewMode) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0700); err != nil {
		return err
	}
	if err := pathNoLinks(f.Path); err != nil {
		return err
	}
	if backup && f.Exists {
		b, err := os.CreateTemp(filepath.Dir(f.Path), filepath.Base(f.Path)+".csql-backup-*")
		if err != nil {
			return err
		}
		_, err = b.Write(f.Before)
		closeErr := b.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		fmt.Fprintf(out, "バックアップ: %s\n", b.Name())
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.Path), ".csql-path-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	mode := f.Mode
	if f.NewMode != 0 {
		mode = f.NewMode
	}
	if err = tmp.Chmod(mode); err == nil {
		_, err = tmp.Write(f.After)
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = pathUnchanged(f, limit); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.Path)
}
func pathBackup(base string, value any) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(base), filepath.Base(base)+".csql-backup-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	if err = f.Close(); err != nil {
		return "", err
	}
	err = writeJSON(name, value)
	return name, err
}
