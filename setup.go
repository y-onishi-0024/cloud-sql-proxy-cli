package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const downloadLimit int64 = 256 << 20
const extractLimit int64 = 1 << 30
const maxEntries = 100000

func defaultArtifacts(goos, arch string) []Artifact {
	type row struct {
		sdk, sdkHash, proxy, proxyHash string
		needsPython                    bool
	}
	rows := map[string]row{
		"darwin/amd64":  {"google-cloud-cli-589.0.0-darwin-x86_64.tar.gz", "b49e8cb9d348267d46fade7b9c916a1aed7ce0ec07e142ce41cc0d24a95a9651", "cloud-sql-proxy.darwin.amd64", "67a38444d03fe77a8b11036f96fe5d2379640088bb6e338737fb48d97c58cf48", true},
		"darwin/arm64":  {"google-cloud-cli-589.0.0-darwin-arm.tar.gz", "71b37ca07ab0f9bc43898e42c913e10f143166230ab31e0bbea3632dbc77b010", "cloud-sql-proxy.darwin.arm64", "fd374545705f2aa687d8e525a2efcbb9d356cc8af60d4dfa1a330cb59a7f9450", true},
		"linux/amd64":   {"google-cloud-cli-589.0.0-linux-x86_64.tar.gz", "817026aec0adea0e2ee8ed3a1ba3eabfe9e5177581fbe16595a8fe495a601300", "cloud-sql-proxy.linux.amd64", "fae2766aac9d614a2bdef2f2a7778f3d054f3acd5ff07a81a9e300bd471512eb", false},
		"linux/arm64":   {"google-cloud-cli-589.0.0-linux-arm.tar.gz", "c7b9abef0649e0fccc70bf2aaaa031a0aa6202863e45fabe938698dc93d4934c", "cloud-sql-proxy.linux.arm64", "da49fd73b0d33c0ab8e5dfc19dfb104cdd969ebd355eef8fd3b079c20a723c82", true},
		"windows/amd64": {"google-cloud-sdk-589.0.0-windows-x86_64-bundled-python.zip", "b9c2f4f296154f55e8a31a6979106573bc1ad8691041d4a32ea90c1834fdf995", "cloud-sql-proxy.x64.exe", "01ec54c69dbb8118c0b181c33ff1469a5cdb2dddb49eb86b0b173f6c55dea0bc", false},
	}
	r, ok := rows[goos+"/"+arch]
	if !ok {
		return nil
	}
	format := "tar.gz"
	if strings.HasSuffix(r.sdk, ".zip") {
		format = "zip"
	}
	return []Artifact{{Name: "gcloud", Version: "589.0.0", File: r.sdk, URL: "https://storage.googleapis.com/cloud-sdk-release/" + r.sdk, SHA256: r.sdkHash, Format: format, PythonRequired: r.needsPython}, {Name: "cloud-sql-proxy", Version: "2.24.1", File: r.proxy, URL: "https://storage.googleapis.com/cloud-sql-connectors/cloud-sql-proxy/v2.24.1/" + r.proxy, SHA256: r.proxyHash, Format: "binary"}}
}

func (a *App) Setup(ctx context.Context, install bool) error {
	_, err := a.setup(ctx, install)
	return err
}
func (a *App) setup(ctx context.Context, install bool) (bool, error) {
	fmt.Fprintln(a.stdout(), "公式導入手順: https://docs.cloud.google.com/sdk/docs/install-sdk\nProxy: https://github.com/GoogleCloudPlatform/cloud-sql-proxy#installation")
	if a.OS == "darwin" {
		fmt.Fprintln(a.stdout(), "Homebrew利用時: brew install --cask gcloud-cli\n                 brew install cloud-sql-proxy")
	}
	artifacts := a.Artifacts
	if artifacts == nil {
		artifacts = defaultArtifacts(a.OS, a.Arch)
	}
	var missing []Artifact
	for _, f := range artifacts {
		if _, err := a.resolve(ctx, f.Name); err != nil {
			missing = append(missing, f)
			fmt.Fprintf(a.stdout(), "導入対象: %s %s\n配布元: %s\n保存先: %s\nSHA-256: %s\n", f.Name, f.Version, f.URL, filepath.Join(a.Paths.ToolsDir, f.Name, f.Version), f.SHA256)
		} else {
			fmt.Fprintf(a.stdout(), "%s: 既存の導入を利用します\n", f.Name)
		}
	}
	a.loginGuide(ctx)
	if !install {
		fmt.Fprintf(a.stdout(), "不足コマンドは csql setup --install で確認後に導入できます。csqlと管理版ツールのPATH設定: csql setup --path（初回は ./dist/csql setup --install --path）。保存先: %s。設定後は端末を開き直してください。\n", filepath.Join(a.Paths.ConfigDir, "bin"))
		return true, nil
	}
	if len(artifacts) == 0 {
		return false, fmt.Errorf("%s/%s の自動導入は非対応です。公式手順で手動導入してください", a.OS, a.Arch)
	}
	if len(missing) == 0 {
		fmt.Fprintln(a.stdout(), "追加インストールは不要です")
		return true, nil
	}
	ok, err := a.confirm(ctx, "実行内容: 取得→SHA-256照合→ユーザー専用領域へ展開・配置→起動確認。シェル設定・ログイン・GCP設定は変更しません。\nインストールしますか？ [y/N]")
	if err != nil {
		return false, err
	}
	if !ok {
		fmt.Fprintln(a.stdout(), "インストールを取り消しました")
		return false, nil
	}
	if err := secureDir(a.Paths.ToolsDir); err != nil {
		return false, err
	}
	lock := filepath.Join(a.Paths.ToolsDir, ".install-lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		return false, fmt.Errorf("別のインストールが進行中、またはロックを作成できません: %w", err)
	}
	defer os.Remove(lock)
	var errs []error
	for _, f := range missing {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		if err := a.installArtifact(ctx, f); err != nil {
			fmt.Fprintf(a.stderr(), "%s 導入失敗: %s\n", f.Name, err)
			errs = append(errs, err)
		} else {
			fmt.Fprintf(a.stdout(), "%s %s の導入・起動確認が完了しました\n", f.Name, f.Version)
		}
	}
	a.loginGuide(ctx)
	return len(errs) == 0, errors.Join(errs...)
}

func (a *App) loginGuide(ctx context.Context) {
	cmd := "gcloud"
	if e, err := a.resolve(ctx, "gcloud"); err == nil {
		parts := append([]string{e.Path}, e.PrefixArgs...)
		for i, s := range parts {
			parts[i] = fmt.Sprintf("%q", s)
		}
		cmd = strings.Join(parts, " ")
		if a.OS == "windows" {
			cmd = "& " + cmd
		}
	}
	fmt.Fprintf(a.stdout(), "検索用ログイン（利用者が実行）: %s auth login\nProxy用ADC（利用者が実行）: %s auth application-default login\n導入後の診断: csql doctor\n", cmd, cmd)
}

func (a *App) installArtifact(ctx context.Context, f Artifact) error {
	if f.PythonRequired {
		if _, err := a.python(ctx); err != nil {
			return err
		}
	}
	base := filepath.Join(a.Paths.ToolsDir, f.Name)
	if err := secureDir(base); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(base, ".install-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	archive := filepath.Join(stage, "download")
	if err := a.download(ctx, f, archive); err != nil {
		return err
	}
	payload := filepath.Join(stage, "payload")
	if err := os.Mkdir(payload, 0700); err != nil {
		return err
	}
	rel := "cloud-sql-proxy"
	if a.OS == "windows" {
		rel += ".exe"
	}
	if f.Format == "binary" {
		if err := os.Rename(archive, filepath.Join(payload, rel)); err != nil {
			return err
		}
		if err := os.Chmod(filepath.Join(payload, rel), 0700); err != nil {
			return err
		}
	} else {
		if err := extractArchive(ctx, archive, payload, f.Format); err != nil {
			return err
		}
		rel = filepath.Join("google-cloud-sdk", "bin", "gcloud")
		if a.OS == "windows" {
			rel += ".cmd"
		}
	}
	_, v, err := a.checkPath(ctx, filepath.Join(payload, rel), f.Name)
	if err != nil {
		return err
	}
	if !strings.Contains(v, f.Version) {
		return fmt.Errorf("配布物の版が一致しません")
	}
	dest := filepath.Join(base, f.Version)
	if _, err := os.Lstat(dest); err == nil {
		// 途中で残った同じ版は上書きせず、利用可能なときだけ再利用する。
		_, v, err := a.checkPath(ctx, filepath.Join(dest, rel), f.Name)
		if err != nil || !strings.Contains(v, f.Version) {
			return fmt.Errorf("既存の版ディレクトリが利用不能です。手動で確認してください: %s", dest)
		}
	} else if !os.IsNotExist(err) {
		return err
	} else if err := os.Rename(payload, dest); err != nil {
		return err
	}
	return writeJSON(filepath.Join(base, "installed.json"), installedTool{Version: f.Version, RelativePath: filepath.ToSlash(filepath.Join(f.Version, rel))})
}

func (a *App) download(ctx context.Context, f Artifact, dest string) error {
	u, err := url.Parse(f.URL)
	if err != nil {
		return err
	}
	if !(u.Scheme == "https" && u.Host == "storage.googleapis.com") && !a.AllowTestHTTP {
		return fmt.Errorf("許可されていない配布元です")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	client := &http.Client{}
	if a.HTTP != nil {
		*client = *a.HTTP
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 || req.URL.Host != u.Host || req.URL.Scheme != u.Scheme {
			return fmt.Errorf("許可されていないリダイレクトです")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return err
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	if res.ContentLength > downloadLimit {
		return fmt.Errorf("配布物が容量上限を超えています")
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(res.Body, downloadLimit+1))
	closeErr := out.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if n > downloadLimit {
		return fmt.Errorf("配布物が容量上限を超えています")
	}
	if hex.EncodeToString(h.Sum(nil)) != f.SHA256 {
		return fmt.Errorf("SHA-256が一致しません")
	}
	return nil
}

type archiveLink struct {
	name, target string
	hard         bool
}

func extractArchive(ctx context.Context, source, dest, format string) error {
	r, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer r.Close()
	var size int64
	count := 0
	var links []archiveLink
	entry := func(name string, mode os.FileMode, n int64, body io.Reader, link string, hard bool) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		count++
		if count > maxEntries {
			return fmt.Errorf("展開件数が上限を超えています")
		}
		name = strings.TrimSuffix(name, "/")
		if !safeRelative(name) {
			return fmt.Errorf("不正な展開パス: %s", cleanText(name))
		}
		if link != "" {
			target := link
			if !hard {
				target = path.Join(path.Dir(name), link)
			}
			if !safeRelative(target) || path.IsAbs(link) || strings.Contains(link, "\\") || strings.Contains(link, ":") {
				return fmt.Errorf("展開先を逸脱するリンクです")
			}
			links = append(links, archiveLink{name, link, hard})
			return nil
		}
		if mode.IsDir() {
			return r.MkdirAll(name, 0700)
		}
		if !mode.IsRegular() {
			return fmt.Errorf("非対応の展開エントリーです")
		}
		if n < 0 || n > downloadLimit || n > extractLimit-size {
			return fmt.Errorf("展開容量が上限を超えています")
		}
		size += n
		if err := r.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return err
		}
		perm := os.FileMode(0600)
		if mode&0111 != 0 {
			perm = 0700
		}
		f, err := r.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
		if err != nil {
			return err
		}
		copied, err := io.CopyN(f, body, n)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if copied != n {
			return io.ErrUnexpectedEOF
		}
		return closeErr
	}
	if format == "tar.gz" {
		f, err := os.Open(source)
		if err != nil {
			return err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			switch h.Typeflag {
			case tar.TypeReg, tar.TypeRegA, tar.TypeDir, tar.TypeSymlink, tar.TypeLink:
			default:
				return fmt.Errorf("非対応のtarエントリーです")
			}
			if err := entry(h.Name, h.FileInfo().Mode(), h.Size, tr, h.Linkname, h.Typeflag == tar.TypeLink); err != nil {
				return err
			}
		}
	} else if format == "zip" {
		zr, err := zip.OpenReader(source)
		if err != nil {
			return err
		}
		defer zr.Close()
		for _, f := range zr.File {
			if f.UncompressedSize64 > uint64(downloadLimit) {
				return fmt.Errorf("展開容量が上限を超えています")
			}
			body, err := f.Open()
			if err != nil {
				return err
			}
			link := ""
			if f.Mode()&os.ModeSymlink != 0 {
				b, e := io.ReadAll(io.LimitReader(body, 4097))
				if e != nil || len(b) > 4096 {
					body.Close()
					return fmt.Errorf("不正なリンクです")
				}
				link = string(b)
			}
			err = entry(f.Name, f.Mode(), int64(f.UncompressedSize64), body, link, false)
			body.Close()
			if err != nil {
				return err
			}
		}
	} else {
		return fmt.Errorf("非対応のアーカイブ形式です")
	}
	// リンクは通常ファイルの後に配置し、展開中にリンクを経由する書込みを作らない。
	for _, l := range links {
		if err := r.MkdirAll(filepath.Dir(l.name), 0700); err != nil {
			return err
		}
		if l.hard {
			if err := r.Link(l.target, l.name); err != nil {
				return err
			}
		} else {
			if err := r.Symlink(l.target, l.name); err != nil {
				return err
			}
		}
	}
	return nil
}

// 確認を共有し、インストールとPATH設定の連続した回答を失わない。
func (a *App) confirm(ctx context.Context, prompt string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if a.ConfirmAction != nil {
		guiPrompt := strings.TrimSuffix(prompt, " [y/N]")
		fmt.Fprintln(a.stdout(), guiPrompt)
		ok, err := a.ConfirmAction(ctx, guiPrompt)
		if canceled := ctx.Err(); canceled != nil {
			return false, canceled
		}
		return ok, err
	}
	fmt.Fprintln(a.stdout(), prompt)
	if !a.TTY {
		return false, fmt.Errorf("非対話環境では変更しません。対話端末で実行してください")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if a.confirmation == nil {
		a.confirmation = bufio.NewReaderSize(a.In, 128)
	}
	reader := a.confirmation
	type answer struct {
		line string
		err  error
	}
	answers := make(chan answer, 1)
	go func() { line, err := reader.ReadSlice('\n'); answers <- answer{string(line), err} }()
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case r := <-answers:
		if r.err != nil && r.err != io.EOF {
			return false, r.err
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(r.line)) {
		case "y", "yes", "はい":
			return true, nil
		}
		return false, nil
	}
}
