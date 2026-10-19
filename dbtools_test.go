package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

// 利用者のworkspaceを模した原本に対し、CLIから保存・再実行・競合を確認する。
func dbToolFixture(t *testing.T) (*App, Profile, string, string) {
	t.Helper()
	a, _, _ := testApp(t)
	a.OS, a.Arch, a.TTY = "darwin", "arm64", true
	p := Profile{Name: "dev", Instance: Instance{Project: "fixture", Name: "db", Region: "asia-northeast1", ConnectionName: "fixture:asia-northeast1:db", DatabaseVersion: "POSTGRES_18"}, Database: "業務/db?#", Username: "reader@team", Port: 15439}
	if err := a.writeProfiles([]Profile{p}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(a.Paths.ConfigDir)
	exe := filepath.Join(root, "DBeaver.app", "Contents", "MacOS", "dbeaver")
	if err := os.MkdirAll(filepath.Dir(exe), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	product := filepath.Join(root, "DBeaver.app", "Contents", "Eclipse", ".eclipseproduct")
	if err := os.MkdirAll(filepath.Dir(product), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(product, []byte("name=DBeaver\nid=org.jkiss.dbeaver.product\nversion=26.2.3\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspace6")
	store := filepath.Join(workspace, "General", ".dbeaver")
	if err := os.MkdirAll(store, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "data-sources.json"), []byte(`{"connections":{"unrelated":{"name":"existing"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	a.DBToolRunning = func(context.Context, dbToolTarget) (bool, error) { return false, nil }
	return a, p, exe, workspace
}
func TestDBToolRegistrationCLI(t *testing.T) {
	a, p, exe, w := dbToolFixture(t)
	args := []string{"profile", "register", p.Name, "--tool", "dbeaver", "--executable", exe, "--dbeaver-workspace", w}
	original := filepath.Join(w, "General", ".dbeaver", "data-sources.json")
	before, _ := os.ReadFile(original)
	if code := a.Run(context.Background(), append(args, "--dry-run")); code != 0 {
		t.Fatalf("dry-run: %s", a.Err)
	}
	if _, err := os.Stat(filepath.Join(a.Paths.ConfigDir, "db-tool-registrations.json")); !os.IsNotExist(err) {
		t.Fatal("dry-runで履歴を書いた")
	}
	if files, _ := filepath.Glob(filepath.Join(w, "General", ".dbeaver", "data-sources-csql-*.json")); len(files) != 0 {
		t.Fatal("dry-runで外部保存")
	}
	a.In = strings.NewReader("\n")
	if code := a.Run(context.Background(), args); code != 0 {
		t.Fatalf("既定取消: %s", a.Err)
	}
	a.In = strings.NewReader("y\n")
	if code := a.Run(context.Background(), args); code != 0 {
		t.Fatalf("追加: %s", a.Err)
	}
	files, _ := filepath.Glob(filepath.Join(w, "General", ".dbeaver", "data-sources-csql-*.json"))
	if len(files) != 1 {
		t.Fatalf("専用ファイル数 %d", len(files))
	}
	data, _ := os.ReadFile(files[0])
	var doc struct {
		Connections map[string]struct {
			Configuration map[string]any `json:"configuration"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	for _, c := range doc.Connections {
		if c.Configuration["user"] != p.Username || c.Configuration["database"] != p.Database || c.Configuration["port"] != "15439" {
			t.Fatalf("値が変化: %+v", c)
		}
	}
	after, _ := os.ReadFile(original)
	if string(before) != string(after) {
		t.Fatal("既存設定を変更")
	}
	a.In = strings.NewReader("y\n")
	if code := a.Run(context.Background(), args); code != 0 {
		t.Fatalf("同じ設定の再登録: %s", a.Err)
	}
	p.Port++
	if err := a.writeProfiles([]Profile{p}); err != nil {
		t.Fatal(err)
	}
	a.In = strings.NewReader("y\n")
	if code := a.Run(context.Background(), args); code == 0 {
		t.Fatal("Port変更を上書きした")
	}
	now, _ := os.ReadFile(files[0])
	if string(now) != string(data) {
		t.Fatal("競合時に変更")
	}
}
func TestDBToolRegistrationRevalidation(t *testing.T) {
	for _, change := range []string{"profile", "store", "running", "symlink"} {
		t.Run(change, func(t *testing.T) {
			a, p, exe, w := dbToolFixture(t)
			plan, err := a.planDBToolRegistration(context.Background(), p, dbToolOptions{Tool: "dbeaver", Executable: exe, DBeaverWorkspace: w})
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "profile":
				p.Port++
				err = a.writeProfiles([]Profile{p})
			case "store":
				err = os.WriteFile(filepath.Join(w, "General", ".dbeaver", "data-sources-other.json"), []byte(`{"connections":{}}`), 0600)
			case "running":
				a.DBToolRunning = func(context.Context, dbToolTarget) (bool, error) { return true, nil }
			case "symlink":
				err = os.Symlink(filepath.Join(w, "General", ".dbeaver", "data-sources.json"), filepath.Join(w, "General", ".dbeaver", "data-sources-link.json"))
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := a.applyDBToolRegistration(context.Background(), plan)
			if err == nil || result.Committed || result.Submitted {
				t.Fatalf("再検査を回避 %+v %v", result, err)
			}
			files, _ := filepath.Glob(filepath.Join(w, "General", ".dbeaver", "data-sources-csql-*.json"))
			if len(files) != 0 {
				t.Fatal("拒否時に追加")
			}
		})
	}
}

// 終了コードではなく登録先の変化を照合する。外部境界以外は実ファイルを使う。
func TestDBToolRegistrationOutcome(t *testing.T) {
	for _, mode := range []string{"success", "zero-without-save", "timeout-after-save", "receipt-failure"} {
		t.Run(mode, func(t *testing.T) {
			a, p, _, _ := dbToolFixture(t)
			root := filepath.Join(filepath.Dir(a.Paths.ConfigDir), "pgAdmin 4.app")
			python := filepath.Join(root, "Contents/Frameworks/Python.framework/Versions/Current/bin/python3")
			web := filepath.Join(root, "Contents/Resources/web")
			for _, dir := range []string{filepath.Dir(python), web} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			for path, content := range map[string]string{python: "fixture", filepath.Join(web, "setup.py"): "fixture", filepath.Join(web, "version.py"): "APP_RELEASE = 9\nAPP_REVISION = 18\n"} {
				if err := os.WriteFile(path, []byte(content), 0700); err != nil {
					t.Fatal(err)
				}
			}
			db := filepath.Join(filepath.Dir(a.Paths.ConfigDir), "pgadmin4.db")
			if err := os.WriteFile(db, []byte("synthetic-db"), 0600); err != nil {
				t.Fatal(err)
			}
			rows := map[string]map[string]any{"1": {"Name": "existing", "Host": "127.0.0.1", "Port": 15432, "MaintenanceDB": "other", "Username": "other"}}
			saved := false
			a.DBToolRun = func(ctx context.Context, exe string, args []string) ([]byte, error) {
				if args[3] == pgAdminSnapshotPython {
					data, _ := os.ReadFile(args[4])
					return nil, os.WriteFile(args[5], data, 0600)
				}
				if args[3] != pgAdminSetupPython {
					t.Fatal("未知の実行")
				}
				switch args[6] {
				case "load-servers":
					if mode != "zero-without-save" {
						rows["2"] = map[string]any{"Name": dbToolName(p), "Host": "127.0.0.1", "Port": p.Port, "MaintenanceDB": p.Database, "Username": p.Username}
						saved = true
						_ = os.WriteFile(db, []byte("saved-db"), 0600)
					}
					if mode == "timeout-after-save" {
						return nil, context.DeadlineExceeded
					}
				case "dump-servers":
					b, _ := json.Marshal(map[string]any{"Servers": rows})
					if err := os.WriteFile(args[7], b, 0600); err != nil {
						return nil, err
					}
					if saved && mode == "receipt-failure" {
						if err := os.Remove(a.dbToolReceiptFile()); err != nil {
							return nil, err
						}
						if err := os.Mkdir(a.dbToolReceiptFile(), 0700); err != nil {
							return nil, err
						}
					}
				default:
					t.Fatalf("禁止または未知のpgAdmin操作 %v", args[6:])
				}
				return nil, nil
			}
			plan, err := a.planDBToolRegistration(context.Background(), p, dbToolOptions{Tool: "pgadmin", Executable: root, PGAdminSQLite: db})
			if err != nil {
				t.Fatal(err)
			}
			result, err := a.applyDBToolRegistration(context.Background(), plan)
			if mode == "success" {
				if err != nil || result.Status != "registered" || !result.Verified {
					t.Fatalf("%+v %v", result, err)
				}
			} else if err == nil || !result.Submitted {
				t.Fatalf("要求済みの失敗を失った %+v %v", result, err)
			}
			if mode == "zero-without-save" || mode == "timeout-after-save" {
				if result.Status != "unknown" || result.Verified {
					t.Fatalf("結果未確認を成功扱い %+v", result)
				}
				newPlan, e := a.planDBToolRegistration(context.Background(), p, plan.Options)
				if e != nil {
					t.Fatal(e)
				}
				again, e := a.applyDBToolRegistration(context.Background(), newPlan)
				if again.Submitted {
					t.Fatal("結果不明の要求を自動再送")
				}
				if mode == "timeout-after-save" {
					if e != nil || again.Status != "already-present" {
						t.Fatalf("追加済みを再照合できない %+v %v", again, e)
					}
				} else if e == nil {
					t.Fatal("未登録のunknownを再送可能にした")
				}
			}
			if mode == "receipt-failure" && (!result.Verified || result.Status != "registered") {
				t.Fatal("履歴失敗で登録済みの事実を失った")
			}
			if rows["1"]["Name"] != "existing" {
				t.Fatal("既存設定を変更")
			}
			backup, err := os.ReadFile(filepath.Join(result.Backup, "pgadmin4.db"))
			if err != nil || string(backup) != "synthetic-db" {
				t.Fatalf("原本バックアップ %q %v", backup, err)
			}
		})
	}
}

// 任意プロパティの注入を許さず、複数JSONの同名定義を競合として扱う。
func TestDBToolRegistrationAmbiguousStore(t *testing.T) {
	a, p, exe, w := dbToolFixture(t)
	plan, err := a.planDBToolRegistration(context.Background(), p, dbToolOptions{Tool: "dbeaver", Executable: exe, DBeaverWorkspace: w})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w, "General/.dbeaver/data-sources-foreign.json"), plan.Data, 0600); err != nil {
		t.Fatal(err)
	}
	plan, err = a.planDBToolRegistration(context.Background(), p, plan.Options)
	if err != nil || plan.Disposition != "already-present" {
		t.Fatalf("既存定義を照合できない %v %+v", err, plan)
	}
	if err := os.WriteFile(filepath.Join(w, "General/.dbeaver/data-sources-duplicate.json"), plan.Data, 0600); err != nil {
		t.Fatal(err)
	}
	plan, err = a.planDBToolRegistration(context.Background(), p, plan.Options)
	if err != nil || plan.Disposition != "conflict" {
		t.Fatalf("重複を検出できない %v %+v", err, plan)
	}
}

// 明示指定時だけ同梱Pythonを実行する。全データは一時領域の合成fixtureに限定する。
func TestDBToolRegistrationPGAdminNative(t *testing.T) {
	root := os.Getenv("CSQL_TEST_PGADMIN_ROOT")
	if root == "" {
		t.Skip("pgAdmin同梱Pythonの隔離実測は明示指定時のみ")
	}
	a, p, _, _ := dbToolFixture(t)
	work := filepath.Join(filepath.Dir(a.Paths.ConfigDir), "native")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	python, err := filepath.EvalSymlinks(filepath.Join(root, "Contents/Frameworks/Python.framework/Versions/Current/bin/python3"))
	if err != nil {
		t.Fatal(err)
	}
	target := dbToolTarget{Python: python, SetupScript: filepath.Join(root, "Contents/Resources/web/setup.py")}
	if err := a.pgAdminSetup(context.Background(), target, work, "setup-db"); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(work, "data/pgadmin4.db")
	baseline := p
	baseline.Name = "unrelated"
	baseline.Port = 15438
	data, err := profileExport(baseline, "pgadmin")
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(work, "baseline.json")
	if err := dbToolWriteNew(input, data); err != nil {
		t.Fatal(err)
	}
	if err := a.pgAdminSetup(context.Background(), target, work, "load-servers", input, "--sqlite-path", db, "--user", "pgadmin4@pgadmin.org"); err != nil {
		t.Fatal(err)
	}
	o := dbToolOptions{Tool: "pgadmin", Executable: root, PGAdminSQLite: db}
	plan, err := a.planDBToolRegistration(context.Background(), p, o)
	if err != nil {
		t.Fatal(err)
	}
	r, err := a.applyDBToolRegistration(context.Background(), plan)
	if err != nil || !r.Verified {
		t.Fatalf("実保存を照合できない %+v %v", r, err)
	}
	for _, name := range []string{"pgadmin4.db", "pgadmin4.db.json", "復元手順.txt"} {
		fi, e := os.Stat(filepath.Join(r.Backup, name))
		if e != nil || fi.Mode().Perm() != 0600 {
			t.Fatalf("バックアップ権限 %s %v %v", name, fi, e)
		}
	}

	plan, err = a.planDBToolRegistration(context.Background(), p, o)
	if err != nil {
		t.Fatal(err)
	}
	r, err = a.applyDBToolRegistration(context.Background(), plan)
	if err != nil || r.Status != "already-present" || r.Submitted {
		t.Fatalf("再実行で追加 %+v %v", r, err)
	}
	p.Port++
	if err := a.writeProfiles([]Profile{p}); err != nil {
		t.Fatal(err)
	}
	plan, err = a.planDBToolRegistration(context.Background(), p, o)
	if err != nil {
		t.Fatal(err)
	}
	r, err = a.applyDBToolRegistration(context.Background(), plan)
	if err == nil || r.Status != "conflict" || r.Submitted {
		t.Fatalf("同名の異なるPortを追加 %+v %v", r, err)
	}
}

// 実配布パッケージの静的メタデータと、検出契約の食い違いを確認する。
func TestDBToolRegistrationDBeaverPackage(t *testing.T) {
	root := os.Getenv("CSQL_TEST_DBEAVER_ROOT")
	if root == "" {
		t.Skip("実配布パッケージの照合は明示指定時のみ")
	}
	a, p, _, workspace := dbToolFixture(t)
	plan, err := a.planDBToolRegistration(context.Background(), p, dbToolOptions{Tool: "dbeaver", Executable: root, DBeaverWorkspace: workspace})
	if err != nil || plan.Target.Version != "26.2.3" || plan.Disposition != "add" {
		t.Fatalf("実パッケージを検出できない %+v %v", plan, err)
	}
	result, err := a.applyDBToolRegistration(context.Background(), plan)
	if err != nil || !result.Committed || result.Verified {
		t.Fatalf("保存とGUI確認を混同 %+v %v", result, err)
	}
}

// 保存形式が不明なA5には要求せず、確認できる形式だけを追加する。
func TestDBToolRegistrationA5Store(t *testing.T) {
	for _, kind := range []string{"empty", "binary", "unknown-string", "valid", "pending-saved", "x86"} {
		t.Run(kind, func(t *testing.T) {
			a, p, _, _ := dbToolFixture(t)
			a.OS, a.Arch = "windows", "amd64"
			exe := filepath.Join(filepath.Dir(a.Paths.ConfigDir), "A5M2.exe")
			image := make([]byte, 152)
			copy(image, "MZ")
			binary.LittleEndian.PutUint32(image[60:], 128)
			copy(image[128:], "PE\x00\x00")
			machine := uint16(0x8664)
			if kind == "x86" {
				machine = 0x14c
			}
			binary.LittleEndian.PutUint16(image[132:], machine)
			if err := os.WriteFile(exe, image, 0700); err != nil {
				t.Fatal(err)
			}
			registryText := func(text string) a5RegistryValue {
				u := append(utf16.Encode([]rune(text)), 0)
				data := make([]byte, len(u)*2)
				for i, v := range u {
					binary.LittleEndian.PutUint16(data[i*2:], v)
				}
				return a5RegistryValue{Type: 1, Data: data}
			}
			name, conn, err := a5Connection(p)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := a5RegistrySnapshot{Values: map[string]a5RegistryValue{}}
			switch kind {
			case "binary":
				snapshot.Values["existing"] = a5RegistryValue{Type: 3, Data: []byte{1, 2}}
			case "unknown-string":
				snapshot.Values["existing"] = registryText("opaque-setting")
			default:
				if kind != "empty" {
					snapshot.Values["existing"] = registryText(conn)
				}
			}
			if kind == "pending-saved" {
				snapshot.Values[name] = registryText(conn)
			}
			a.DBToolA5Read = func() ([]byte, error) { return json.Marshal(snapshot) }
			a.DBToolA5Start = func(target dbToolTarget, n, c string) (bool, error) {
				snapshot.Values[n] = registryText(c)
				return true, nil
			}
			plan, err := a.planDBToolRegistration(context.Background(), p, dbToolOptions{Tool: "a5", Executable: exe})
			if kind == "empty" || kind == "binary" || kind == "unknown-string" || kind == "x86" {
				if err == nil {
					t.Fatalf("未知形式を受理 %+v", plan)
				}
				if _, e := os.Stat(a.dbToolReceiptFile()); !os.IsNotExist(e) {
					t.Fatal("拒否時にpendingを記録")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind == "pending-saved" {
				if err := a.writeDBToolReceipt(dbToolReceipt{Version: 1, Entries: map[string]dbToolRegistrationResult{plan.ID: {Status: "unknown", Submitted: true, Backup: "previous-backup"}}}); err != nil {
					t.Fatal(err)
				}
			}
			r, err := a.applyDBToolRegistration(context.Background(), plan)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "pending-saved" {
				if r.Submitted || r.Status != "already-present" {
					t.Fatalf("再送なしで解決できない %+v", r)
				}
			} else if !r.Submitted || !r.Verified || r.Status != "registered" {
				t.Fatalf("追加照合できない %+v", r)
			}
			if len(snapshot.Values) != 2 {
				t.Fatal("既存設定を失った")
			}
		})
	}
}

func TestDBToolRegistrationDBeaverPending(t *testing.T) {
	a, p, exe, w := dbToolFixture(t)
	plan, err := a.planDBToolRegistration(context.Background(), p, dbToolOptions{Tool: "dbeaver", Executable: exe, DBeaverWorkspace: w})
	if err != nil {
		t.Fatal(err)
	}
	r, err := a.applyDBToolRegistration(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.writeDBToolReceipt(dbToolReceipt{Version: 1, Entries: map[string]dbToolRegistrationResult{plan.ID: {Status: "unknown", Committed: true, Backup: r.Backup}}}); err != nil {
		t.Fatal(err)
	}
	plan, err = a.planDBToolRegistration(context.Background(), p, plan.Options)
	if err != nil {
		t.Fatal(err)
	}
	r, err = a.applyDBToolRegistration(context.Background(), plan)
	if err != nil || r.Status != "already-present" || r.Submitted || r.Verified {
		t.Fatalf("ファイル確定を再照合できない %+v %v", r, err)
	}
	history, err := a.readDBToolReceipt()
	if err != nil || history.Entries[plan.ID].Status != "already-present" {
		t.Fatal("履歴が解決されない")
	}
	// 通常のCLI再実行も、表示だけでなくunknown履歴を再送なしで解消する。
	history.Entries[plan.ID] = dbToolRegistrationResult{Status: "unknown", Committed: true, Backup: r.Backup}
	if err := a.writeDBToolReceipt(history); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(w, "General/.dbeaver/data-sources-csql-*.json"))
	before, _ := os.ReadFile(files[0])
	if code := a.Run(context.Background(), []string{"profile", "register", p.Name, "--tool", "dbeaver", "--executable", exe, "--dbeaver-workspace", w}); code != 0 {
		t.Fatalf("CLI再照合: %s", a.Err)
	}
	history, err = a.readDBToolReceipt()
	if err != nil || history.Entries[plan.ID].Status != "already-present" {
		t.Fatal("CLIがunknown履歴を解消しない")
	}
	after, _ := os.ReadFile(files[0])
	if string(before) != string(after) || history.Entries[plan.ID].Submitted {
		t.Fatal("再照合で外部変更を再送")
	}
}

func TestDBToolRegistrationWindowsPGAdminDetection(t *testing.T) {
	a, p, _, _ := dbToolFixture(t)
	a.OS = "windows"
	pf := filepath.Join(filepath.Dir(a.Paths.ConfigDir), "Program Files")
	root := filepath.Join(pf, "pgAdmin 4", "v9")
	for _, dir := range []string{filepath.Join(root, "python"), filepath.Join(root, "web")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for path, data := range map[string]string{filepath.Join(root, "python/python.exe"): "fixture", filepath.Join(root, "web/setup.py"): "fixture", filepath.Join(root, "web/version.py"): "APP_RELEASE = 9\nAPP_REVISION = 18\n"} {
		if err := os.WriteFile(path, []byte(data), 0700); err != nil {
			t.Fatal(err)
		}
	}
	db := filepath.Join(filepath.Dir(a.Paths.ConfigDir), "pgadmin4.db")
	if err := os.WriteFile(db, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	a.Env = func(key string) string {
		if key == "ProgramFiles" {
			return pf
		}
		return ""
	}
	plan, err := a.planDBToolRegistration(context.Background(), p, dbToolOptions{Tool: "pgadmin", PGAdminSQLite: db})
	if err != nil || plan.Target.Executable != root {
		t.Fatalf("親とv9を別の導入候補と誤検出 %+v %v", plan, err)
	}
}
