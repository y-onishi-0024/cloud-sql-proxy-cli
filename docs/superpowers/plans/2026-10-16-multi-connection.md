# 複数接続管理 実装計画

> 実装担当は承認された実行方法に従い、`superpowers:executing-plans`または`superpowers:subagent-driven-development`で以下を直列に実施する。担当を工程ごとに変更しない。

**目的:** 1つのcsql画面で接続を追加・切替・個別停止し、他の接続を維持する。
**構成:** 既存のProxy実行処理に起動待ちを含むポート予約を加え、TUIの単一接続状態を接続ごとの状態へ移す。検索・編集・更新確認は画面が所有し、接続の終了通知は接続IDと試行番号で配送する。
**技術:** Go 1.26.9、Cobra、Bubble Tea 1.3.10、既存Lip Gloss/x/ansi。新規依存なし。
**正本:** [承認済み設計書](../specs/2026-10-16-multi-connection-design.md)。2026-10-16にユーザーが「設計を承認し、実装計画の作成へ進める」を選択した。
**基準:** 公開main `f96975546423c6d5df289c563a9e4e477cb54267`、v0.2.3。本書は計画確認用で、実装開始の証拠ではない。

## 共通制約

- 固有lane・追加AGENTS.mdなし。ユーザーのAGENTS指示と`running-staged-development`に従う。文書・コメント・案内は日本語。
- 既存の署名付きstatus/stop、管理記録・プロファイル・pgAdmin JSON・URIの形式、認証/権限の扱いを維持する。
- 非TTYのconnectは既存の単一接続。常駐サービス、切離し起動、自動ログイン、DBパスワード管理、新しいGo依存は追加しない。
- 初期ポート範囲は既存のDB種別ごとの21候補。保存済みポートを暗黙変更しない。待受は127.0.0.1だけ。
- Proxyの利用可能は既存の接続テスト成功後であり、DBログインは未検証。起動確認60秒・通常停止猶予10秒・ログの既存上限を維持する。
- 画面は60列×16行以上、100列×20行以上で2ペイン。NO_COLOR、固定操作ガイド、長いエラー案内のページ送りを維持する。
- 画面の全終了で停止するのは自分が起動したProxyだけ。外部の全セッションに対する`Stop --all`は呼ばない。
- 製品版は0.3.0とする。複数接続の追加とEscの意味の変更をREADMEに明示する。公開済みv0.2.3のタグ・配布物を置換しない。
- 実GCP/DB、実pgAdmin設定、既存の稼働Proxyを使って検証しない。

## 作業場所・担当・完了境界

元の作業場所は`feat/modern-cli-interface`、HEAD `df06daf65109578d93583028af95adac8de5a1ad`に過去の承認済み変更が未コミットで残る。その履歴を公開用mainへ混ぜず、reset/stash/一括commitしない。
公開用の独立コピー`.cache/github-public-v0.2.2`はmain/f969755、cleanであり、製品ソースは元の作業場所と同内容。実装開始時に状態とremote/mainを再確認し、このコピーに`feat/multi-connection`を作成する。main上では実装しない。
作業開始前に元のソースと設計/計画のhashを記録する。最終候補を元の作業場所へ反映する際は対象ファイルの基準hashを照合し、利用者による追加変更と競合する場合は上書きせず報告する。

推奨はこのチャットの親が設計・実装・focused検証・修正まで担当し、最後に履歴なしの独立Reviewer1名が全差分を監査する方式。
ポート、接続ID、保存対象、終了待機が連動するため実装を並列分割しない。新しいユーザー所有Taskは作成しない。内部へ委譲する場合も実装・検証・修正を同じ1名へまとめる。
現在の担当は既存の文脈を継続し、モデルを切替済みと主張しない。独立Reviewerのモデル・effortは起動時のrouting skill/tool契約で照合する。

意味のある変更はポート管理とTUI統合を中心に2〜3コミットを目安とし、各チェック項目では細分化しない。コミット名は`feat:` / `fix:` / `docs:`などの接頭辞と日本語の要約にする。
独立レビューは原則1回と補正後の同じReviewerの再確認1回。BLOCKER/HIGHと受入条件を妨げるMEDIUMは解消する。
push・main向けPR作成は実行承認に含まれる場合だけ行う。PRのmerge、mainへの直接push、新リリース公開は今回の実行範囲に含めない。

## 変更するファイルと責務

| ファイル | 責務と変更 |
| --- | --- |
| `cli.go` | App単位のポート予約の同期と版0.3.0。引数の既存契約を保持 |
| `sessions.go` / `sessions_test.go` | Connectのポート取得・解放、偽Proxyで並行起動/終了を検証 |
| `profiles.go` / `profiles_test.go` | 起動待ちのポートを割当候補から除外。保存形式・固定予約は変更しない |
| 新規`connections.go` / `connections_test.go` | TUIの接続別状態、開始/停止/再試行と完了通知の配送 |
| `selection.go` / `selection_test.go` | 3タブ、選択、編集対象の固定、検索と接続の共存、終了待機 |
| `presentation.go` | 既存の描画部品で接続一覧・詳細・操作ガイドを表示 |
| `updates_test.go` | 接続モデル変更と版更新に応じた既存通知テストのfixture調整 |
| `README.md`、本書、承認済み設計書 | 複数接続、キー変更、所有範囲、非TTYの説明 |

新しい製品ソースは`connections.go`の1ファイルに限定する。追加処理は「選択→接続別状態→既存Connect」の3ファイルで追える構成にする。独立した状態遷移を検証する価値があるため、その回帰テストを`connections_test.go`へまとめる。汎用helper/service群を新設しない。
`process_*`の終了方式、`dependencies.go`、認証診断、`updates.go`、インストーラー実装、go.mod/go.sumは変更禁止。設計上必要になった場合は実装を止めて差異を親へ戻す。

## レビュー重点

1. 同時起動で最初のProxyがまだbindしていないときも別ポートを割り当てる（工程1で実プロセスの待機fixtureを検証）。
2. 古い試行や非選択接続の完了通知が、現在のログ・フォーム・再試行を変更しない（工程2）。
3. 保存/出力中の接続終了と別接続の状態更新が、対象のDB名・ユーザー・ポートを取り違えない（工程2）。
4. 未配送Cmd、起動直前、強制停止待ちでの終了が、worker残留やWaitGroupの後追いAddを起こさない（工程2・最終PTY）。
5. 接続が存在する状態の検索失敗・Tab/Esc・狭幅表示が、全終了や他端末の停止を引き起こさない（工程2・最終PTY）。

## 工程1: 起動待ちを含むポート予約

依存: 基準の確認。主対象: cli.go、sessions.go、profiles.goと既存テスト。

インターフェース:

- `App`にmutexと`map[int]bool`の実行中ポート予約を保持する。永続化・プロセス間の所有権判定には使わない。
- `func (a *App) reserveProxyPort(v Instance, explicit int) (int, func(), error)`は排他下で割当と予約を一度に行い、解放関数を返す。Connectは取得成功直後に解放をdeferし、子のWait/記録削除後にだけ解放される順序にする。
- `func (a *App) reservedProxyPorts() map[int]bool`は排他下で値コピーを返す。読者へ内部mapを渡さない。
- `profilePort(v Instance, explicit int, unavailable map[int]bool)`、`availableProfilePort(v Instance, explicit int, rows []Profile, unavailable map[int]bool)`は予約済みポートを避ける。明示/保存済みポートならエラー、未指定なら既存の21候補から選ぶ。
- PrepareProfileは予約のコピーを利用する。稼働中の保存は従来の署名確認済みセッションとの一致条件で実ポートを再利用する。予約mutexを持ったままstatus HTTPや設定ファイルの排他書込を実行しない。

- [ ] `TestConcurrentConnectPortReservations`で同じAppから未指定ポートのConnectを2件開始する。偽Proxyをbind前で待機させ、別ポートの管理記録が生成されること、待機解除後に両ポートへ実際に接続できることを確認する。最初のbindを待ってから2件目を始めるだけのテストにしない。
- [ ] 偽Proxyの必要な制御をsessions_test.goの既存TestMainへ追加する。外部時刻の固定sleepで順序を仮定せず、開始/解放の観測で同期する。
- [ ] `TestConcurrentConnectReservationRelease`で1件の終了/起動失敗後にそのポートを再利用でき、もう1件が稼働し続けることを確認する。保存済み/明示ポートの競合が勝手な変更にならないことは既存TestProfilePortConflictsを拡張する。
- [ ] それぞれの観測可能な回帰のREDを確認してから、予約と除外を実装する。private helperやmutexの呼出回数だけのテストは作らない。
- [ ] focused: `go test -race ./... -run 'TestConcurrentConnect|TestProfilePort|TestProfiles|TestSessionOwnedLifecycle|TestSessionCancelAndForcedStop'`。終了・署名の既存契約を維持してPASS。

## 工程2: 接続別状態と画面の統合

依存: 工程1。主対象: connections.go/test、selection.go/test、presentation.go、updates_test.go、README。

接続のインターフェース:

- `uiConnection`は`id, attempt uint64`、`profile Profile`、`progress *connectionProgress`、`cancel context.CancelFunc`、`running, stopping, retry bool`、`err error`、`diagnostic *Diagnostic`、`showLogs bool`、`recoveryPage int`を持つ。
- idは画面内で一意かつ再利用しない。署名付き管理セッションIDとは別物。attemptは同じ接続の再試行開始時に増やす。
- `connectionFinished`は`id, attempt uint64; err error`を返す。接続の完了はこの組で照合し、検索generationや現在の選択行番号では配送しない。
- `startConnection(p Profile) tea.Cmd`、`stopConnection(id uint64, retry bool) tea.Cmd`、`finishConnection(v connectionFinished) tea.Cmd`をconnectUIのメソッドとしてconnections.goへまとめる。再試行は既存IDの終了後に新しいattemptを開始する。
- 同じConnectionNameの稼働/起動/停止中接続を重複起動しない。PrivateIPと明示ポートが一致すれば詳細へ移動し、不一致なら理由を案内する。別の保存済みプロファイルで同じProxyを再利用する場合は、選択したDB/ユーザーを詳細と出力へ反映する。設定名のないインスタンス選択で既存の保存情報を消さない。

画面・workerのインターフェース:

- `connectUI`は接続の集合、次のid、選択中のid、`tabInstances/tabProfiles/tabConnections`、検索・編集・出力・全終了を所有する。接続のprofile/progress/errorを別のグローバル変数へ複製して持ち続けない。
- 検索generation、保存/出力の操作番号、接続のid/attemptを区別する。初期`--port`は最初の接続へだけ適用し、追加接続へ再利用しない。追加のインスタンス検索では初期の名前絞込みを解除し、明示したprojectの検索範囲と未保存接続のPrivate IP指定は保持する。
- 編集は`editProfile Profile`と対象id/attemptをフォーム開始時に固定する。prepared/save/exportのuiResultにも対象を付ける。接続の完了では編集中の画面や入力を奪わない。
- 実ポートはprogress snapshotから読む。ポート未確定の接続で保存を要求されたら、確定を待つ案内を出す。確定後は従来の保存確認と固定ポートを使う。
- `work(ctx context.Context, cancelled tea.Msg, run tea.Cmd) tea.Cmd`へ既存のworkを変更する。開始前に取消された場合も、呼出側が指定した型の取消結果を返す。全workerに同じworkMu/workClosed/WaitGroupの入場契約を使う。
- Tickは接続ごとに増やさず、画面につき1本の100msチェーンで全接続のsnapshotを読む。`uiTick`は`epoch uint64`、画面は`tickEpoch uint64`と`tickPending bool`を持つ。`ensureTick() tea.Cmd`は未配送Tickがあれば追加せず、なければepochを増やして100ms後を予約する。受信時はepoch一致を確認してpendingを解除し、全snapshot反映後、検索/保存/所有接続の処理があれば次を予約する。検索generationとは独立し、未使用時は止める。
- `beginShutdown() tea.Cmd`は入場ゲートを閉じてcancelAllを呼び、WaitGroup完了後に`uiShutdownComplete`を返す。自身をそのWaitGroupへ登録しない。未配送Cmdを待機数へ含めず、後追いAddを許さない。runConnectModelの外部取消/異常終了時も同じゲート閉鎖・取消・待機を行う。
- `profile add`は既存の単独フォーム入口として維持する。接続追加ごとにInitや更新確認をやり直さない。

キーと表示:

- 一覧のTabは3タブを循環。profile addでは既存の選択/フォームだけを扱う。検索中でも接続一覧へ切替可能にする。
- 詳細のNで追加（インスタンス一覧）、Sで選択接続を停止、Rでその接続だけ再試行。Escは接続一覧へ戻る。
- 接続一覧のEnterで詳細、Ctrl+Dで選択した1件を停止。N/Sなど通常の文字は検索文字として扱い、文字入力から停止させない。
- 一覧のEscは、稼働/起動/停止処理中の所有接続があれば接続一覧へ戻る。それ以外は取消終了。検索中のEscは検索だけを取消し、接続があれば接続一覧へ戻る。
- フォーム/出力のEscは元の画面へ戻るだけ。入力欄のN/S/R/E/Uなどは常に文字入力。保存中は表示移動を解禁しない。
- Ctrl+Cはどの画面・画面サイズでも全終了へ進める。ガイドに「この画面の全接続停止」を表示する。終了コード130を維持する。
- 接続一覧は起動順、稼働件数、プロジェクト/名前/実ポート/状態を表示。停止済み/失敗を残して詳細と再試行へ戻れる。非選択接続のエラーも一覧で判別可能にする。

- [ ] `TestConnectionsAddSwitchAndStop`で2接続を起動し、N/Tab/Enter/Escで移動しても両方が稼働すること、片方の停止後も他方で新しいTCP接続を受けられることを確認する。
- [ ] `TestConnectionsDelayedResultsAndRetry`で停止待ちの二重追加/再試行、別接続の失敗、旧attemptの遅延完了、別CLIのstopを確認する。新しい試行・他のログ・選択先を変えないことを観測する。
- [ ] `TestConnectionsSaveAndExportOwnership`でAの保存/pgAdmin/URI出力中にBが終了するケースと逆のケースを検証する。出力先・DB名・ユーザー・実ポート・未完了ファイル後片付けが正しいことを確認する。
- [ ] `TestConnectionsShutdownIsolation`で複数の所有Proxyと別App/別CLIのProxyを起動し、Ctrl+C/外部取消で所有分だけが終了することを確認する。未配送Cmd・開始直前・保存中・強制停止待ちも検証する。
- [ ] 既存の検索/日本語入力/狭幅/フォーム/エラー案内テストは構造の変更に必要な部分だけ更新する。3タブと接続数が多い場合のスクロール、検索中の切替、60×16で操作キー/更新通知を確認する。
- [ ] 版更新により通知テストの意味が失われないようupdates_test.goを更新する。数値比較の新版はv0.10.0（0.3.0より新しい）、同版はv0.3.0、通知を抑制する各異常ケースは正常なら新版となるタグを使う。scope/opt-outとレイアウトのcache/HTTP fixtureもv0.4.0以降にし、同版だから通る偽陽性を防ぐ。
- [ ] READMEへ追加/個別停止/全終了、Escの変更、他端末を停止しないこと、固定ポート、同じインスタンスの再利用を記載する。cli.goを0.3.0へ更新する。
- [ ] focused: `go test -race ./... -run 'TestConnections|TestConnectUI|TestRich|TestStartupUpdate|TestProfileCLI|TestCLI|TestSelector|TestSelection'`。新規の振る舞いのRED→GREENと既存回帰のPASSを残す。

## 工程3: 独立レビュー・最終検証・引渡し

依存: 工程1・2のfocused成功。1名の実装担当が固定候補まで所有する。

- [ ] 全差分の自己確認後、featureブランチに候補をcommitし、HEAD/clean、正本のhash、全差分範囲とfocused結果を記録する。レビュー開始条件へレビュー完了を要求しない。
- [ ] 実装者と別のfresh Reviewerへbase/candidate、絶対パス、spec/plan、全差分と影響周辺、既存証拠を渡す。BLOCKER/HIGH/受入条件を妨げるMEDIUMのI指摘は同じ実装担当へ戻し、補正後は同じReviewerへ確認する。
- [ ] 最終候補で`go test -race ./...`、installer focused race（`-tags installer -run 'TestInstaller|TestSetupPathCLI'`）、通常/installer両vet、gofmt、diff-checkを行う。
- [ ] macOS arm64/amd64、Linux arm64/amd64、Windows amd64のCLIをCgoなしでビルド。`scripts/build-installers.py`でMac2版・Windows版を生成し、版0.3.0・同梱CLI/SHA256・GUI種別・Mac署名/ZIP実行権を確認する。
- [ ] 色あり/NO_COLORのPTYで、2接続→切替→追加→1件の失敗→個別停止→保存/pgAdmin/URI→再試行→全終了を確認する。60×16への縮小/復帰と他端末の接続維持も観測する。Terminal/Ghostty実アプリ操作やWindows実GUIと混同しない。
- [ ] 有効な同一候補の証拠を再利用し、変更・失敗・未解決の懸念がある対象だけ再検証する。候補が変われば同じReviewerへ差分と影響周辺を返す。
- [ ] 元の作業場所へ反映するファイルの基準hashを照合してから、最終候補のソースとローカル配布物を反映する。既存の利用者設定・稼働Proxy・シェル設定は変更しない。新しいCLIの実ユーザー領域への導入は今回の計画に含めない。
- [ ] 最終報告は変更、検証、OS実機未確認、成果物、Git状態を日本語で簡潔に示す。承認された場合だけfeatureをpushしmain向けPRを作成・添付し、head/base/SHA/状態を確認する。merge・新リリース公開は行わない。

## 事前照合と実行方法の確認

現コードのLoadProfilesは書込ロックを取得しないため、ポート予約中の読込と排他書込を分離できる。Connectの既存Wait/stopChild/記録削除を囲む予約解放で、所有プロセスの終了契約を変更せず実装できる。
現在のconnectUIはcurrent/progress/cancel/generationを1件だけ保持するため、そのまま複数goroutineを追加する方式は採用しない。接続別状態と操作対象の識別を先に固定する。
これらのpath/型/契約の照合はREADY。既存v0.2.3のテスト成功は変更前の証拠として保持し、実装開始時にfocused基準を確認する。
本書の実行承認前に製品コードを編集しない。計画の承認時に、このチャットの親による直列実装か、内部実装担当1名への一括委譲かと、push/PRの範囲を確認する。
