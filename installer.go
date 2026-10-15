//go:build installer

package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

//go:embed installer_payload/*
var installerFiles embed.FS

const installerLogLimit = 256 << 10
const (
	installerIdle       = "idle"
	installerWorking    = "working"
	installerAwaiting   = "awaiting"
	installerCancelling = "cancelling"
	installerComplete   = "complete"
	installerCancelled  = "cancelled"
	installerFailed     = "failed"
)

var installerNativeMain = func() int {
	fmt.Fprintln(os.Stderr, "このビルドではセットアップ画面を利用できません。scripts/build-installers.pyで対象OS用にビルドしてください。")
	return 1
}
var installerPrepareCommand func(*exec.Cmd)

func init() { installerEntry = func() int { runtime.LockOSThread(); return installerNativeMain() } }

type installerSnapshot struct {
	State, Log, Prompt string
	Revision, PromptID uint64
}

func (s installerSnapshot) Active() bool {
	return s.State == installerWorking || s.State == installerAwaiting || s.State == installerCancelling
}

type installerController struct {
	mu                 sync.Mutex
	state, log, prompt string
	revision, promptID uint64
	answer             chan bool
	cancel             context.CancelFunc
	makeApp            func() *App
	payload            func(context.Context) (string, func(), error)
}

func newInstallerController(makeApp func() *App, payload func(context.Context) (string, func(), error)) *installerController {
	welcome := ""
	if makeApp == nil {
		app := NewApp()
		app.Runner = osRunner{Prepare: installerPrepareCommand}
		makeApp = func() *App { return app }
		welcome = fmt.Sprintf("csql配置先: %s\n管理ツールの保存先: %s\n\n開始後に導入対象・配布元・PATH変更内容を表示し、実行前に確認します。", filepath.Join(app.Paths.ConfigDir, "bin"), app.Paths.ToolsDir)
	}
	if payload == nil {
		payload = defaultInstallerPayload
	}
	return &installerController{state: installerIdle, log: welcome, makeApp: makeApp, payload: payload}
}
func (c *installerController) Snapshot() installerSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return installerSnapshot{c.state, c.log, c.prompt, c.revision, c.promptID}
}
func (c *installerController) Write(p []byte) (int, error) {
	text := strings.ReplaceAll(strings.ToValidUTF8(string(p), "�"), "\x00", "�")
	c.mu.Lock()
	defer c.mu.Unlock()
	c.log += text
	if len(c.log) > installerLogLimit {
		c.log = strings.ToValidUTF8(c.log[len(c.log)-installerLogLimit:], "�")
	}
	c.revision++
	return len(p), nil
}
func (c *installerController) Start(shell string) bool {
	c.mu.Lock()
	if (installerSnapshot{State: c.state}).Active() || c.state == installerComplete {
		c.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.state = installerWorking
	c.log = ""
	c.prompt = ""
	c.answer = nil
	c.revision++
	c.mu.Unlock()
	go func() {
		state, err := c.perform(ctx, shell)
		if err != nil {
			if ctx.Err() != nil {
				fmt.Fprintln(c, "操作を取り消しました。")
			} else {
				fmt.Fprintln(c, err)
			}
		}
		if ctx.Err() != nil {
			state = installerCancelled
		}
		c.mu.Lock()
		c.state = state
		c.prompt = ""
		c.answer = nil
		c.cancel = nil
		c.revision++
		c.mu.Unlock()
		cancel()
	}()
	return true
}
func (c *installerController) perform(ctx context.Context, shell string) (string, error) {
	fmt.Fprintln(c, "セットアップを準備しています。変更前に内容を確認します。")
	source, cleanup, err := c.payload(ctx)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return installerFailed, err
	}
	if err = ctx.Err(); err != nil {
		return installerCancelled, err
	}
	a := c.makeApp()
	a.Out = c
	a.Err = c
	a.Executable = func() (string, error) { return source, nil }
	declined := false
	a.ConfirmAction = func(ctx context.Context, prompt string) (bool, error) {
		ok, err := c.ask(ctx, prompt)
		if !ok && err == nil {
			declined = true
		}
		return ok, err
	}
	fmt.Fprintln(c, "段階1/2: 不足ツールの確認・導入")
	done, err := a.setup(ctx, true)
	if err != nil {
		return installerFailed, err
	}
	if !done || declined {
		return installerCancelled, nil
	}
	fmt.Fprintln(c, "段階2/2: csqlの配置とユーザーPATH設定")
	if err = a.SetupPath(ctx, shell); err != nil {
		return installerFailed, err
	}
	if declined {
		return installerCancelled, nil
	}
	fmt.Fprintln(c, "セットアップが完了しました。ターミナルアプリを終了して開き直し、csql doctorで確認してください。Googleログインは案内されたコマンドから別途行ってください。")
	return installerComplete, nil
}
func (c *installerController) ask(ctx context.Context, prompt string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	ch := make(chan bool, 1)
	c.mu.Lock()
	if c.state == installerCancelling {
		c.mu.Unlock()
		return false, context.Canceled
	}
	c.promptID++
	id := c.promptID
	c.prompt = prompt
	c.answer = ch
	c.state = installerAwaiting
	c.revision++
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.promptID == id {
			c.prompt = ""
			c.answer = nil
			if c.state == installerAwaiting {
				c.state = installerWorking
			}
			c.revision++
		}
		c.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case ok := <-ch:
		if err := ctx.Err(); err != nil {
			return false, err
		}
		return ok, nil
	}
}
func (c *installerController) Answer(id uint64, ok bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != installerAwaiting || id != c.promptID || c.answer == nil {
		return false
	}
	c.answer <- ok
	c.answer = nil
	c.state = installerWorking
	c.prompt = ""
	c.revision++
	return true
}
func (c *installerController) Cancel() {
	c.mu.Lock()
	cancel := c.cancel
	if cancel != nil {
		c.state = installerCancelling
		c.revision++
	}
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

type installerPayloadManifest struct {
	Kind    string `json:"kind"`
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	SHA256  string `json:"sha256"`
}

func materializeInstallerPayload(ctx context.Context, files fs.FS, goos, arch string) (string, func(), error) {
	data, err := fs.ReadFile(files, "manifest.json")
	if err != nil {
		return "", nil, fmt.Errorf("同梱CLIのmanifestがありません。配布用ビルドを使用してください: %w", err)
	}
	var m installerPayloadManifest
	if json.Unmarshal(data, &m) != nil || m.Kind != "csql-cli" || m.Version != version || m.OS != goos || m.Arch != arch {
		return "", nil, fmt.Errorf("同梱CLIの版・種別・OS/CPUが一致しません")
	}
	name := "csql"
	if goos == "windows" {
		name += ".exe"
	}
	f, err := files.Open(name)
	if err != nil {
		return "", nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > 256<<20 {
		return "", nil, fmt.Errorf("同梱CLIの容量・種類が不正です")
	}
	b, err := io.ReadAll(io.LimitReader(f, 256<<20+1))
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != m.SHA256 {
		return "", nil, fmt.Errorf("同梱CLIのSHA256が一致しません")
	}
	if err = ctx.Err(); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "csql-installer-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	p := filepath.Join(dir, name)
	if err = os.WriteFile(p, b, 0700); err != nil {
		cleanup()
		return "", nil, err
	}
	return p, cleanup, nil
}
func defaultInstallerPayload(ctx context.Context) (string, func(), error) {
	files, err := fs.Sub(installerFiles, "installer_payload")
	if err != nil {
		return "", nil, err
	}
	p, cleanup, err := materializeInstallerPayload(ctx, files, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return p, cleanup, err
	}
	// GUIそのものをCLIとして配置するビルドミスを、利用者領域の変更前に検出する。
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := (osRunner{Prepare: installerPrepareCommand}).Run(checkCtx, Executable{Path: p}, []string{"version"})
	if err != nil || strings.TrimSpace(string(out)) != "csql "+version {
		cleanup()
		return "", nil, fmt.Errorf("同梱CLIの起動確認に失敗しました: %v", err)
	}
	return p, cleanup, nil
}

func installerPresentation(s installerSnapshot) (status, action string, enabled, busy bool) {
	switch s.State {
	case installerIdle:
		return "変更前に内容を確認します。準備ができたら開始してください。", "セットアップ開始", true, false
	case installerAwaiting:
		return "詳細の変更内容を確認し、実行する場合はボタンを押してください。", "表示内容で実行", true, false
	case installerWorking:
		return "処理中です。完了まで画面を開いたままお待ちください。", "処理中", false, true
	case installerCancelling:
		return "取り消しています。終了処理が終わるまでお待ちください。", "取消処理中", false, true
	case installerComplete:
		return "セットアップ完了。ターミナルを開き直し、Googleログインとdoctorを確認してください。", "完了", false, false
	case installerCancelled:
		return "取り消しました。成功済みの導入は保持されます。必要なら再試行できます。", "再試行", true, false
	default:
		return "完了していません。詳細のエラーを確認してから再試行してください。", "再試行", true, false
	}
}
