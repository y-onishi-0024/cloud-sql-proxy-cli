# csqlの対話画面・エラー案内・接続設定再利用 実装計画

> 実装担当: `superpowers:executing-plans`を使い、このチャットの親が各Stageを直列に実行する。内部の独立Reviewerが最後に全差分を監査する。

**目的:** 検索からProxy接続、エラー対処、DBツールの設定再利用までを端末内で扱えるようにする。
**構成:** プロファイル保存とProxy状態通知を先に固定し、最後に単一TUIへ統合する。既存status/stopとセットアップを保持する。
**技術:** Go 1.26.9、Cobra 1.10.1、Bubble Tea 1.3.10、Lip Gloss 1.1.0、既存x/sys 0.36.0。
**正本:** [承認済み設計書](../specs/2026-10-15-modern-cli-design.md)。設計commit `c488fbfb6955b342685dcbfc9741a41812e35c36`。
**起点:** `feat/modern-cli-interface`、HEAD `c488fbfb6955b342685dcbfc9741a41812e35c36`、clean。製品コードの起点は`a4965ed`。
**承認:** 設計書を提示した後、2026-10-15にユーザーが「OK進めて」。本書は実装工程の確認用で、実装を開始した証拠ではない。

## 共通制約と進行方法

- 固有lane・追加AGENTS.mdはない。ユーザーのAGENTS指示と`running-staged-development`を適用する。文書・コメント・案内は日本語。
- 通常CLIはCgoなしでmacOS amd64/arm64、Linux amd64/arm64、Windows amd64。セットアップは既存macOS2環境・Windows amd64を維持する。
- UI用の新しいGo依存やメジャー版移行は行わない。既存Lip Gloss、ANSI表示幅、x/sysを直接使う依存区分の整理だけを許可する。
- Goコマンドは`.tools/go/bin/go`、`GOCACHE=$PWD/.cache/build`、`GOMODCACHE=$PWD/.cache/mod`、`GOTOOLCHAIN=local`を共通使用する。
- JSONはVersion=1、最大1MiB。ログは1行64KiB・合計256KiB。Proxy起動確認は60秒。標準ポート候補は最大21件。
- 保存済みPortを暗黙変更しない。同一ConnectionNameは共通Port、別ConnectionNameの予約重複は拒否する。bindの競合もエラーとして扱う。
- 認証ファイル・パスワードを読み出さず、ログイン・IAM/API/ネットワーク設定の変更を自動実行しない。DBパスワードは保存・出力しない。
- プロセス所有と署名付きstatus/stopのプロトコルを維持する。検索worker、Proxyログ、UIが端末へ同時描画しない。
- 利用可能はProxyの接続テスト成功後。statusのrunningは子の生存、DBログインは引き続き未検証。
- 対応関係・状態・副作用の設計変更が必要なら、コード変更を止めて親が差分をまとめる。機械的誤記は実行記録へ記載して補正する。

親は現在のモデル・文脈で設計、実装、focused検証、修正まで所有する。TUI、引数、Proxy、保存が共有状態に依存するため、実装を複数subagentへ分ける引継ぎ・統合の負担を避ける。
新しいユーザー所有Taskは依頼されていないので作成しない。最後の独立Reviewerだけ履歴なしの内部subagentを用い、spec/plan/base/candidate/証拠を渡す。モデル指定はその時点のrouting skillとtool条件で照合する。
自己reviewを独立reviewの代替にしない。原則レビュー1回、blocking修正後は同じReviewerへ再確認1回。BLOCKER/HIGHと本scope内MEDIUMを解消する。
Stage数をTask数へ変換しない。意味のある実装commitは1〜4個を目安とし、各チェック項目だけを理由に細分化しない。
各Stageのfocused成功後に次へ進み、最終候補で全体gateをまとめる。結果は`.superpowers/sdd/2026-10-15-modern-cli/progress.md`へ記録する。

## 事前照合と既存証拠

現在の`selector`はqueryをBackspaceで削除するが、端末サイズと入力カーソルの状態を持たない。新しいTUIへ必要な既存フローは`cli.go`、`selection.go`、`sessions.go`で追える。
Proxy 2.24.1のローカルhelpと公式ソースでstructured-logs/run-connection-testを確認済み。readyログは接続テストより先に出るため、完了判定の代用にはできない。
新しい排他は既存x/sysのUnix Flock／Windows LockFileExで実装可能。ロックファイルは削除せず、ハンドル解放・プロセス終了で解除する。
Ghostty、Terminal、pgAdmin 4のアプリがこの端末にあることを読取り確認済み。pgAdminにSQLITE_PATHを分離する経路があり、検証時はそれ以外のログ・セッション・保存先も一時領域へ分離する。
以上の設計と既存path/APIの照合はREADY。実装コードは未変更。

基準コードa4965edの通常race全体、installer focused race、通常/installer vet、5環境CLI・3環境GUIは成功記録がある。c488fbfは設計文書だけの差分なので、実装前だけの全体テスト再実行は不要。
既知の`TestSessionOwnedLifecycle`には、子がargv.jsonを書く前に親のstatusが公開されて停止されるfixture競合がある。今回の起動確認fixture更新時に、実際の子初期化を待つ観測条件へ直す。テストの失敗履歴を隠したり、固定sleepで回避したりしない。

## ファイルの担当範囲

| ファイル | 担当 |
| --- | --- |
| 新規`profiles.go`、`profiles_test.go` | プロファイルの検証・排他保存・予約・出力契約 |
| 新規`diagnostics.go`、`diagnostics_test.go` | 発生元を含めた原因分類、次の操作、ログの無害化 |
| `sessions.go`、`sessions_test.go` | 所有Proxy、構造化ログ、起動確認、状態snapshot、ポート選択 |
| `selection.go`、新規`selection_test.go` | 検索・フォーム・接続・ログ表示を持つ単一TUI |
| `cli.go`、`cli_test.go`、`discovery.go` | 引数分岐、プロファイルコマンド、TUI／通常出力の接続 |
| `dependencies.go`、既存`process_unix.go`／`process_windows.go` | capability確認、同期的な診断出力、OS固有の排他 |
| `go.mod`、`go.sum`、`README.md` | 既存依存の直接利用、日本語の利用・エラー対処手順 |

Allowed: 上記および影響を受ける既存fixture・installer/PATHテストの期待値更新。必要な小さな注入点は既存Appへ追加する。
Forbidden: 署名プロトコルの変更、gcloud/Proxy配布版・SHA変更、setup/PATH永続化方式の変更、DB認証やネイティブインストーラー画面の再設計、無関係な再分割。

## Stage 1: プロファイルと再利用できる接続先

依存: なし。Primary: profiles.go/test、process_*、cli.go/test、sessions.goの予約を考慮したPort決定。

インターフェース:

- `Profile`は`Name string`、埋め込み`Instance`、`Database, Username string`、`Port int`、`PrivateIP bool`。
- `App.LoadProfiles() ([]Profile, error)`は読取りのみ。ファイル不存在は空配列、壊れた・大きすぎる・未知版のファイルはエラー。
- `App.PrepareProfile(Profile) (Profile, error)`は既存予約と空きポートから保存予定値を決める。まだ書き込まない。
- `App.AddProfile(context.Context, Profile) error`は確定Portを持つ予定値を排他下で再検証して保存。名前・Portが競合すれば書き換えず失敗。
- `App.RemoveProfile(context.Context, string) error`は排他下で名前を削除する。確認は呼出し側が行い、保存ロジックへTTYを持ち込まない。
- `profileExport(Profile, string) ([]byte, error)`と`writeProfileExport(string, []byte) error`をCLIとTUIから共用する。
- `lockProfiles(context.Context, *os.File) (func(), error)`はOS別実装。永久に残す専用lockファイルを排他し、取消可能な待ちで取得、返却関数で解除する。
- `App.profilePort(Instance, int) (int, error)`を既存choosePortの呼出し境界に置き、既存予約の保持・明示Portの一致・未登録先による予約回避を行う。

- [ ] `TestProfilesPersistenceAndPortReuse`で初回保存→別Appで読取り→同一Instanceの別DB/Userで同じPort→別Instanceの重複拒否→削除後の予約解除を保護する。名前・必須値・制御文字・1MiB・Version不整合では元データを保持する。
- [ ] `TestProfilesConcurrentProcesses`で2つの子プロセスの保存が消失しないこと、lock所有プロセス終了で別の保存が進むことを検証する。テスト対象は最終保存結果と取消であり、lock helperの呼出し回数に固定しない。
- [ ] リンクされた設定・lock・出力ファイル、途中書込み失敗で既存ファイルが壊れないことを同じ外部契約テストへまとめる。Unixのpermissionもここで確認する。
- [ ] 保存前にPrepareProfileの結果を表示し、AddProfileが排他下で再検証する。表示後に競合が生じた場合はPortを黙って取り直さず再操作を案内する。読込みにはatomic保存された旧・新の全体だけが見えるようにする。
- [ ] CLIへprofile add/list/remove/export、root/connectへ--profileを追加する。省略値のTTY入力はStage 3へ委ね、非TTY必須値不足・profileと位置引数等の衝突は終了2で拒否する。既存名は上書きしない。
- [ ] `TestProfileCLIAndExports`で一覧APIなしのprofile接続、gcloud/Proxyなしのlist/export/remove確認、取消後の非変更を確認する。profile addは指定project内の完全一致1件だけを登録する。
- [ ] pgAdmin JSONの必須値・SSL指定・パスワード非包含、特殊文字を含むDB/UserのURIをparseして元の値へ戻ること、SQL ServerのURI未対応、--output既存ファイルの非上書きを検証する。CLIのstdout全体をJSON/URIとして解釈し、案内やログが混入しないことも確認する。
- [ ] `TestProfilePortConflicts`で保存済みPortの使用中・明示不一致・空き確認後bind競合を検出し、別Portへ移らないことを確認する。確認済み既存セッションはstatusへ案内し、暗黙attachしない。
- [ ] focused: `.tools/go/bin/go test ./... -run 'TestProfiles|TestProfile|TestPortAndUnresponsiveRecord|TestCLIBadArguments'`。観測可能な失敗をRED確認してから最小実装し、成功を記録する。

## Stage 2: 原因別の対処案内とProxyの状態通知

依存: Stage 1。Primary: diagnostics.go/test、sessions.go/test、dependencies.go、cli.goの通常出力。

インターフェース:

- `FailureContext`に`Source, Operation, Project, Instance, GcloudCommand, CredentialMode string`と`PrivateIP bool`。Sourceはgcloud/proxy/local、CredentialModeはuser/explicit/unknown。
- `Diagnostic`に`Summary, Cause, Next, Detail string`。`explainFailure(FailureContext, string) Diagnostic`をTUI・通常出力へ共用する。詳細の無害化もここで共用する。
- `connectionProgress`はmutexとrevision付きsnapshotを所有する。`Snapshot() connectionSnapshot`は`State string`、`Revision uint64`、`Logs []string`、`Diagnostic *Diagnostic`をコピーして返す。
- `ConnectOptions.Progress *connectionProgress`を追加する。nilなら通常表示、指定時はprogressへの更新だけを行い端末へ書かない。既存Connectはエラーとcleanupを所有し続ける。
- `App.StartupTimeout time.Duration`を追加し既定60秒。テストでは短縮可能。StopGraceとは別に扱う。

- [ ] `TestDiagnosticRecovery`を実際のinvalid_raptログ、gcloud再認証、明示認証ファイル、IAM、API無効、ネットワーク、Port、未知エラーの表形式テストにする。発生元だけを変えた同じinvalid_grantで案内がADC/gcloudへ分かれること、403だけでAPI無効と断定しないことを確認する。
- [ ] 既知の秘密値・制御文字を含むログが画面へ出ず、64KiB行・256KiB保持上限でも最新エラーと省略表示が残ることを保護する。トークンは架空のfixture値を使い、実認証ファイルを読まない。
- [ ] Proxy解決時にstructured-logs/run-connection-test capabilityを確認する。fixtureのhelpも更新し、非対応版はsetup案内で中止する。通常のdoctorへ有効性確認通信は追加しない。
- [ ] `TestProxyReadinessAndRecovery`でreadyだけ、接続テスト成功、失敗、未知ログ、利用中の認証エラー、起動時間切れ、正常停止・異常終了をfake Proxyから流す。readyだけで利用可能にせず、テスト成功後にDBログイン未検証と表示する。
- [ ] stdout/stderrのchunkを行へ組み立てて構造化ログを解釈し、snapshotへ反映する。ログ保持と状態更新を分け、取りこぼしたログがあっても取消・終了状態は保持する。出力解析失敗だけでは終了させない。
- [ ] 通常出力でも同じ原因分類と次の操作をstderrへ表示する。管理版gcloudの実行パス・引用、未導入時のsetup案内を確認する。返却エラーはctx/ErrCancelled/usageの終了コードを壊さない。
- [ ] 所有child停止、Wait一回、制御応答署名、記録削除の既存契約を保持する。既存`TestSessionOwnedLifecycle`では子がargvを書いたという観測後に停止を要求し、旧fixture競合を修正理由とともに記録する。
- [ ] focused: `.tools/go/bin/go test -race ./... -run 'TestDiagnostic|TestProxy|TestSession|TestCLIConnect'`。今回変更した振る舞いのRED→GREENと、既存署名/停止の回帰結果を残す。

## Stage 3: TUIの統合と検索表示修正

依存: Stage 1・2。Primary: selection.go/test、cli.go/test、discovery.goの出力境界、go.mod/sum、README。

インターフェース:

- `connectRequest`は`Query, Project, ProfileName string`、`Refresh, PrivateIP bool`、`Port int`。引数の衝突はcli.goで検証済みの値だけを渡す。
- `App.RunConnectUI(context.Context, connectRequest) error`が単一Bubble Tea Programを所有し、検索開始からフォーム・Proxy終了まで管理する。
- `App.SelectInstance`は既存fixture用の選択だけの入口を保持し、同じselector・表示処理を共用する。別の検索UIを二重実装しない。
- 検索・接続workerの返却Msgに試行generationを持たせる。画面を戻した後や取消後の古い結果を採用しない。
- connectionProgressは100msのtea.Tickで読み、Connect完了はtea.Cmdの結果Msgで受ける。snapshotは値コピーし、画面からworkerの内部状態を書き換えない。

- [ ] 変更前のCSQL_TEST_SELECT fixtureとPTYで検索→削除→再入力、短い端末、長い行、日本語の現象を記録する。queryの状態と端末表示を分け、確認できない条件を再現済みと扱わない。
- [ ] `TestSelectorEditingAndResize`で入力・貼付け・Backspace/Delete・左右/Home/End・Ctrl+U、0件から全件復帰、選択範囲、複数プロジェクト、表示セル幅と60×16境界を保護する。終了コードとEsc/Enterの既存テストも更新して残す。
- [ ] Bubble Teaの代替画面とLip Glossを使い、幅・高さに収まる一覧、詳細、件数、操作キーを表示する。狭い画面はサイズ説明へ切り替え、拡大時に同じ入力から復帰する。NO_COLORでも状態を文字で識別できるようにする。
- [ ] 初期タブ、Tab切替、検索、Ctrl+Sの保存フォーム、profile addの不足値入力を統合する。保存と接続を分け、取消で保存しない。API待ちと描画を分離し、部分失敗をTUIで確認できるようにする。
- [ ] `TestConnectUITransitions`で遅い検索の取消・古い結果の到着、保存前取消、保存失敗、Proxy準備/利用可能、認証エラー→対処表示→明示再試行、所有childの停止完了前の再試行拒否を検証する。
- [ ] 接続中のLでログ表示、E/UでpgAdmin JSON/URI、ファイルパス入力を扱う。プロファイル未保存なら保存フォームへ案内する。URIは表示して端末の文字選択でコピーでき、パスワードを含めない。
- [ ] Ctrl+Cは停止処理中を表示し、所有childとworkerの終了後にだけProgramを終了する。画面幅不足・エクスポート入力中・ログ大量出力中でも終了できる。TUI開始前の失敗は通常の診断で表示する。
- [ ] READMEへ新画面操作、profile add/connect/export、pgAdmin初回取込みと次回再利用、再取込みの重複、Port変更、エラー対処を日本語で記載する。バージョンは互換性を含む確認後に0.2.0へ更新し、生成manifestにも同じ値を使う。
- [ ] focused: `.tools/go/bin/go test -race ./... -run 'TestSelector|TestSelection|TestConnectUI|TestProfileCLI|TestCLI|TestDiscovery'`。非TTYの一覧形式、profile接続、取消130/引数2/その他1の契約も含める。

## Stage 4: 実画面・独立レビュー・最終gate

依存: Stage 1〜3。Primary: 生成dist、既存packager、ignored検証記録。製品の新機能は追加しない。

- [ ] テスト用CLI/Proxy・一時HOME/ConfigDirのfixtureでGhosttyとTerminalの画面を確認する。検索/削除/サイズ変更、日本語、取消、エラー案内と再試行、プロファイル保存・再接続をまとめて確認する。UI操作はCUAで行う。
- [ ] pgAdmin付属の正規Python/実装を使う検証は一時SQLITE_PATHと一時HOME/ログ/セッション/保存先をすべて固定し、既存DBファイルを触らないと確認してから行う。出力JSONの取込み→登録内容を公式exportで確認し、実DBへ接続しない。隔離不能なら既存設定へ代入せず、未確認項目として報告する。
- [ ] focused成功と自己確認後にcandidateをcommitしcleanを確認する。Reviewerへbase `c488fbf`・candidate・全差分・spec/plan・テスト/画面証拠を渡す。任意refactorを増やさず、必要な修正は同じ親が実装して同じReviewerへ戻す。
- [ ] 最終候補の`go test -race ./...`、`go test -race -tags installer ./... -run 'TestInstaller|TestSetupPathCLI'`、`go vet ./...`、`go vet -tags installer ./...`、gofmt/diff checkを成功させる。GOCACHE=.cache/build、GOMODCACHE=.cache/mod、GOTOOLCHAIN=localを共通指定する。
- [ ] CGO_ENABLED=0で通常CLIの5環境をビルドし、既存`python3 scripts/build-installers.py`で3環境を生成する。packagerの埋込CLI/manifest/CPU/GUI subsystem/ZIP権限/macOSアドホック署名の検証を確認する。dist/csqlも新しいarm64 CLIへ同期する。
- [ ] gateは同一製品blob・plan/scope・argv・toolchain・環境での成功証拠を再利用できる。新しい変更・失敗・未解決懸念がある範囲だけ再実行する。別返信を理由に全体テストを反復しない。
- [ ] 最終報告は変更、fixed candidate、focused/全体検証、独立レビュー、実画面で確認したOS/アプリ、pgAdmin取込みの範囲を分ける。Windows/Linuxの実端末、公開署名・公証、実GCP/DB接続は実行していなければ未確認と明示する。

push・PR/MR・merge・deploy、実ユーザー環境の再導入、公開用署名・公証は本計画の実行範囲に含めない。配布ファイルの更新と導入用コマンドまでを用意する。

## レビュー重点と止める条件

1. 保存確認の後で他プロセスが予約を変えた場合: Stage 1の複数プロセス・Port競合試験で、表示と違う接続先を保存しないことを確認する。
2. readyログ・成功ログ・エラーログの順序差、分割Write、stdout/stderrの交錯: Stage 2のfake Proxyで、成功の早出しや対処の喪失を検出する。
3. 起動中/ログ表示/フォーム中の取消と遅い結果: Stage 3のgeneration付き試験で、再起動・保存の再実行や所有child残留を検出する。
4. 長い日本語、URL予約文字、端末制御文字・秘密らしい文字列: Stage 1のエクスポートとStage 2/3の表示試験で、値の誤解釈・漏出・画面崩れを検出する。
5. 単体テストと実アプリの差: Stage 4でpgAdmin取込みと両端末の実画面を確認し、未確認OSをビルド成功だけで検証済みにしない。

baseline由来の失敗も最終成功へ混ぜない。設計の外部契約に矛盾が見つかった場合はNEEDS_SPEC_FIXとして差分を提示し、実装担当の判断だけで変更しない。
必要な独立Reviewerまたは実画面の検証経路が利用不能なら、影響しない実装・focused検証を進めた上で、不足条件を報告する。自己監査で代用して完了とはしない。
