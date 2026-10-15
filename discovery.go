package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Instance struct{ Project, Name, Region, ConnectionName, DatabaseVersion string }
type ProjectFailure struct{ Project, Message string }
type DiscoveryResult struct {
	Instances []Instance
	Failures  []ProjectFailure
}
type instanceCache struct {
	Version int
	Key     string
	Fetched time.Time
	Result  DiscoveryResult
}

var resourceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

func (a *App) Discover(ctx context.Context, project string, refresh bool) (DiscoveryResult, error) {
	return a.discover(ctx, project, refresh, func(s string) { fmt.Fprintln(a.stderr(), s) })
}
func (a *App) discover(ctx context.Context, project string, refresh bool, notice func(string)) (DiscoveryResult, error) {
	if project != "" && !resourceName.MatchString(project) {
		return DiscoveryResult{}, usageError{fmt.Errorf("プロジェクトIDが不正です")}
	}
	g, err := a.resolve(ctx, "gcloud")
	if err != nil {
		return DiscoveryResult{}, err
	}
	key := a.cacheKey(ctx, g, project)
	cachePath := filepath.Join(a.Paths.CacheDir, key+".json")
	if key != "" && !refresh {
		var cached instanceCache
		if readJSONFile(cachePath, &cached) == nil && cached.Version == 1 && cached.Key == key {
			age := a.Now().Sub(cached.Fetched)
			if age >= 0 && age < 10*time.Minute && validCached(cached.Result) {
				return cached.Result, nil
			}
		}
	}
	projects := []string{project}
	if project == "" {
		out, err := a.query(ctx, g, "projects", "list", "--format=json", "--quiet")
		if err != nil {
			return DiscoveryResult{}, err
		}
		var rows []struct {
			ProjectID string `json:"projectId"`
		}
		if err := json.Unmarshal(out, &rows); err != nil {
			return DiscoveryResult{}, fmt.Errorf("プロジェクト一覧が不正なJSONです: %w", err)
		}
		projects = nil
		seen := map[string]bool{}
		for _, r := range rows {
			if !resourceName.MatchString(r.ProjectID) {
				return DiscoveryResult{}, fmt.Errorf("プロジェクト一覧に不正なIDがあります")
			}
			if !seen[r.ProjectID] {
				projects = append(projects, r.ProjectID)
				seen[r.ProjectID] = true
			}
		}
	}
	type result struct {
		project string
		rows    []Instance
		err     error
	}
	jobs := make(chan string)
	results := make(chan result, len(projects))
	var workers sync.WaitGroup
	// APIを最大4並列に制限し、すべての結果を集めて部分失敗を残す。
	for n := 0; n < 4; n++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for p := range jobs {
				out, err := a.query(ctx, g, "sql", "instances", "list", "--project="+p, "--format=json", "--quiet")
				var rows []Instance
				if err == nil {
					rows, err = parseInstances(p, out)
				}
				results <- result{p, rows, err}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, p := range projects {
			select {
			case jobs <- p:
			case <-ctx.Done():
				return
			}
		}
	}()
	workers.Wait()
	close(results)
	r := DiscoveryResult{}
	for res := range results {
		if res.err != nil {
			r.Failures = append(r.Failures, ProjectFailure{res.project, res.err.Error()})
		} else {
			r.Instances = append(r.Instances, res.rows...)
		}
	}
	if ctx.Err() != nil {
		return r, ctx.Err()
	}
	sort.Slice(r.Instances, func(i, j int) bool {
		x, y := r.Instances[i], r.Instances[j]
		if x.Project != y.Project {
			return x.Project < y.Project
		}
		if x.Name != y.Name {
			return x.Name < y.Name
		}
		return x.ConnectionName < y.ConnectionName
	})
	sort.Slice(r.Failures, func(i, j int) bool { return r.Failures[i].Project < r.Failures[j].Project })
	if len(projects) > 0 && len(r.Failures) == len(projects) {
		return r, fmt.Errorf("すべてのプロジェクトで検索に失敗しました: %v", r.Failures)
	}
	if key != "" {
		if err := writeJSON(cachePath, instanceCache{1, key, a.Now(), r}); err != nil {
			notice(fmt.Sprintf("キャッシュを書き込めません（今回の検索結果を利用）: %s", err))
		}
	}
	return r, nil
}
func (a *App) query(ctx context.Context, e Executable, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, a.QueryTimeout)
	defer cancel()
	out, err := a.Runner.Run(ctx, e, args)
	if err != nil && ctx.Err() == nil {
		project := ""
		for _, arg := range args {
			if strings.HasPrefix(arg, "--project=") {
				project = strings.TrimPrefix(arg, "--project=")
			}
		}
		return nil, diagnosticError{Context: FailureContext{Source: "gcloud", Operation: "インスタンス検索", Project: project, GcloudCommand: a.commandDisplay(e)}, Err: err}
	}
	return out, err
}
func parseInstances(project string, out []byte) ([]Instance, error) {
	var rows []struct{ Name, Project, Region, ConnectionName, DatabaseVersion string }
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("インスタンス一覧が不正なJSONです: %w", err)
	}
	result := make([]Instance, 0, len(rows))
	seen := map[string]bool{}
	for _, r := range rows {
		v := Instance{project, r.Name, r.Region, r.ConnectionName, r.DatabaseVersion}
		if r.Project != "" && r.Project != project || !validInstance(v) {
			return nil, fmt.Errorf("インスタンスの接続名または必須項目が不正です")
		}
		if !seen[v.ConnectionName] {
			result = append(result, v)
			seen[v.ConnectionName] = true
		}
	}
	return result, nil
}
func validInstance(v Instance) bool {
	parts := strings.Split(v.ConnectionName, ":")
	return len(parts) == 3 && parts[0] == v.Project && parts[1] == v.Region && parts[2] == v.Name && resourceName.MatchString(v.Project) && resourceName.MatchString(v.Region) && resourceName.MatchString(v.Name) && v.DatabaseVersion != "" && len(v.DatabaseVersion) < 100 && v.DatabaseVersion == cleanText(v.DatabaseVersion)
}
func validCached(r DiscoveryResult) bool {
	for _, v := range r.Instances {
		if !validInstance(v) {
			return false
		}
	}
	for _, f := range r.Failures {
		if !resourceName.MatchString(f.Project) {
			return false
		}
	}
	return true
}
func MatchInstances(rows []Instance, q string) []Instance {
	q = strings.ToLower(q)
	if q == "" {
		return rows
	}
	var exact, partial []Instance
	for _, r := range rows {
		n := strings.ToLower(r.Name)
		if n == q {
			exact = append(exact, r)
		} else if strings.Contains(n, q) {
			partial = append(partial, r)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return partial
}

func (a *App) cacheKey(ctx context.Context, g Executable, project string) string {
	out, err := a.run(ctx, g, "config", "list", "--format=json", "--quiet")
	if err != nil {
		return ""
	}
	var props map[string]map[string]any
	if json.Unmarshal(out, &props) != nil {
		return ""
	}
	// universeはAPI接続先を変更するため、未対応ドメインの結果を再利用しない。
	if value, exists := props["core"]["universe_domain"]; exists {
		domain, ok := value.(string)
		if !ok || domain != "" && domain != "googleapis.com" {
			return ""
		}
	}
	if domain := a.Env("CLOUDSDK_CORE_UNIVERSE_DOMAIN"); domain != "" && domain != "googleapis.com" {
		return ""
	}
	// 認証の値は保存せず、対応できない認証方式・API差替えはキャッシュを利用しない。
	for section, values := range props {
		if section == "api_endpoint_overrides" && len(values) > 0 {
			return ""
		}
		if section == "auth" {
			for key, value := range values {
				if key != "impersonate_service_account" && value != nil && fmt.Sprint(value) != "" && fmt.Sprint(value) != "false" {
					return ""
				}
			}
		}
	}
	for _, env := range os.Environ() {
		name, _, _ := strings.Cut(env, "=")
		if strings.HasPrefix(name, "CLOUDSDK_API_ENDPOINT_OVERRIDES_") || strings.HasPrefix(name, "CLOUDSDK_AUTH_") {
			if name != "CLOUDSDK_AUTH_IMPERSONATE_SERVICE_ACCOUNT" && a.Env(name) != "" {
				return ""
			}
		}
	}
	account, _ := props["core"]["account"].(string)
	if account == "" {
		return ""
	}
	impersonate, _ := props["auth"]["impersonate_service_account"].(string)
	active := a.Env("CLOUDSDK_ACTIVE_CONFIG_NAME")
	if active == "" {
		out, err = a.run(ctx, g, "config", "configurations", "list", "--filter=is_active:true", "--format=value(name)", "--quiet")
		if err != nil {
			return ""
		}
		active = strings.TrimSpace(string(out))
		if active == "" {
			return ""
		}
	}
	data, _ := json.Marshal([]string{a.gcloudDir(), active, account, impersonate, project})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
