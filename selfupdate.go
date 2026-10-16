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
		return t, fmt.Errorf("csqlの管理先を確認できません: %s", t.Path)
	}
	t.Receipt, err = pathReadChange(filepath.Join(a.Paths.ConfigDir, "binary-install.json"), settingsLimit)
	var receipt binaryReceipt
	hash := sha256.Sum256(t.Binary.Before)
	if err != nil || !t.Receipt.Exists || json.Unmarshal(t.Receipt.Before, &receipt) != nil || receipt.SHA256 != hex.EncodeToString(hash[:]) {
		return t, fmt.Errorf("csqlの管理記録が一致しません。公式セットアップで配置を確認してください\n%s", t.Path)
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
	if err := cmd.Run(); err != nil || strings.TrimSpace(out.String()) != "csql "+strings.TrimPrefix(tag, "v") {
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
