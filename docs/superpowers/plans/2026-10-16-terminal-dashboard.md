# 接続ダッシュボード改善の実装計画

> 実装担当向け: `superpowers:executing-plans`を使い、このチャットの現在の担当が直列に実装する。段階ごとに実装担当やユーザー所有Taskを作らず、完了した項目をチェックする。

**目的:** 更新通知で現在位置を失わず、接続を列で比較し、操作を検索して、エラーからコピー・再試行へ進めるようにする。

**構成:** 現在のBubble Teaモデルを維持する。接続ID・試行番号・検索世代を使って操作対象を固定し、F1は元の状態を置き換えない表示層として扱う。外部クリップボード処理だけを1ファイルに分離する。

**技術:** Go 1.26.9、Cobra、Bubble Tea、Lip Gloss、既存のANSIセル幅処理。依存追加なし。

**設計:** `docs/superpowers/specs/2026-10-16-terminal-dashboard-design.md`。2026-10-16利用者承認済み、設計コミット`28c07ee40d8f27410440738a93c2bde0ec275d4a`。

**基準:** 公開main `9fab4bd493ce13a2f5fa96630fd137917dec367c`、作業ブランチ`feat/terminal-dashboard`。既存の作業場所と公開前履歴のバックアップを継続利用する。

## 共通契約と範囲

- ヘッダー2行・本文・フッター2行の構成を保ち、更新通知でも3タブと現在位置を表示する。更新先全文はF1へ移す。
- 接続一覧だけ、横100列・縦20行以上で左右2ペインと1接続1行の列表示。それ未満は縦配置と2行表示。最低60列×16行、未満はサイズ案内と終了操作。
- 検索欄への通常文字は検索、操作メニュー内では操作の検索。既存のEnter、Ctrl+O/E/L/S/D/R、Ctrl+P/N、Esc、Ctrl+Cを維持する。追加は一覧・検索エラーのCtrl+K、F1ヘルプ。
- 操作・コピーは起点の対象を固定し、確定時にも検証する。背景イベントで入力・対象・戻り先を奪わない。
- 認証コマンドは提示・コピーだけとする。gcloud検索認証、通常ADC、明示ADCを区別し、権限/API設定を自動変更しない。
- `CSQL_THEME=auto|dark|light`、既定auto、`NO_COLOR`最優先。テーマ用の端末入力問い合わせ・永続設定ファイルは追加しない。
- 署名通信、Proxy所有権・停止待機、保存形式Version 1、ポート予約・更新排他、4形式出力・非上書き、保存だけでは接続しない契約を維持する。
- 新規製品ファイルは`clipboard.go`だけ。`sessions.go`の変更は診断コマンドのSnapshot複製だけとし、セッション制御・永続化・外部認証プロトコルを変更しない。
- 版番号変更、push、PR、マージ、リリース、端末へのインストール更新、実クリップボードの自動検証は範囲外。

## レビュー重点

1. 操作を検索した後の行番号で別操作を実行する、または背景の再試行後に古い対象を操作する。段階2のメニュー確定・旧試行配送テストで保護する。
2. F1表示中に検索・保存・接続が完了し、ヘルプや入力が消える、閉じた際に完了前の状態へ戻る。段階2で実際のイベントを配送する。
3. コピー開始後に別接続・フォームへ移り、古い完了結果が新しい画面を上書きする。段階2の遅延コピー・取消テストで保護する。
4. 日本語・空白・引用符を含むパスやログ由来の文字列が、シェルコードとして実行・コピーされる。段階1の偽実行ファイルと診断往復、段階2の表示・コピー一致で保護する。
5. 更新通知・長い名前・エラー・NO_COLORが重なり、タブ・選択行・終了案内が消える。段階2の幅検証と段階3のPTYで保護する。

## 段階1: 診断コマンドとコピー境界

**対象:** `diagnostics.go`、`cli.go`のApp差替え入口、`clipboard.go`、`sessions.go`のSnapshot、`diagnostics_test.go`、`clipboard_test.go`。別プロセスfixtureは既存`sessions_test.go`のTestMainを必要最小限拡張する。

**公開する画面内契約:**

- `type RecoveryCommand struct { Label, Text string }`を`diagnostics.go`に置き、`Diagnostic.Commands []RecoveryCommand`を加える。既存のSummary/Cause/Next/Detailと`formatDiagnostic`は保つ。
- Appに`CopyText func(context.Context, string) error`を加える。`func (a *App) copyText(ctx context.Context, text string) error`が実行と後処理を合わせて最大2秒の予算を設け、差替えがあれば使用し、通常はOS別コマンドを起動する。

- [x] 既存`TestDiagnosticRecovery`とWindows/未導入の検証へ、通常ADC・検索認証・明示ADC・未導入・ポート競合・不明エラーのCommandsを追加する。外部ログの「次の操作」や秘密値をコピー候補へ採用しないことを確認する。型未定義だけのコンパイル失敗を機能のREDとは数えない。
- [x] `explainFailure`の既存分岐でCommandsを設定する。検索認証は引用済みgcloudの`auth login`、通常ADCは`auth application-default login`。明示ADC・権限・API・通信・設定・不明は`csql doctor`、ポート競合は`csql status`と`csql profile list`、ツール不足は`csql doctor`と`csql setup --install`。gcloud未導入なら認証コマンドを有効な候補にせず導入の2件を優先する。
- [x] SnapshotでCommandsのスライスを複製し、診断の共有データを画面側から書き換えない。
- [x] `TestClipboardBackendExecution`で偽の子プロセスへ日本語・空白・単引用符・二重引用符を含むコマンドを渡し、stdinのバイト列が原文のまま、argvに入力テキストが混ざらないことを確認する。macOS/Windows/Linuxの実行選択と固定引数を同じテストで検証し、利用者の実クリップボードは使わない。
- [x] `copyText`は`exec.CommandContext`に引数配列とstdinを別々に渡す。macOSは`pbcopy`、Windowsは`powershell.exe -NoLogo -NoProfile -NonInteractive -Command`と固定のUTF-8読込み・Set-Clipboard処理。LinuxはWAYLAND_DISPLAYがあれば`wl-copy`を優先し、それ以外は`xclip -selection clipboard -in`を優先、見つからなければ他方を探す。ツールの導入・shell経由の実行・OSC52を行わない。
- [x] `TestClipboardFailures`で未導入、子の非0終了、2秒上限、親context取消を確認する。実行contextを最大1.8秒、`WaitDelay`を200msとし、実行後に別の2秒待機を積み上げない。子の出力は既存`limitedWriter`で最大16KiBとする。これはOS外部境界の統合検証とし、単純な委譲回数のテストは作らない。
- [x] focused raceを実行し、診断CLI・ログの秘密除去・既存の失敗保持が回帰しないことを確認してローカルコミットする。

focused: `go test -race ./... -run 'TestDiagnostic|TestProxyLog|TestProxyRetains|TestClipboard' -count=1`。

## 段階2: 固定画面・操作メニュー・ヘルプ・復旧

**対象:** `presentation.go`、`selection.go`、`cli.go`の必要な画面入口、`selection_test.go`、`connections_test.go`、`updates_test.go`、README。段階1のCommandsとcopyTextを使用する。

**型と入口:**

- `presentation.go`に`uiTheme`を置き、accent/muted/success/warning/error、選択の前景/背景、反転選択の情報を保持する。`func parseUITheme(env func(string) string) (uiTheme, error)`を画面開始時だけ呼ぶ。selectorとterminalViewへ同じテーマを渡す。
- `terminalChoice`へ接続一覧用の表示列を加え、`choiceView`に接続表を示す情報を持たせる。状態の短い表示名への変換は描画だけに閉じ、`uiConnection.status`や進捗の意味を変えない。
- `selection.go`の操作を`uiAction`（ID、表示名、検索補助語、無効理由、任意のRecoveryCommand）で表す。`actionContext`に開始時のstate、listLocation、profile、owner/attempt、検索generation、診断を保持する。クエリ・入力位置・カーソルは一覧と別にする。
- `availableActions() []uiAction`、`filteredActions() []uiAction`、`executeAction(uiAction) tea.Cmd`を同ファイルにまとめる。既存の番号依存`workspaceAction`を意味のあるID実行へ寄せ、ショートカットも同じ入口を使う。
- F1は`helpOverlay`（開始時の説明行とページ）をモデルへ加え、stateを書き換えない。コピーは`uiClipboardResult`（専用操作番号、owner/attempt、検索generation、Text、err）で返し、保存・出力用のoperationを上書きしない。

### 描画・テーマ

- [x] 既存の`TestStartupUpdateNoticeLayouts`と更新キャッシュ検証を更新し、60×16・100×20・120×26、一覧/フォーム/操作/保存/終了中で「更新版＋タブ＋終了キー」が共存し、F1で更新先全文を確認できる契約を先に検証する。URLが常時タブを置換する旧挙動を失敗として確認する。
- [x] `terminalView.frame`で更新を最上段の小さなバッジへ変更し、2段目のタブを維持する。長い版番号でも優先順位に従ってアプリ名・補助文を省略し、更新版と終了案内を隠さない。F1の入口をフッターへ統一する。
- [x] `TestRichListLayout`と接続画面の既存検証へ、広幅で5列と複数行のPort/DBが対応すること、長い日本語名の省略、選択変更・100列境界のリサイズ、狭幅での2行表示・空欄の代替文を追加する。名前やPortが混線する回帰を対象とし、全画面の完全一致snapshotは固定しない。
- [x] 接続表は「状態／設定・接続先／プロジェクト／Port／DB」。設定名を優先し、未保存はインスタンス名、未指定DBと未確定Portを明示する。セル幅で省略・埋め合わせし、稼働状態で行順を変更しない。選択詳細のセッションIDはF1へ移す。
- [x] `workspacePanel`と検索エラー表示は失敗操作・原因・次の操作を先頭へ置き、詳細はCtrl+Lへ残す。空のDB/ユーザー表示と重複した原因を先頭へ置かず、「DBログイン未検証」を確認範囲として表示する。確認結果は既存SnapshotのStateだけから描画する。
- [x] `CSQL_THEME`は未指定/auto、dark、lightのみ受け付ける。autoはCOLORFGBGの末尾が0/8なら暗色、7/15なら明色、それ以外は標準文字色と反転選択を使う。不正値では許容値と修正例を案内して画面開始前に終了し、version/help/list/statusなど非対話コマンドへ影響させない。
- [x] 暗色は現在のアクセントを基に、明色は濃い青のアクセントと暗い補助色を使う。テーマの色はpresentation.goへ集約する。画面全体の背景変更や背景色問い合わせを避け、色が未指定の役割は標準文字色を使う。NO_COLORでは文字と記号を残し、選択背景・反転を含むSGRを出さない。
- [x] 既存レイアウト検証をautoの既知/未知背景、dark、light、NO_COLORへ拡張する。入力を読む端末問い合わせやテーマ専用の外部実行がないこと、不正なCSQL_THEMEでも非対話コマンドが従来どおり動くことをAppの入口から確認する。

### 操作・ヘルプ・コピー

- [x] `TestConnectionsWorkspaceActions`と`TestConnectionsWorkspaceSelection`へ、Ctrl+K、操作の日本語/英語検索と消去、検索後の確定、無効理由、0件、Escの戻り先を追加する。F1/Ctrl+Kは既存harnessのキー配送に追加し、実際のモデルUpdateを経由する。
- [x] 操作のIDはconnect/save/export/logs/port/stop/retry/refresh/copy-recoveryとする。copy-recoveryは候補内に固定したCommandを持ち、可変の行番号でコマンドを引き直さない。開始時の対象から項目を作り、選択中の別行を参照しない。
- [x] 実行前に同じID・attemptまたは検索generation・診断コマンドの一致を再検証する。稼働中/停止中/未保存などの条件はメニューと直接キーで共通化する。保存済み設定のEnterは接続のまま、フォーム中はCtrl+Kで別操作を開かない。
- [x] F1/EscでhelpOverlayだけを開閉し、PgUp/PgDnで説明を送る。Ctrl+Cを最優先で処理する。文字キーは背後の入力へ送らず、元のstate・フォーム・戻り先を保つ。背景処理が完了した場合、閉じた後は完了済みの最新stateを表示し、古いsaving状態へ巻き戻さない。
- [x] `TestConnectionsHelpAndBackgroundResults`でフォーム入力・操作検索中のF1、接続の成功/失敗/停止、検索結果、保存完了を配送し、ヘルプが開いたまま・戻り先保持・再表示時の完了反映を確認する。F1からのCtrl+Cも所有接続だけを終了する。
- [x] コピーはメニューで1件を選んだときだけ開始する。画面の折返し前のCommand.Textを保持し、`m.work`経由でcopyTextを呼ぶ。専用の番号とbusy状態で二重起動を防ぎ、F1/リサイズ/終了処理を妨げない。
- [x] `TestConnectionsRecoveryCopy`で偽CopyTextを遅延させ、別接続・別試行・フォームへ移った後の結果が画面を上書きしないこと、連打、取消、失敗時の全文・手動案内、成功時の「別ターミナルで実行→戻って再試行」を確認する。検索の認証とProxyのADCで実際に選ぶ文字列を検証する。
- [x] コピー失敗時にもコマンド全文を既存のページ送りで読めるようにする。専用操作番号が一致する結果は表示先の検証に先立ってbusyを解除し、古い対象からの完了でコピー不能のまま残さない。コピー対象に一致した結果だけを現在の一覧へ表示し、別操作中の結果は入力・保存結果を上書きしない。ctx取消時はworker終了を待つ。
- [x] 検索の再取得は現在のrequestの検索範囲を保ってRefreshを有効にする。接続の再試行は既存stopConnectionの停止待機を使う。認証・導入・IAM/API変更コマンドの自動実行を追加しない。
- [x] READMEへ新メニュー・F1・テーマ・認証復旧の操作と、コピー先コマンドは別端末で実行すること、ツール未導入時の手動コピーを追記する。
- [x] focused raceを実行する。既存の複数接続、4形式出力、ポート編集、検索後の所有権、保存の非起動、長い診断・ログ・端末復元の検証を再利用し、UI変更をローカルコミットする。

focused: `go test -race ./... -run 'TestSelector|TestSelection|TestConnectUI|TestConnections|TestRich|TestStartupUpdate|TestDiagnostic|TestClipboard' -count=1`。

## 段階3: 独立レビュー・最終検証

- [ ] 候補SHAとclean状態を固定し、履歴なしの独立Reviewer1名へ設計・計画・base/candidate・focused結果を渡す。全差分と5つの重点条件、認証案内・副作用・入力/終了境界を監査する。必要な修正は同じ実装担当が行い、同じReviewerが候補全体を再確認する。
- [ ] 候補に対し`go test -race ./... -count=1 -timeout=90s`、`go test -race -tags installer ./... -run 'TestInstaller|TestSetupPathCLI' -count=1`、`go vet ./...`、`go vet -tags installer ./...`、gofmt、`git diff --check`を実行する。
- [ ] 同候補からCLI5環境（darwin/arm64・amd64、linux/arm64・amd64、windows/amd64）をCGO_ENABLED=0、`-trimpath -buildvcs=false`でビルドする。`python3 scripts/build-installers.py`でMac2版・Windows版の同梱CLI/manifestと署名・構造を確認する。過去の版別releaseを保持する。
- [ ] 既存`.superpowers/sdd/2026-10-16-connection-workspace/verify-release-pty.py`を参考に、今回用のPTY手順を除外済みの作業ディレクトリへ作る。実クリップボードを使わないよう、既存runUIFixtureへ偽コピー入口を追加する。製品用の待機フックは増やさない。
- [ ] auto（COLORFGBG未設定）・light・NO_COLORの3条件で、複数接続→表と狭幅→日本語検索/消去→検索付き操作の確定/取消→F1と背景結果→認証エラーの偽コピー/失敗/再試行→4形式出力→保存→停止後ポート変更→再接続→Ctrl+Cを確認する。darkと既知背景autoはfocused描画テストで確認する。別端末の接続維持と代替画面の復元も観測する。
- [ ] 同一候補SHA、argv、Go版、GOCACHE/GOMODCACHE、結果、設計への適合と未確認範囲を`.superpowers/sdd/2026-10-16-terminal-dashboard/`へ記録する。新しい変更・失敗・懸念がなければ成功済みgateを繰り返さない。
- [ ] 実DB接続、DBツールGUI取込、Windows実機のSet-Clipboard/セットアップ操作を未確認として区別する。製品の受入条件と工程の完了を分け、公開・端末更新済みとは報告しない。

## 実行方法と停止条件

Goは`.tools/go/bin/go`、`GOTOOLCHAIN=local`、`GOCACHE=$PWD/.cache/build`、`GOMODCACHE=$PWD/.cache/mod`を使う。上記のgoはこの実体を指す。loopback・別プロセス・PTY・Git管理情報の更新は必要に応じて環境の承認機構を使う。

設計・計画は現在のモデル設定を維持し、切替済みとは扱わない。画面状態、操作の対象、workerの結果配送が密接に連動するため、親が同じ文脈で直列実装する。新しいユーザー所有Taskは未依頼のため作らず、独立性が必要なレビューだけ内部委譲する。意味のある実装コミットは1〜3件を目安とし、テスト・docsを工程ごとに細切れにしない。

この計画の承認後は、実装・独立レビュー・補正・同一候補の最終検証まで継続する。同じ範囲の途中承認は繰り返さない。保存形式、認証/制御通信、所有権、承認済み操作やscopeを変える必要が出た場合は設計へ戻し、製品変更の前に差異を確認する。
