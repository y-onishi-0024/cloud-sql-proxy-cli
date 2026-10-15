package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func discoveryApp(t *testing.T, sql func(context.Context, string) ([]byte, error)) (*App, *atomic.Int32) {
	a, _, _ := testApp(t)
	a.LookPath = func(n string) (string, error) {
		if n == "gcloud" {
			return "/fake/gcloud", nil
		}
		return "", fmt.Errorf("未導入")
	}
	calls := &atomic.Int32{}
	a.Runner = runnerFunc(func(ctx context.Context, e Executable, args []string) ([]byte, error) {
		s := strings.Join(args, " ")
		switch {
		case s == "--version":
			return []byte("Google Cloud SDK 589.0.0"), nil
		case strings.HasPrefix(s, "config list"):
			return []byte(`{"core":{"account":"test@example.invalid"}}`), nil
		case strings.Contains(s, "configurations list"):
			return []byte("default"), nil
		case strings.HasPrefix(s, "projects list"):
			return []byte(`[{"projectId":"alpha"},{"projectId":"beta"}]`), nil
		case strings.HasPrefix(s, "sql instances list"):
			calls.Add(1)
			for _, arg := range args {
				if strings.HasPrefix(arg, "--project=") {
					return sql(ctx, strings.TrimPrefix(arg, "--project="))
				}
			}
		}
		return nil, fmt.Errorf("想定外: %v", args)
	})
	return a, calls
}

func TestCacheRejectsCustomUniverse(t *testing.T) {
	for _, source := range []string{"property", "environment", "property-sentinel", "property-null"} {
		t.Run(source, func(t *testing.T) {
			var changed atomic.Bool
			a, calls := discoveryApp(t, func(_ context.Context, p string) ([]byte, error) {
				if changed.Load() {
					return sqlJSON(p, "alternate-db"), nil
				}
				return sqlJSON(p, "default-db"), nil
			})
			original := a.Runner
			a.Runner = runnerFunc(func(c context.Context, e Executable, args []string) ([]byte, error) {
				if strings.HasPrefix(strings.Join(args, " "), "config list") {
					var domain any = "googleapis.com"
					if strings.HasPrefix(source, "property") && changed.Load() {
						domain = "alternate.invalid"
						if source == "property-sentinel" {
							domain = "<nil>"
						}
						if source == "property-null" {
							domain = nil
						}
					}
					return json.Marshal(map[string]any{"core": map[string]any{"account": "test@example.invalid", "universe_domain": domain}})
				}
				return original.Run(c, e, args)
			})
			a.Env = func(n string) string {
				if source == "environment" && changed.Load() && n == "CLOUDSDK_CORE_UNIVERSE_DOMAIN" {
					return "alternate.invalid"
				}
				return ""
			}
			if _, err := a.Discover(context.Background(), "alpha", false); err != nil {
				t.Fatal(err)
			}
			changed.Store(true)
			for i := 0; i < 2; i++ {
				r, err := a.Discover(context.Background(), "alpha", false)
				if err != nil || len(r.Instances) != 1 || r.Instances[0].Name != "alternate-db" {
					t.Fatalf("別接続先で旧キャッシュ: %+v %v", r, err)
				}
			}
			if calls.Load() != 3 {
				t.Fatalf("未対応universeをキャッシュしました: %d", calls.Load())
			}
		})
	}
}
func TestCLIEmptyListFails(t *testing.T) {
	a, _ := discoveryApp(t, func(_ context.Context, p string) ([]byte, error) { return []byte(`[]`), nil })
	if code := a.Run(context.Background(), []string{"list", "absent", "--project", "alpha"}); code != 1 {
		t.Fatalf("0件の終了コード: %d", code)
	}
}
func sqlJSON(project, name string) []byte {
	return []byte(fmt.Sprintf(`[{"name":%q,"region":"asia-northeast1","connectionName":%q,"databaseVersion":"POSTGRES_16"}]`, name, project+":asia-northeast1:"+name))
}

func TestDiscoveryPartialFailureAndCache(t *testing.T) {
	a, calls := discoveryApp(t, func(_ context.Context, p string) ([]byte, error) {
		if p == "beta" {
			return nil, fmt.Errorf("permission denied")
		}
		return sqlJSON(p, "db"), nil
	})
	r, err := a.Discover(context.Background(), "", false)
	if err != nil || len(r.Instances) != 1 || len(r.Failures) != 1 {
		t.Fatalf("部分検索: %+v %v", r, err)
	}
	r, err = a.Discover(context.Background(), "", false)
	if err != nil || len(r.Failures) != 1 || calls.Load() != 2 {
		t.Fatalf("キャッシュで警告が消えた／再検索: %+v %v calls=%d", r, err, calls.Load())
	}
	a.Now = func() time.Time { return time.Now().Add(11 * time.Minute) }
	if _, err = a.Discover(context.Background(), "", false); err != nil || calls.Load() != 4 {
		t.Fatalf("期限切れ更新: %v calls=%d", err, calls.Load())
	}
}
func TestCacheIdentityAndProject(t *testing.T) {
	a, calls := discoveryApp(t, func(_ context.Context, p string) ([]byte, error) { return sqlJSON(p, "db"), nil })
	a.Discover(context.Background(), "alpha", false)
	a.Discover(context.Background(), "alpha", false)
	if calls.Load() != 1 {
		t.Fatal("キャッシュなし")
	}
	original := a.Runner
	a.Runner = runnerFunc(func(c context.Context, e Executable, args []string) ([]byte, error) {
		if strings.HasPrefix(strings.Join(args, " "), "config list") {
			return []byte(`{"core":{"account":"other@example.invalid"}}`), nil
		}
		return original.Run(c, e, args)
	})
	a.Discover(context.Background(), "alpha", false)
	a.Discover(context.Background(), "beta", false)
	if calls.Load() != 3 {
		t.Fatalf("別認証・別projectを混用: %d", calls.Load())
	}
}
func TestDiscoveryInvalidAndCancelled(t *testing.T) {
	for _, mode := range []string{"bad-json", "bad-connection", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			a, _ := discoveryApp(t, func(ctx context.Context, p string) ([]byte, error) {
				switch mode {
				case "bad-json":
					return []byte("broken"), nil
				case "bad-connection":
					return sqlJSON("other", "db"), nil
				default:
					<-ctx.Done()
					return nil, ctx.Err()
				}
			})
			a.QueryTimeout = 10 * time.Millisecond
			if _, err := a.Discover(context.Background(), "alpha", true); err == nil {
				t.Fatal("検索失敗を成功扱い")
			}
		})
	}
}
func TestMatchExactPriorityAndDuplicate(t *testing.T) {
	rows := []Instance{{Name: "db", Project: "alpha"}, {Name: "db-prod", Project: "alpha"}, {Name: "db", Project: "beta"}}
	if got := MatchInstances(rows, "DB"); len(got) != 2 || got[1].Project != "beta" {
		t.Fatalf("完全一致優先: %+v", got)
	}
	if got := MatchInstances(rows, "prod"); len(got) != 1 {
		t.Fatal(got)
	}
	if got := MatchInstances(rows, "absent"); len(got) != 0 {
		t.Fatal(got)
	}
}
func TestCLIListWithoutProxy(t *testing.T) {
	a, _ := discoveryApp(t, func(_ context.Context, p string) ([]byte, error) { return sqlJSON(p, "db"), nil })
	if code := a.Run(context.Background(), []string{"list", "--project", "alpha"}); code != 0 {
		t.Fatalf("Proxyなしlist: %d", code)
	}
}

func TestDiscoveryBoundedConcurrency(t *testing.T) {
	var active, peak atomic.Int32
	a, _ := discoveryApp(t, func(_ context.Context, p string) ([]byte, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		return sqlJSON(p, "db"), nil
	})
	original := a.Runner
	a.Runner = runnerFunc(func(c context.Context, e Executable, args []string) ([]byte, error) {
		if strings.HasPrefix(strings.Join(args, " "), "projects list") {
			rows := []map[string]string{}
			for i := 0; i < 12; i++ {
				rows = append(rows, map[string]string{"projectId": fmt.Sprintf("project%d", i)})
			}
			return json.Marshal(rows)
		}
		return original.Run(c, e, args)
	})
	r, err := a.Discover(context.Background(), "", true)
	if err != nil || len(r.Instances) != 12 || peak.Load() > 4 || peak.Load() < 2 {
		t.Fatalf("並列検索: %d %+v %v", peak.Load(), r, err)
	}
}
