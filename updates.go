package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const updatePage = "https://github.com/y-onishi-0024/cloud-sql-proxy-cli/releases/latest"

type updateChecked struct{ latest string }
type updateCache struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
}

// 更新確認だけで接続を遅らせず、失敗は診断や接続のエラーへ混ぜない。
func (a *App) checkForUpdate(ctx context.Context) string {
	if !a.TTY || a.Env("CSQL_NO_UPDATE_CHECK") == "1" || ctx.Err() != nil {
		return ""
	}
	file := filepath.Join(a.Paths.CacheDir, "update-check.json")
	var cached updateCache
	cacheOK := false
	// 更新キャッシュは全文を検証し、末尾のごみや別JSONで確認を省略しない。
	if info, err := os.Lstat(file); err == nil && info.Mode().IsRegular() && info.Size() <= 4<<10 {
		if f, err := os.Open(file); err == nil {
			data, readErr := io.ReadAll(io.LimitReader(f, (4<<10)+1))
			f.Close()
			cacheOK = readErr == nil && len(data) <= 4<<10 && json.Unmarshal(data, &cached) == nil
		}
	}
	_, valid := parseReleaseVersion(cached.Latest)
	cacheOK = cacheOK && valid && strings.HasPrefix(cached.Latest, "v") && !cached.CheckedAt.IsZero()
	notice := ""
	if cacheOK && newerRelease(cached.Latest) {
		notice = cached.Latest
	}
	now := a.Now()
	age := now.Sub(cached.CheckedAt)
	if cacheOK && age >= 0 && age < 24*time.Hour {
		return notice
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	client := &http.Client{}
	if a.HTTP != nil {
		*client = *a.HTTP
	}
	if client.Timeout == 0 || client.Timeout > 2*time.Second {
		client.Timeout = 2 * time.Second
	}
	// 認証やCookieを必要とせず、別ホストへの誘導も受け入れない。
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/y-onishi-0024/cloud-sql-proxy-cli/releases/latest", nil)
	if err != nil {
		return notice
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "csql/"+version)
	response, err := client.Do(req)
	if err != nil {
		return notice
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return notice
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (256<<10)+1))
	if err != nil || len(data) > 256<<10 || ctx.Err() != nil {
		return notice
	}
	var release struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if json.Unmarshal(data, &release) != nil || release.Draft || release.Prerelease {
		return notice
	}
	if _, ok := parseReleaseVersion(release.Tag); !ok {
		return notice
	}
	latest := "v" + strings.TrimPrefix(release.Tag, "v")
	// キャッシュ不能でも案内を表示する。通信失敗時は前回の確認済み案内を保持する。
	_ = writeJSON(file, updateCache{CheckedAt: now, Latest: latest})
	if newerRelease(latest) {
		return latest
	}
	return ""
}

// 正式版の3要素だけを許可し、外部の任意文字列をそのまま端末へ描画しない。
func parseReleaseVersion(tag string) ([3]uint64, bool) {
	var result [3]uint64
	parts := strings.Split(strings.TrimPrefix(tag, "v"), ".")
	if len(parts) != 3 {
		return result, false
	}
	for i, part := range parts {
		if part == "" || len(part) > 1 && part[0] == '0' {
			return result, false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return result, false
			}
		}
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return result, false
		}
		result[i] = n
	}
	return result, true
}

func newerRelease(tag string) bool {
	latest, ok := parseReleaseVersion(tag)
	current, currentOK := parseReleaseVersion(version)
	if !ok || !currentOK {
		return false
	}
	for i := range latest {
		if latest[i] != current[i] {
			return latest[i] > current[i]
		}
	}
	return false
}
