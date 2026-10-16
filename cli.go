package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Paths struct{ ConfigDir, CacheDir, ToolsDir string }
type Executable struct {
	Path       string
	PrefixArgs []string
}
type CommandRunner interface {
	Run(context.Context, Executable, []string) ([]byte, error)
}
type Artifact struct {
	Name, Version, File, URL, SHA256, Format string
	PythonRequired                           bool
}
type App struct {
	outMu, errMu                          sync.Mutex
	proxyPortsMu                          sync.Mutex
	proxyPorts                            map[int]bool
	Paths                                 Paths
	In                                    io.Reader
	Out, Err                              io.Writer
	TTY                                   bool
	OS, Arch                              string
	Runner                                CommandRunner
	LookPath                              func(string) (string, error)
	Env                                   func(string) string
	Now                                   func() time.Time
	QueryTimeout, CheckTimeout, StopGrace time.Duration
	StartupTimeout                        time.Duration
	HTTP                                  *http.Client
	Artifacts                             []Artifact
	AllowTestHTTP                         bool
	Executable                            func() (string, error)
	UserPath                              userPathStore
	confirmation                          *bufio.Reader
	ConfirmAction                         func(context.Context, string) (bool, error)
	CopyText                              func(context.Context, string) error
}
type synchronizedWriter struct {
	writer io.Writer
	mu     *sync.Mutex
}

func (w synchronizedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(p)
}

// TUIは唯一の描画担当なので元の端末Fileを渡す。汎用Writerで包むとBubble Teaの端末判定が失われる。
func (a *App) terminalOutput() io.Writer {
	if file, ok := a.Out.(term.File); ok {
		return file
	}
	return a.stdout()
}
func (a *App) stdout() io.Writer { return synchronizedWriter{a.Out, &a.outMu} }
func (a *App) stderr() io.Writer { return synchronizedWriter{a.Err, &a.errMu} }

func NewApp() *App {
	c, _ := os.UserConfigDir()
	cache, _ := os.UserCacheDir()
	return &App{Paths: Paths{filepath.Join(c, "csql"), filepath.Join(cache, "csql"), filepath.Join(c, "csql", "tools")}, In: os.Stdin, Out: os.Stdout, Err: os.Stderr, TTY: term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd()), OS: runtime.GOOS, Arch: runtime.GOARCH, Runner: osRunner{}, LookPath: exec.LookPath, Env: os.Getenv, Now: time.Now, QueryTimeout: 60 * time.Second, CheckTimeout: 10 * time.Second, StopGrace: 10 * time.Second, StartupTimeout: 60 * time.Second}
}

const version = "0.5.0"

var ErrCancelled = errors.New("操作を取り消しました")

type usageError struct{ error }

func (a *App) Run(ctx context.Context, args []string) int {
	a.confirmation = nil
	root := &cobra.Command{Use: "csql [インスタンス名]", Short: "Cloud SQLの検索・接続・Proxy管理", SilenceUsage: true, SilenceErrors: true}
	root.SetOut(a.stdout())
	root.SetErr(a.stderr())
	root.SetIn(a.In)
	root.SetArgs(args)
	root.SetHelpTemplate("{{.Short}}\n\n利用方法:\n  {{.UseLine}}\n{{if .HasAvailableSubCommands}}\nコマンド:\n{{range .Commands}}{{if .IsAvailableCommand}}  {{rpad .Name .NamePadding }} {{.Short}}\n{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}\nオプション:\n{{.LocalFlags.FlagUsages}}{{end}}")
	root.SetFlagErrorFunc(func(_ *cobra.Command, e error) error { return usageError{e} })
	root.CompletionOptions.DisableDefaultCmd = true
	argCount := func(min, max int) cobra.PositionalArgs {
		return func(_ *cobra.Command, args []string) error {
			if len(args) < min || len(args) > max {
				return usageError{fmt.Errorf("引数の数が不正です。--helpを確認してください")}
			}
			return nil
		}
	}
	connect := func(cmd *cobra.Command, args []string) error {
		profile, _ := cmd.Flags().GetString("profile")
		if profile != "" {
			if len(args) != 0 || cmd.Flags().Changed("project") || cmd.Flags().Changed("refresh") || cmd.Flags().Changed("private-ip") {
				return usageError{fmt.Errorf("--profile は位置引数・--project・--refresh・--private-ipと併用できません")}
			}
			rows, err := a.LoadProfiles()
			if err != nil {
				return err
			}
			selected, err := findProfile(rows, profile)
			if err != nil {
				return err
			}
			port, _ := cmd.Flags().GetInt("port")
			if cmd.Flags().Changed("port") && (port < 1 || port > 65535) {
				return usageError{fmt.Errorf("ポートは1〜65535を指定してください")}
			}
			if a.TTY {
				return a.RunConnectUI(cmd.Context(), connectRequest{ProfileName: profile, Port: port})
			}
			return a.Connect(cmd.Context(), selected.Instance, ConnectOptions{Port: port, PrivateIP: selected.PrivateIP})
		}
		p, _ := cmd.Flags().GetInt("port")
		if cmd.Flags().Changed("port") && (p < 1 || p > 65535) {
			return usageError{fmt.Errorf("ポートは1〜65535を指定してください")}
		}
		if a.TTY {
			project, _ := cmd.Flags().GetString("project")
			refresh, _ := cmd.Flags().GetBool("refresh")
			priv, _ := cmd.Flags().GetBool("private-ip")
			query := ""
			if len(args) > 0 {
				query = args[0]
			}
			return a.RunConnectUI(cmd.Context(), connectRequest{Query: query, Project: project, Refresh: refresh, PrivateIP: priv, Port: p})
		}
		if _, err := a.resolve(cmd.Context(), "gcloud"); err != nil {
			return err
		}
		if _, err := a.resolve(cmd.Context(), "cloud-sql-proxy"); err != nil {
			return err
		}
		project, _ := cmd.Flags().GetString("project")
		refresh, _ := cmd.Flags().GetBool("refresh")
		priv, _ := cmd.Flags().GetBool("private-ip")
		query := ""
		if len(args) > 0 {
			query = args[0]
		}
		result, err := a.Discover(cmd.Context(), project, refresh)
		if err != nil {
			return err
		}
		a.printFailures(result)
		matches := MatchInstances(result.Instances, query)
		if len(matches) == 0 && !refresh {
			result, err = a.Discover(cmd.Context(), project, true)
			if err != nil {
				return err
			}
			a.printFailures(result)
			matches = MatchInstances(result.Instances, query)
		}
		if len(matches) == 0 {
			return fmt.Errorf("一致するインスタンスが見つかりません（参照できた検索範囲内）。--project または --refresh を確認してください")
		}
		selected := matches[0]
		if len(matches) > 1 || query == "" {
			selected, err = a.SelectInstance(cmd.Context(), matches)
			if err != nil {
				return err
			}
		}
		return a.Connect(cmd.Context(), selected, ConnectOptions{Port: p, PrivateIP: priv})
	}
	searchFlags := func(c *cobra.Command) {
		c.Flags().String("project", "", "検索するプロジェクトID")
		c.Flags().Bool("refresh", false, "一覧キャッシュを更新")
	}
	connectFlags := func(c *cobra.Command) {
		c.Flags().String("profile", "", "保存済みの接続設定名")
		searchFlags(c)
		c.Flags().IntP("port", "p", 0, "待受ポート（保存済み設定がない場合はPostgreSQL 15432 / MySQL 13306 / SQL Server 11433から空きを選択）")
		c.Flags().Bool("private-ip", false, "Private IPで接続")
	}
	root.Args = argCount(0, 1)
	root.RunE = connect
	connectFlags(root)
	c := &cobra.Command{Use: "connect [インスタンス名]", Short: "検索してProxyを起動", Args: argCount(0, 1), RunE: connect}
	connectFlags(c)
	root.AddCommand(c)
	root.AddCommand(a.profileCommand())
	l := &cobra.Command{Use: "list [検索文字列]", Short: "インスタンス一覧", Args: argCount(0, 1), RunE: func(cmd *cobra.Command, args []string) error {
		p, _ := cmd.Flags().GetString("project")
		r, _ := cmd.Flags().GetBool("refresh")
		result, err := a.Discover(cmd.Context(), p, r)
		if err != nil {
			return err
		}
		a.printFailures(result)
		q := ""
		if len(args) > 0 {
			q = args[0]
		}
		matches := MatchInstances(result.Instances, q)
		if len(matches) == 0 {
			return fmt.Errorf("一致するインスタンスが見つかりません（参照できた検索範囲内）")
		}
		a.printInstances(matches)
		return nil
	}}
	searchFlags(l)
	root.AddCommand(l)
	root.AddCommand(&cobra.Command{Use: "doctor", Short: "導入と認証の準備状況を診断", Args: argCount(0, 0), RunE: func(c *cobra.Command, _ []string) error {
		d := a.Diagnose(c.Context())
		a.printDiagnosis(d)
		if !d.Ready {
			return fmt.Errorf("不足・要確認の項目があります。csql setup を実行してください")
		}
		return nil
	}})
	s := &cobra.Command{Use: "setup", Short: "導入・ログイン手順を案内", Args: argCount(0, 0), RunE: func(c *cobra.Command, _ []string) error {
		i, _ := c.Flags().GetBool("install")
		path, _ := c.Flags().GetBool("path")
		shell, _ := c.Flags().GetString("shell")
		if c.Flags().Changed("shell") && (!path || a.OS == "windows" || (shell != "zsh" && shell != "bash" && shell != "fish")) {
			return usageError{fmt.Errorf("--shell はUnixの --path と併用し、zsh|bash|fish を指定してください")}
		}
		if !path {
			return a.Setup(c.Context(), i)
		}
		if i {
			done, err := a.setup(c.Context(), true)
			if err != nil {
				return err
			}
			if !done {
				return nil
			}
		}
		return a.SetupPath(c.Context(), shell)
	}}
	s.Flags().Bool("install", false, "内容を確認して不足コマンドを導入")
	s.Flags().Bool("path", false, "内容を確認してcsql・管理版ツールをユーザーPATHへ登録")
	s.Flags().String("shell", "", "PATH設定の対象シェル（zsh|bash|fish、省略時は自動検出）")
	root.AddCommand(s)
	root.AddCommand(&cobra.Command{Use: "status", Short: "管理中Proxyの状況", Args: argCount(0, 0), RunE: func(c *cobra.Command, _ []string) error {
		rows, err := a.Status(c.Context())
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			fmt.Fprintln(a.stdout(), "管理中のセッションはありません")
		}
		for _, r := range rows {
			fmt.Fprintf(a.stdout(), "%s\t%s/%s\t127.0.0.1:%d\t%s\n", r.ID, r.Project, r.Name, r.Port, stateLabel(r.State))
		}
		return nil
	}})
	stop := &cobra.Command{Use: "stop [セッションID]", Short: "指定セッション、または--allで全管理セッションを停止", Args: argCount(0, 1), RunE: func(c *cobra.Command, args []string) error {
		all, _ := c.Flags().GetBool("all")
		if (all && len(args) != 0) || (!all && len(args) != 1) {
			return usageError{fmt.Errorf("csql stop SESSION_ID または csql stop --all を指定してください")}
		}
		id := ""
		if len(args) > 0 {
			id = args[0]
		}
		return a.Stop(c.Context(), id, all)
	}}
	stop.Flags().Bool("all", false, "全管理セッションを停止")
	root.AddCommand(stop)
	root.AddCommand(&cobra.Command{Use: "version", Short: "CLIの版を表示", Args: argCount(0, 0), Run: func(*cobra.Command, []string) { fmt.Fprintln(a.stdout(), "csql "+version) }})
	var localize func(*cobra.Command)
	localize = func(cmd *cobra.Command) {
		cmd.InitDefaultHelpFlag()
		if flag := cmd.Flags().Lookup("help"); flag != nil {
			flag.Usage = "ヘルプを表示"
		}
		for _, child := range cmd.Commands() {
			localize(child)
		}
	}
	localize(root)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	var diagnosed diagnosticError
	var usage usageError
	if !errors.Is(err, context.Canceled) && !errors.Is(err, ErrCancelled) && !errors.As(err, &usage) && !errors.As(err, &diagnosed) && !strings.Contains(err.Error(), "次の操作:") {
		fmt.Fprintln(a.stderr(), formatDiagnostic(explainFailure(FailureContext{Source: "local", Operation: "操作"}, err.Error())))
	} else {
		fmt.Fprintln(a.stderr(), err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, ErrCancelled) {
		return 130
	}
	var u usageError
	if errors.As(err, &u) {
		return 2
	}
	return 1
}

func (a *App) printFailures(r DiscoveryResult) {
	for _, f := range r.Failures {
		fmt.Fprintf(a.stderr(), "検索失敗 [%s]: %s\n", f.Project, f.Message)
	}
}
func (a *App) printInstances(rows []Instance) {
	fmt.Fprintln(a.stdout(), "PROJECT\tINSTANCE\tREGION\tDB")
	for _, r := range rows {
		fmt.Fprintf(a.stdout(), "%s\t%s\t%s\t%s\n", r.Project, r.Name, r.Region, r.DatabaseVersion)
	}
}
func stateLabel(s string) string {
	switch s {
	case "running":
		return "稼働中（DBログイン未検証）"
	case "stopped":
		return "停止済み"
	default:
		return "応答なし"
	}
}

// 端末制御文字を含む外部データは表示前に取り除く。
func cleanText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, s)
}
