package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// 実ファイルを別PCの領域へ渡し、保存対象の値と出力の非破壊性を確かめる。
func migrationRows() []Profile {
	p := Profile{Name: "dev", Instance: testInstance(), Database: "業務/db", Username: "user@team", Port: 15432, PrivateIP: true}
	q := p
	q.Name, q.Database = "report", "集計"
	return []Profile{p, q}
}
func migrationFile(t *testing.T, data []byte) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "移行.json")
	if err = os.WriteFile(p, data, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func migrationData(t *testing.T, rows []Profile) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"format": "csql-migration", "version": 1, "createdAt": "2026-10-19T00:00:00Z", "csqlVersion": version, "profiles": rows})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestMigrationFormatAndExport(t *testing.T) {
	a, _, _ := testApp(t)
	rows := migrationRows()
	if err := writeJSON(filepath.Join(a.Paths.ConfigDir, "profiles.json"), profileStore{1, rows}); err != nil {
		t.Fatal(err)
	}
	b, count, err := a.prepareMigrationExport()
	if err != nil || count != 2 {
		t.Fatalf("出力準備: count=%d err=%v", count, err)
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(b, &doc) != nil || len(doc) != 5 || string(doc["format"]) != `"csql-migration"` {
		t.Fatalf("移行形式が不正: %s", b)
	}
	for _, forbidden := range []string{"Password", "Token", "Session", "SHA256"} {
		if bytes.Contains(b, []byte(forbidden)) {
			t.Fatal("移行対象外を出力: " + forbidden)
		}
	}
	path := migrationFile(t, []byte("keep"))
	if _, err = a.exportMigration(context.Background(), path, b, count); err == nil {
		t.Fatal("既存ファイルを上書き")
	}
	if got, _ := os.ReadFile(path); string(got) != "keep" {
		t.Fatal("元の出力を破損")
	}
	path = filepath.Join(filepath.Dir(path), "new.json")
	r, err := a.exportMigration(context.Background(), path, b, count)
	if err != nil || !r.Committed || r.Path != path {
		t.Fatalf("出力: %+v %v", r, err)
	}
	other, _, _ := testApp(t)
	preview, err := other.previewMigration(context.Background(), path)
	if err != nil || preview == nil {
		t.Fatalf("別PCの入力: %v", err)
	}
	if !reflect.DeepEqual(preview.Added, rows) || len(preview.Conflicts) != 0 {
		t.Fatalf("値が変化: %+v", preview)
	}
	if _, err = os.Stat(other.Paths.ConfigDir); !os.IsNotExist(err) {
		t.Fatal("プレビューが設定を作成")
	}
	if a.OS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0600 {
			t.Fatal("出力権限")
		}
	}
}

// 外部入力の形式だけを検証し、不正ファイルを設定として採用しない。
func TestMigrationInputRejection(t *testing.T) {
	good := migrationData(t, migrationRows())
	cases := map[string][]byte{
		"format":        bytes.Replace(good, []byte(`"csql-migration"`), []byte(`"other"`), 1),
		"version":       bytes.Replace(good, []byte(`"version":1`), []byte(`"version":2`), 1),
		"duplicate":     append([]byte(`{"version":1,`), good[1:]...),
		"alias":         bytes.Replace(good, []byte(`"format"`), []byte(`"Format"`), 1),
		"unknown":       append([]byte(`{"command":"run",`), good[1:]...),
		"trailing":      append(bytes.Clone(good), []byte(` {}`)...),
		"utf8":          append([]byte{0xff}, good...),
		"double-bom":    append([]byte{0xef, 0xbb, 0xbf, 0xef, 0xbb, 0xbf}, good...),
		"oversize":      []byte(strings.Repeat(" ", 2<<20) + string(good)),
		"deep":          []byte(strings.Repeat("[", 100) + strings.Repeat("]", 100)),
		"control":       bytes.Replace(good, []byte(`user@team`), []byte(`user\u001b[2J`), 1),
		"profile-alias": bytes.Replace(good, []byte(`"Name"`), []byte(`"name"`), 1),
		"null-boolean":  bytes.Replace(good, []byte(`"PrivateIP":true`), []byte(`"PrivateIP":null`), 1),
	}
	// JSONの配列位置に依存しないnullケースを追加する。
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(good, &raw)
	delete(raw, "version")
	cases["missing"], _ = json.Marshal(raw)
	_ = json.Unmarshal(good, &raw)
	raw["profiles"] = json.RawMessage(`null`)
	cases["null"], _ = json.Marshal(raw)
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			a, _, _ := testApp(t)
			if _, err := a.previewMigration(context.Background(), migrationFile(t, data)); err == nil {
				t.Fatal("不正入力を受理")
			}
		})
	}
	for _, prefix := range [][]byte{nil, {0xef, 0xbb, 0xbf}} {
		a, _, _ := testApp(t)
		if _, err := a.previewMigration(context.Background(), migrationFile(t, append(prefix, good...))); err != nil {
			t.Fatal("正常UTF-8/BOMを拒否:", err)
		}
	}
	a, _, _ := testApp(t)
	dir := filepath.Dir(migrationFile(t, good))
	if _, err := a.previewMigration(context.Background(), dir); err == nil {
		t.Fatal("dirを受理")
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(filepath.Join(dir, "移行.json"), link); err == nil {
		if _, err = a.previewMigration(context.Background(), link); err == nil {
			t.Fatal("linkを受理")
		}
	}
}

// 片側は保存可能でも、結合後に上限を越えるケースを拒否する。
func TestMigrationPreviewCapacity(t *testing.T) {
	a, _, _ := testApp(t)
	var existing, incoming []Profile
	for i := 0; i < 500; i++ {
		p := migrationRows()[0]
		p.Name = fmt.Sprintf("a%04d", i)
		p.Database = strings.Repeat("業", 256)
		p.Username = strings.Repeat("務", 256)
		existing = append(existing, p)
		p.Name = fmt.Sprintf("b%04d", i)
		incoming = append(incoming, p)
	}
	if err := a.writeProfiles(existing); err != nil {
		t.Fatal(err)
	}
	if _, err := a.previewMigration(context.Background(), migrationFile(t, migrationData(t, incoming))); err == nil {
		t.Fatal("結合後の容量超過を受理")
	}
}

func TestMigrationPreview(t *testing.T) {
	for _, name := range []string{"empty", "identical", "alias", "name", "port", "instance-port", "invalid-reservation"} {
		t.Run(name, func(t *testing.T) {
			a, _, _ := testApp(t)
			existing := migrationRows()
			incoming := []Profile{existing[0]}
			switch name {
			case "empty":
				incoming = []Profile{}
			case "alias":
				incoming[0].Name = "alias"
			case "name":
				incoming[0].Database = "changed"
			case "port":
				incoming[0].Name = "other"
				incoming[0].Instance.Name = "other"
				incoming[0].ConnectionName = incoming[0].Project + ":" + incoming[0].Region + ":other"
			case "instance-port":
				incoming[0].Name = "other"
				incoming[0].Port++
			case "invalid-reservation":
				incoming = append(incoming, incoming[0])
			}
			if err := writeJSON(filepath.Join(a.Paths.ConfigDir, "profiles.json"), profileStore{1, existing}); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(a.Paths.ConfigDir, "profiles.json"))
			p, err := a.previewMigration(context.Background(), migrationFile(t, migrationData(t, incoming)))
			if name == "invalid-reservation" {
				if err == nil {
					t.Fatal("入力内の重複を受理")
				}
				return
			}
			if err != nil || p == nil {
				t.Fatal(err)
			}
			want := 0
			if name == "name" || name == "port" || name == "instance-port" {
				want = 1
			}
			if len(p.Conflicts) < want || (want == 0 && len(p.Conflicts) != 0) {
				t.Fatalf("競合判定: %+v", p)
			}
			if name == "alias" && len(p.Added) != 1 {
				t.Fatal("同Portの別名が消失")
			}
			if name == "identical" && len(p.Identical) != 1 {
				t.Fatal("同一判定")
			}
			after, _ := os.ReadFile(filepath.Join(a.Paths.ConfigDir, "profiles.json"))
			if !bytes.Equal(before, after) {
				t.Fatal("プレビューで変更")
			}
			if matches, _ := filepath.Glob(filepath.Join(a.Paths.ConfigDir, "*import*")); len(matches) != 0 {
				t.Fatal("プレビューでbackup作成")
			}
		})
	}
}

// 確認・排他・バックアップ・原子的保存を、実際の設定ファイルで観測する。
func TestMigrationApplySafety(t *testing.T) {
	for _, mode := range []string{"existing", "first", "backup-fail", "save-fail", "changed", "deleted", "created", "cancel-after-backup", "cancel-after-save", "input-changed", "lock-cancel"} {
		t.Run(mode, func(t *testing.T) {
			a, _, _ := testApp(t)
			file := filepath.Join(a.Paths.ConfigDir, "profiles.json")
			rows := migrationRows()
			if mode != "first" && mode != "created" {
				if err := a.writeProfiles(rows); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(file)
			p := rows[0]
			p.Name = "imported"
			source := migrationFile(t, migrationData(t, []Profile{p}))
			preview, err := a.previewMigration(context.Background(), source)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "backup-fail":
				a.MigrationOps = &migrationFileOps{Backup: func(string, []byte) (string, error) { return "", fmt.Errorf("backup fail") }}
			case "save-fail":
				a.MigrationOps = &migrationFileOps{Save: func([]Profile) error { return fmt.Errorf("save fail") }}
			case "changed", "created":
				rows[0].Username = "changed"
				if err = a.writeProfiles(rows); err != nil {
					t.Fatal(err)
				}
				before, _ = os.ReadFile(file)
			case "deleted":
				if err = os.Remove(file); err != nil {
					t.Fatal(err)
				}
				before = nil
			case "cancel-after-backup":
				a.MigrationOps = &migrationFileOps{Backup: func(dir string, b []byte) (string, error) {
					path, err := backupMigration(dir, b)
					cancel()
					return path, err
				}}
			case "cancel-after-save":
				a.MigrationOps = &migrationFileOps{Save: func(rows []Profile) error { err := a.writeProfiles(rows); cancel(); return err }}
			case "input-changed":
				if err = os.WriteFile(source, []byte("changed externally"), 0600); err != nil {
					t.Fatal(err)
				}
			case "lock-cancel":
				if err = secureDir(a.Paths.ConfigDir); err != nil {
					t.Fatal(err)
				}
				f, e := os.OpenFile(filepath.Join(a.Paths.ConfigDir, "profiles.lock"), os.O_CREATE|os.O_RDWR, 0600)
				if e != nil {
					t.Fatal(e)
				}
				defer f.Close()
				unlock, e := lockProfiles(context.Background(), f)
				if e != nil {
					t.Fatal(e)
				}
				defer unlock()
				ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
				defer cancel()
			}
			result, err := a.applyMigration(ctx, preview)
			success := mode == "existing" || mode == "first" || mode == "cancel-after-save" || mode == "input-changed"
			after, _ := os.ReadFile(file)
			if success {
				if err != nil || !result.Committed || result.Added != 1 {
					t.Fatalf("成功: %+v %v", result, err)
				}
				got, e := a.LoadProfiles()
				if e != nil {
					t.Fatal(e)
				}
				if _, e = findProfile(got, "imported"); e != nil {
					t.Fatal(e)
				}
				if mode == "first" {
					if result.Backup != "" {
						t.Fatal("初回の偽backup")
					}
				} else {
					saved, e := os.ReadFile(result.Backup)
					if e != nil || !bytes.Equal(saved, before) {
						t.Fatal("原本backup不一致", e)
					}
				}
			} else {
				if err == nil || result.Committed || !bytes.Equal(before, after) {
					t.Fatalf("拒否で変更: %+v %v", result, err)
				}
				if mode == "save-fail" || mode == "cancel-after-backup" {
					saved, e := os.ReadFile(result.Backup)
					if e != nil || !bytes.Equal(saved, before) {
						t.Fatal("失敗後のbackupを喪失")
					}
				}
			}
		})
	}
}
func TestMigrationCLI(t *testing.T) {
	source, _, _ := testApp(t)
	if err := source.writeProfiles(migrationRows()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(migrationFile(t, nil)), "transfer.json")
	if code := source.Run(context.Background(), []string{"migrate", "export", "--output", path}); code != 0 {
		t.Fatalf("CLI出力 code=%d", code)
	}
	a, out, errs := testApp(t)
	if code := a.Run(context.Background(), []string{"migrate", "import", path, "--dry-run"}); code != 0 {
		t.Fatal(errs)
	}
	if !strings.Contains(out.String(), "追加: 2") || !strings.Contains(out.String(), path) {
		t.Fatal("確認情報不足", out)
	}
	if _, err := os.Stat(a.Paths.ConfigDir); !os.IsNotExist(err) {
		t.Fatal("dry-runが設定を作成")
	}
	if code := a.Run(context.Background(), []string{"migrate", "import", path}); code != 2 {
		t.Fatalf("非TTY取込 code=%d", code)
	}
	a.TTY = true
	a.In = strings.NewReader("\n")
	if code := a.Run(context.Background(), []string{"migrate", "import", path}); code != 0 {
		t.Fatal(errs)
	}
	if _, err := os.Stat(a.Paths.ConfigDir); !os.IsNotExist(err) {
		t.Fatal("既定取消で設定作成")
	}
	a.In = strings.NewReader("y\n")
	out.Reset()
	if code := a.Run(context.Background(), []string{"migrate", "import", path}); code != 0 {
		t.Fatal(errs)
	}
	rows, err := a.LoadProfiles()
	if err != nil || !reflect.DeepEqual(rows, migrationRows()) {
		t.Fatalf("CLI往復: %+v %v", rows, err)
	}
	out.Reset()
	if code := a.Run(context.Background(), []string{"migrate", "import", path}); code != 0 || !strings.Contains(out.String(), "変更なし") {
		t.Fatal("再取込", out, errs)
	}
	for _, args := range [][]string{{"migrate", "--help"}, {"migrate", "import", "--help"}} {
		out.Reset()
		if code := a.Run(context.Background(), args); code != 0 || !strings.Contains(out.String(), "取込") {
			t.Fatal("help", out, errs)
		}
	}
	for _, args := range [][]string{{"migrate", "import"}, {"migrate", "export", "extra"}, {"migrate", "import", path, "--yes"}} {
		if code := a.Run(context.Background(), args); code != 2 {
			t.Fatalf("引数拒否 %v: %d", args, code)
		}
	}
}
