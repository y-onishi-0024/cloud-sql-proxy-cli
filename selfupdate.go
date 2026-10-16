package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type selfUpdateOffer struct {
	Tag, Name, URL, SHA256 string
	Size                   int64
}

// 明示更新は通知cacheを使わず、公式の正式版と完全一致するassetだけを選ぶ。
func (a *App) fetchSelfUpdate(ctx context.Context) (*selfUpdateOffer, error) {
	if !((a.OS == "darwin" || a.OS == "linux") && (a.Arch == "arm64" || a.Arch == "amd64") || a.OS == "windows" && a.Arch == "amd64") {
		return nil, fmt.Errorf("このOS/CPUの更新には対応していません: %s/%s\n%s", a.OS, a.Arch, updatePage)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client := a.selfUpdateClient(10 * time.Second)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, updateAPI, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "csql/"+version)
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("更新情報を取得できません。通信を確認してcsql updateを再実行してください")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("更新情報の取得に失敗しました（HTTP %d）。時間を置いてcsql updateを再実行してください", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, (256<<10)+1))
	if err != nil || len(body) > 256<<10 {
		return nil, fmt.Errorf("更新情報を安全に読み込めませんでした")
	}
	var rel struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name, State, Digest string
			Size                int64
		} `json:"assets"`
	}
	if json.Unmarshal(body, &rel) != nil || rel.Draft || rel.Prerelease {
		return nil, fmt.Errorf("正式版の更新情報ではありません\n%s", updatePage)
	}
	if _, ok := parseReleaseVersion(rel.Tag); !ok || !strings.HasPrefix(rel.Tag, "v") {
		return nil, fmt.Errorf("更新情報の版番号が不正です")
	}
	if !newerRelease(rel.Tag) {
		return nil, nil
	}
	name := "csql-" + strings.TrimPrefix(rel.Tag, "v") + "-" + a.OS + "-" + a.Arch
	if a.OS == "windows" {
		name += ".exe"
	}
	var offer *selfUpdateOffer
	for _, asset := range rel.Assets {
		if asset.Name != name {
			continue
		}
		if offer != nil {
			return nil, fmt.Errorf("更新ファイルが重複しています\n%s", updatePage)
		}
		digest := strings.TrimPrefix(asset.Digest, "sha256:")
		hash, err := hex.DecodeString(digest)
		if !strings.HasPrefix(asset.Digest, "sha256:") || err != nil || len(hash) != 32 || asset.State != "uploaded" || asset.Size <= 0 || asset.Size > downloadLimit {
			return nil, fmt.Errorf("更新ファイルのハッシュ・容量・配布状態が不正です\n%s", updatePage)
		}
		offer = &selfUpdateOffer{rel.Tag, name, "https://github.com/y-onishi-0024/cloud-sql-proxy-cli/releases/download/" + rel.Tag + "/" + name, strings.ToLower(digest), asset.Size}
	}
	if offer == nil {
		return nil, fmt.Errorf("この版には対応するCLI更新ファイルがありません\n%s", updatePage)
	}
	return offer, ctx.Err()
}
func (a *App) selfUpdateClient(timeout time.Duration) *http.Client {
	c := &http.Client{}
	if a.HTTP != nil {
		*c = *a.HTTP
	}
	c.Jar = nil
	if c.Timeout == 0 || c.Timeout > timeout {
		c.Timeout = timeout
	}
	return c
}

type selfUpdateTarget struct {
	Path            string
	Binary, Receipt pathFile
}
type selfUpdateProgress struct {
	Phase           string
	Received, Total int64
}
type preparedSelfUpdate struct {
	Offer             selfUpdateOffer
	Target            selfUpdateTarget
	StageDir, Payload string
	mu                sync.Mutex
	closed, retained  bool
	unlock            func()
}

func (a *App) inspectSelfUpdateTarget() (selfUpdateTarget, error) {
	var t selfUpdateTarget
	name := "csql"
	if a.OS == "windows" {
		name += ".exe"
	}
	t.Path = filepath.Join(a.Paths.ConfigDir, "bin", name)
	exe := a.Executable
	if exe == nil {
		exe = os.Executable
	}
	running, err := exe()
	if err != nil {
		return t, err
	}
	equal := filepath.Clean(running) == filepath.Clean(t.Path)
	if a.OS == "windows" {
		equal = strings.EqualFold(filepath.Clean(running), filepath.Clean(t.Path))
	}
	if !equal {
		return t, fmt.Errorf("管理領域のcsqlではないため更新できません。公式セットアップ、または csql setup --path で管理領域へ配置してください\n管理先: %s", t.Path)
	}
	if t.Binary, err = pathReadChange(t.Path, downloadLimit); err != nil || !t.Binary.Exists {
		return t, fmt.Errorf("csqlの管理先を確認できません: %s%s", t.Path, selfUpdateRecoveryHint(t.Path))
	}
	t.Receipt, err = pathReadChange(filepath.Join(a.Paths.ConfigDir, "binary-install.json"), settingsLimit)
	var receipt binaryReceipt
	hash := sha256.Sum256(t.Binary.Before)
	if err != nil || !t.Receipt.Exists || json.Unmarshal(t.Receipt.Before, &receipt) != nil || receipt.SHA256 != hex.EncodeToString(hash[:]) {
		return t, fmt.Errorf("csqlの管理記録が一致しません。公式セットアップで配置を確認してください\n%s%s", t.Path, selfUpdateRecoveryHint(t.Path))
	}
	return t, nil
}

func (a *App) prepareSelfUpdate(ctx context.Context, offer selfUpdateOffer, target selfUpdateTarget, progress func(selfUpdateProgress)) (_ *preparedSelfUpdate, resultErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if offer.Size <= 0 || offer.Size > downloadLimit {
		return nil, fmt.Errorf("更新容量が不正です")
	}
	if err := pathNoLinks(target.Path); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(filepath.Dir(target.Path), ".csql-update-")
	if err != nil {
		return nil, err
	}
	p := &preparedSelfUpdate{Offer: offer, Target: target, StageDir: dir, Payload: filepath.Join(dir, "payload")}
	if a.OS == "windows" {
		p.Payload += ".exe"
	}
	defer func() {
		if resultErr != nil {
			_ = p.Close()
		}
	}()
	report := func(phase string, received int64) {
		if progress != nil {
			progress(selfUpdateProgress{phase, received, offer.Size})
		}
	}
	report("取得中", 0)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	client := a.selfUpdateClient(5 * time.Minute)
	allowed := func(u *url.URL) bool {
		return u.Scheme == "https" && u.User == nil && (u.Port() == "" || u.Port() == "443") && (u.Hostname() == "github.com" || u.Hostname() == "release-assets.githubusercontent.com") && u.Fragment == ""
	}
	initial, err := url.Parse(offer.URL)
	if err != nil || !allowed(initial) || initial.Hostname() != "github.com" {
		return nil, fmt.Errorf("許可していない更新配布元です")
	}
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) > 3 || !allowed(r.URL) {
			return fmt.Errorf("許可していない更新配布元への転送です")
		}
		r.Header.Del("Authorization")
		r.Header.Del("Cookie")
		return nil
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, offer.URL, nil)
	req.Header.Set("User-Agent", "csql/"+version)
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("更新ファイルを取得できません。通信と公式Releaseを確認してください")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("更新ファイルの取得に失敗しました（HTTP %d）", res.StatusCode)
	}
	if res.ContentLength > downloadLimit || res.ContentLength >= 0 && res.ContentLength != offer.Size {
		return nil, fmt.Errorf("更新ファイルの容量が配布情報と一致しません")
	}
	f, err := os.OpenFile(p.Payload, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	received := int64(0)
	pw := selfUpdateProgressWriter{func(n int) { received += int64(n); report("取得中", received) }}
	n, copyErr := io.Copy(io.MultiWriter(f, hash, pw), io.LimitReader(res.Body, offer.Size+1))
	closeErr := f.Close()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if copyErr != nil {
		return nil, fmt.Errorf("更新ファイルの取得が途中で終了しました")
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if n != offer.Size {
		return nil, fmt.Errorf("更新ファイルの容量が配布情報と一致しません")
	}
	if hex.EncodeToString(hash.Sum(nil)) != offer.SHA256 {
		return nil, fmt.Errorf("更新ファイルのSHA-256が一致しません。旧版を維持しました")
	}
	report("検証中", n)
	if err = a.checkSelfUpdateBinary(ctx, p.Payload, offer.Tag); err != nil {
		return nil, err
	}
	report("準備完了", n)
	return p, nil
}

type selfUpdateProgressWriter struct{ report func(int) }

func (w selfUpdateProgressWriter) Write(b []byte) (int, error) { w.report(len(b)); return len(b), nil }

// ハッシュに加えOS/CPU/CLI形式を検査してから、取得した実行ファイルを起動する。
func (a *App) checkSelfUpdateBinary(ctx context.Context, path, tag string) error {
	valid := false
	switch a.OS {
	case "darwin":
		f, e := macho.Open(path)
		if e == nil {
			want := macho.CpuArm64
			if a.Arch == "amd64" {
				want = macho.CpuAmd64
			}
			valid = f.Cpu == want && f.Type == macho.TypeExec
			f.Close()
		}
	case "linux":
		f, e := elf.Open(path)
		if e == nil {
			want := elf.EM_AARCH64
			if a.Arch == "amd64" {
				want = elf.EM_X86_64
			}
			valid = f.Machine == want && (f.Type == elf.ET_EXEC || f.Type == elf.ET_DYN)
			f.Close()
		}
	case "windows":
		f, e := pe.Open(path)
		if e == nil {
			h, ok := f.OptionalHeader.(*pe.OptionalHeader64)
			valid = f.Machine == pe.IMAGE_FILE_MACHINE_AMD64 && ok && h.Subsystem == pe.IMAGE_SUBSYSTEM_WINDOWS_CUI
			f.Close()
		}
	}
	if !valid {
		return fmt.Errorf("更新ファイルのOS/CPU・CLI形式が一致しません")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version")
	cmd.WaitDelay = 200 * time.Millisecond
	var out bytes.Buffer
	cmd.Stdout = &limitedWriter{Writer: &out, Remaining: 16 << 10}
	cmd.Stderr = &limitedWriter{Writer: io.Discard, Remaining: 16 << 10}
	err := cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || strings.TrimSpace(out.String()) != "csql "+strings.TrimPrefix(tag, "v") {
		return fmt.Errorf("更新ファイルの版・起動確認に失敗しました。旧版を維持します")
	}
	return ctx.Err()
}
func (p *preparedSelfUpdate) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	if p.unlock != nil {
		p.unlock()
		p.unlock = nil
	}
	if !p.retained {
		return os.RemoveAll(p.StageDir)
	}
	return nil
}

type selfUpdateFileOps struct {
	Rename       func(string, string) error
	WriteReceipt func(string, binaryReceipt) error
}

// setupと同じロックを、接続停止の前に取得して確認内容を再検査する。
func (p *preparedSelfUpdate) Acquire(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return fmt.Errorf("更新準備は取り消されています")
	}
	if p.unlock != nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	lock := filepath.Join(filepath.Dir(p.Target.Receipt.Path), ".path-lock")
	if err := pathNoLinks(lock); err != nil {
		return err
	}
	if err := os.Mkdir(lock, 0700); err != nil {
		return fmt.Errorf("別のsetupまたはupdateが実行中です。完了後に再試行してください")
	}
	info, _ := os.Lstat(lock)
	release := func() {
		now, err := os.Lstat(lock)
		if err == nil && info != nil && os.SameFile(now, info) {
			_ = os.Remove(lock)
		}
	}
	if err := p.unchanged(); err != nil {
		release()
		return err
	}
	if err := ctx.Err(); err != nil {
		release()
		return err
	}
	p.unlock = release
	return nil
}
func (p *preparedSelfUpdate) unchanged() error {
	if err := pathUnchanged(p.Target.Binary, downloadLimit); err != nil {
		return err
	}
	if err := pathUnchanged(p.Target.Receipt, settingsLimit); err != nil {
		return err
	}
	b, _, err := pathRead(p.Payload, downloadLimit)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	if int64(len(b)) != p.Offer.Size || hex.EncodeToString(sum[:]) != p.Offer.SHA256 {
		return fmt.Errorf("検証後に更新ファイルが変更されました")
	}
	return nil
}

type selfUpdateState struct {
	Schema                                               int
	ManagedPath, Directory, OldHash, NewHash, Tag, Phase string
}

func selfUpdatePrevious(p *preparedSelfUpdate) string {
	return filepath.Join(p.StageDir, "previous-csql"+filepath.Ext(p.Target.Path))
}
func (a *App) updateRename(old, next string) error {
	if a.SelfUpdateOps != nil && a.SelfUpdateOps.Rename != nil {
		return a.SelfUpdateOps.Rename(old, next)
	}
	return os.Rename(old, next)
}

// ファイルと管理記録を一組で確定し、途中失敗では取消済みctxに依存せず戻す。
func (a *App) applySelfUpdate(ctx context.Context, p *preparedSelfUpdate) (resultErr error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.unlock == nil {
		return fmt.Errorf("更新の排他を取得していません")
	}
	defer func() {
		if p.unlock != nil {
			p.unlock()
			p.unlock = nil
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.unchanged(); err != nil {
		return err
	}
	oldHash := sha256.Sum256(p.Target.Binary.Before)
	state := selfUpdateState{1, p.Target.Path, filepath.Base(p.StageDir), hex.EncodeToString(oldHash[:]), p.Offer.SHA256, p.Offer.Tag, "replacing"}
	statePath := filepath.Join(p.StageDir, "update-state.json")
	if err := writeJSON(statePath, state); err != nil {
		return err
	}
	previous := selfUpdatePrevious(p)
	if err := os.WriteFile(filepath.Join(p.StageDir, "previous-receipt.json"), p.Target.Receipt.Before, 0600); err != nil {
		return err
	}
	moved := false
	if a.OS == "windows" {
		if err := a.updateRename(p.Target.Path, previous); err != nil {
			return fmt.Errorf("旧版を退避できません。他のcsqlを終了して再試行してください: %w", err)
		}
		moved = true
	} else if err := os.WriteFile(previous, p.Target.Binary.Before, p.Target.Binary.Mode); err != nil {
		return err
	}
	p.retained = true
	rollback := func(cause error) error {
		var restoreErr error
		if moved {
			if a.OS == "windows" {
				if _, err := os.Lstat(p.Target.Path); err == nil {
					restoreErr = a.updateRename(p.Target.Path, filepath.Join(p.StageDir, "failed-new.exe"))
				}
			}
			if restoreErr == nil {
				restoreErr = a.updateRename(previous, p.Target.Path)
			}
		}
		now, err := pathReadChange(p.Target.Receipt.Path, settingsLimit)
		if err == nil {
			now.After = p.Target.Receipt.Before
			now.NewMode = p.Target.Receipt.Mode
			err = pathApply(now, settingsLimit, false, io.Discard)
		}
		if restoreErr == nil {
			restoreErr = err
		}
		if restoreErr != nil {
			return fmt.Errorf("更新に失敗し、旧版/管理記録の復元も完了できませんでした。\n原因: %v\n復元: %v\n旧版: %s\n新版/作業先: %s\n管理記録: %s\n次の操作: 現在の配置を退避し、旧版の公式セットアップで復旧してください（%s setup --path）", cause, restoreErr, previous, p.StageDir, p.Target.Receipt.Path, shellQuote(previous))
		}
		p.retained = false
		return fmt.Errorf("更新に失敗しました。旧版と管理記録を復元しました: %w", cause)
	}
	if err := a.updateRename(p.Payload, p.Target.Path); err != nil {
		return rollback(err)
	}
	moved = true
	if err := a.checkSelfUpdateBinary(ctx, p.Target.Path, p.Offer.Tag); err != nil {
		return rollback(err)
	}
	receipt := binaryReceipt{p.Offer.SHA256}
	write := func(path string, r binaryReceipt) error { return writeJSON(path, r) }
	if a.SelfUpdateOps != nil && a.SelfUpdateOps.WriteReceipt != nil {
		write = a.SelfUpdateOps.WriteReceipt
	}
	if err := write(p.Target.Receipt.Path, receipt); err != nil {
		return rollback(err)
	}
	state.Phase = "committed"
	if err := writeJSON(statePath, state); err != nil {
		return rollback(err)
	}
	a.cleanupSelfUpdateBackups(p)
	return nil
}

// 所有記録と旧版hashが一致する、確定済みの旧バックアップだけを整理する。
func readSelfUpdateState(dir, target string) (selfUpdateState, bool) {
	var s selfUpdateState
	b, _, err := pathRead(filepath.Join(dir, "update-state.json"), settingsLimit)
	if err != nil || json.Unmarshal(b, &s) != nil || s.Schema != 1 || s.ManagedPath != target || s.Directory != filepath.Base(dir) || !strings.HasPrefix(s.Directory, ".csql-update-") {
		return s, false
	}
	for _, hash := range []string{s.OldHash, s.NewHash} {
		decoded, e := hex.DecodeString(hash)
		if e != nil || len(decoded) != 32 {
			return s, false
		}
	}
	if _, ok := parseReleaseVersion(s.Tag); !ok {
		return s, false
	}
	return s, true
}
func (a *App) cleanupSelfUpdateBackups(current *preparedSelfUpdate) {
	dirs, _ := filepath.Glob(filepath.Join(filepath.Dir(current.Target.Path), ".csql-update-*"))
	for _, dir := range dirs {
		if dir == current.StageDir {
			continue
		}
		s, ok := readSelfUpdateState(dir, current.Target.Path)
		if !ok || s.Phase != "committed" {
			continue
		}
		previous := filepath.Join(dir, "previous-csql"+filepath.Ext(current.Target.Path))
		b, _, err := pathRead(previous, downloadLimit)
		sum := sha256.Sum256(b)
		if err != nil || hex.EncodeToString(sum[:]) != s.OldHash {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 3 {
			continue
		}
		known := true
		for _, e := range entries {
			if e.Name() != filepath.Base(previous) && e.Name() != "update-state.json" && e.Name() != "previous-receipt.json" {
				known = false
			}
		}
		if !known {
			continue
		}
		// Windowsで旧プロセスが使用中なら記録も残し、次回に再検査できるようにする。
		if os.Remove(previous) != nil {
			continue
		}
		_ = os.Remove(filepath.Join(dir, "previous-receipt.json"))
		_ = os.Remove(filepath.Join(dir, "update-state.json"))
		_ = os.Remove(dir)
	}
}
func selfUpdateRecoveryHint(target string) string {
	dirs, _ := filepath.Glob(filepath.Join(filepath.Dir(target), ".csql-update-*"))
	for _, dir := range dirs {
		s, ok := readSelfUpdateState(dir, target)
		if !ok {
			continue
		}
		old := filepath.Join(dir, "previous-csql"+filepath.Ext(target))
		b, _, err := pathRead(old, downloadLimit)
		sum := sha256.Sum256(b)
		if err == nil && hex.EncodeToString(sum[:]) == s.OldHash {
			return "\n旧版の復旧先: " + old + "\n次の操作: " + shellQuote(old) + " setup --path（現在の配置を退避してから公式セットアップで復旧してください）"
		}
	}
	return ""
}
func (a *App) UpdateSelf(ctx context.Context) error {
	if !a.TTY {
		return usageError{fmt.Errorf("csql updateは対話端末から実行してください")}
	}
	target, err := a.inspectSelfUpdateTarget()
	if err != nil {
		return err
	}
	offer, err := a.fetchSelfUpdate(ctx)
	if err != nil {
		return err
	}
	if offer == nil {
		fmt.Fprintf(a.stdout(), "csql %s は最新版です。変更はありません。\n", version)
		return nil
	}
	fmt.Fprintf(a.stdout(), "csql %s → %s\n配布元: %s\n更新先: %s\n", version, offer.Tag, offer.URL, target.Path)
	ok, err := a.confirm(ctx, "この内容でcsqlを更新しますか？ [y/N]")
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(a.stdout(), "更新を取り消しました")
		return nil
	}
	p, err := a.prepareSelfUpdate(ctx, *offer, target, nil)
	if err != nil {
		return err
	}
	defer p.Close()
	if err = p.Acquire(ctx); err != nil {
		return err
	}
	if err = a.applySelfUpdate(ctx, p); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout(), "csql %s に更新しました。次回起動から利用できます。\n", strings.TrimPrefix(offer.Tag, "v"))
	return nil
}
