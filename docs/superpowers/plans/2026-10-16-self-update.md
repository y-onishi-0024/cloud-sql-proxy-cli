# csql内更新の実装計画

> エージェント向け必須サブスキル: このチャットの担当が`superpowers:executing-plans`で各段階を直列実装する方式を推奨する。独立した内部担当へ実装を分ける方式を利用者が選んだ場合だけ`superpowers:subagent-driven-development`を使う。各段階はチェックボックスで進捗を残す。

**目標:** csqlの画面または`csql update`から、公式CLIを検証して管理版を更新できるようにする。

**構成:** 更新候補の取得・準備・配置を共通化し、UIだけが検証後に自画面の接続を停止して新版の一覧へ引き継ぐ。管理領域と既存receiptを検査し、setupと同じロックで配置を排他する。配布用の通常CLIとinstallerの整合を検証する。

**技術:** Go 1.26.9、既存Cobra/Bubble Tea、net/http・debug/macho/elf/pe・os/exec、既存golang.org/x/sys、Python標準ライブラリ。新規依存を追加しない。

**設計:** `docs/superpowers/specs/2026-10-16-self-update-design.md`（利用者承認済み）。基準main/v0.6.0 `22da84ca766f5cceb302343de103f13e9050628c`、設計候補`a9c68b5a7fc97e5b3922e1933925f8a895677dac`。作業ブランチ`feat/self-update`。

## 全体の制約

- 日本語の文書・表示・適切なソースコメントを使う。保存形式・Proxy所有権・status/stop署名・ポート排他を変更しない。
- 更新対象は管理領域のcsql本体だけ。profiles・認証・gcloud/Proxy・シェル設定・ユーザーPATHは変更しない。
- ユーザー確認後、取得と検証を完了してから自画面の接続だけを停止し、配置・再起動する。外部所有の接続は停止しない。
- 起動時の最大2秒/24時間キャッシュ/通知無効化を保持。明示updateはキャッシュを迂回し、非対話・自動適用フラグ・任意URL/版・ダウングレードを許可しない。
- 固定した公開repositoryの正式latestだけを使用。API10秒/256KiB、取得5分/256MiB、assetサイズ厳密一致とSHA-256必須。別ホストや認証への信頼範囲を広げない。
- raw CLI名は`csql-<版>-<OS>-<CPU>`、Windowsだけ`.exe`。版はvなし。darwin/arm64・amd64、linux/arm64・amd64、windows/amd64を対象にする。
- v0.7.0を搭載版とする。搭載版への初回移行だけは従来の更新が必要。今回の実装工程にはpush/PR/merge/公開/この端末の実インストール更新を含めない。
- テストは重要な外部挙動・状態遷移・失敗復元を守る最小の構成にする。新しいprivate関数や単純な委譲ごとのテストを追加しない。

## 重点確認

1. 通信中のsetup/別updateで配置が変化しても、古い確認内容で上書きせず、接続停止前に拒否する。
2. Esc/Ctrl+Cや遅い成功結果で、staging/ロックが残ったり、別画面で勝手に停止・更新したりしない。
3. 旧版退避後、新版配置・正式パスの起動確認・receipt確定の各地点で失敗しても、旧版とreceiptの組を戻す。復元失敗を成功と報告しない。
4. 検索0件・選択対象の消失・背景Proxy終了でも共通の更新入口は使え、元の行操作・保存専用の制約を壊さない。
5. 新版へ古い`--profile`等を渡さず、端末を復元して入力を引き渡す。別端末の接続を保ち、Windowsの旧版削除不能を他プロセスの強制終了で解決しない。

## ファイルと主要フロー

| ファイル | 役割 |
| --- | --- |
| `updates.go`（変更） | 固定API/repository定数を共有する。通知の非同期・キャッシュ・表示契約を維持 |
| `selfupdate.go`（新規） | 最新asset選択、管理先検査、取得・検証、準備結果の所有、排他、配置/復元、単独updateコマンドの主要フロー |
| `selfupdate_unix.go` / `selfupdate_windows.go`（新規） | OS固有の旧版退避/配置と、新版UIへの端末引継ぎ。共通の業務判断は持たせない |
| `cli.go` / `selection.go`（変更） | update登録、最小限のOS障害検証用差替え境界、版番号、UI共通操作・状態・終了理由 |
| `selfupdate_test.go`（新規）、既存`updates_test.go`/`connections_test.go`/`selection_test.go`/`sessions_test.go`（変更） | 取得/配置の外部契約、キー配送、実プロセス/PTY fixtureをまとめて保護 |
| `scripts/package-release.py`（新規）、`README.md`（変更） | ローカル配布物の検査/版別ZIP・raw CLI・SHA256準備と日本語操作説明。GitHubへは書かない |

大きな既存ファイルの分割や新しいservice層は追加しない。ネットワークから配置までselfupdate.goで追え、OS専用処理だけbuild tagで分ける。

### 共通インターフェース

- `selfUpdateOffer {Tag, Name, URL, SHA256 string; Size int64}`: 検証済み正式Release/asset。URLは固定repositoryから構築する。
- `selfUpdateTarget {Path string; Binary, Receipt pathFile}`: 管理先と確認時snapshot。`(*App).inspectSelfUpdateTarget() (selfUpdateTarget, error)`は書込みをしない。
- `(*App).fetchSelfUpdate(ctx context.Context) (*selfUpdateOffer, error)`: 明示取得専用。最新が同版/旧版ならnil、取得不能/不正なら理由付きerror。
- `selfUpdateProgress {Phase string; Received, Total int64}`: チェック/取得/検証/準備完了を示す値。外部文字列を状態名として描画しない。
- `(*App).prepareSelfUpdate(ctx context.Context, offer selfUpdateOffer, target selfUpdateTarget, progress func(selfUpdateProgress)) (*preparedSelfUpdate, error)`: stagingへ取得し、形式/版/ハッシュ検証済みの結果を返す。
- `preparedSelfUpdate`: Offer/Target/StageDir/Payloadを保持。`Acquire(ctx context.Context) error`で`.path-lock`取得とsnapshot再検査を行い、失敗時はロックを解放する。`Close() error`は未適用stagingと所有ロックの解放を冪等に行う。
- `(*App).applySelfUpdate(ctx context.Context, p *preparedSelfUpdate) error`: 取得済みロック下で再検査し、配置/正式パス起動/receipt更新または復元を行う。成功でもバックアップは保持し、ロックを解放する。
- `(*App).UpdateSelf(ctx context.Context) error`: CLIの確認→準備→Acquire→適用→新版番号表示。UIはこの対話関数を呼ばず、上の共通処理を呼ぶ。
- `restartSelfUpdate(a *App, path string) error`: 引数は新版の絶対パスだけ。通常起動（位置引数/フラグなし）へ端末を引き継ぐ。Unixのexec成功時は戻らず、Windowsは子の終了まで待って戻る。
- `uiSelfUpdateJob {operation uint64; cancel context.CancelFunc; mu sync.Mutex; progress selfUpdateProgress; prepared *preparedSelfUpdate}`: 更新1回の進捗・準備結果を配送前から所有する。UIへ移譲済みかを明示し、同時に2担当がCloseしない。
- `selfUpdateFileOps {Rename func(string,string) error; WriteReceipt func(string,binaryReceipt) error}`と`App.SelfUpdateOps *selfUpdateFileOps`、`App.RestartSelf func(string) error`を副作用境界の差替えに限定して使う。nilなら実OS処理。汎用FS抽象化やproductionのテスト用待機フックを追加しない。

## 段階1: 公式更新候補の取得と準備

対象: updates.go、selfupdate.go、selfupdate_test.go。
成果: 正しい正式版の通常CLIだけを取得・検証し、準備完了/取消を返せる。

- [x] `TestSelfUpdateReleaseContract`をtableで追加する。正式新版のasset選択、同版/旧版の無変更、draft/prerelease/不正tag、欠落/重複asset、digest/サイズ不正、未対応OSを確認する。`CSQL_NO_UPDATE_CHECK=1`でも明示取得は可能とし、既存通知cacheへ依存しない。
- [x] 上記testを実行して、機能未実装による期待するREDを確認する。既存`TestStartupUpdate*`の成功を基準証拠として残す。コンパイル用の型だけが必要なら足場を置き、意味のある振る舞いの失敗まで確認する。
- [x] `fetchSelfUpdate`を実装する。既存parseReleaseVersion/newerReleaseを利用し、canonicalなv付きtagと完全一致assetを検証する。固定APIはredirect禁止、Cookie/authなし。API bodyは256KiB+1を読み上限超過を拒否する。明示取得失敗は通知処理のように黙殺しない。
- [x] `TestSelfUpdatePreparation`に偽RoundTripper/配布物で、成功、Content-Length不整合/途中切断、容量超過、ハッシュ不一致、取消、HTTP、未知ホストへのredirect、間違ったOS/CPU/GUI/版をまとめる。不正ハッシュ・形式のファイルを起動しないことを観測する。
- [x] `inspectSelfUpdateTarget`と`prepareSelfUpdate`を実装する。実行中パス・managed bin・binary-install.json・経路のリンク拒否を既存pathRead/pathNoLinksの契約で検査し、専用一時dirを0700で作る。現行Google専用downloadは変更しない。
- [x] 取得URLは`https://github.com/y-onishi-0024/cloud-sql-proxy-cli/releases/download/<Tag>/<Name>`、redirect上限3回、https/通常443・userinfoなし、hostはgithub.com/release-assets.githubusercontent.comだけを許可する。metadataの任意URLは使わず、公開配布に不要なCookie/Authorizationを転送しない。署名付きredirectのqueryや完全URLをエラーへ出さない。
- [x] 受信中にAPIサイズ超過を検出し、終了時にサイズとSHA-256を照合する。形式確認は標準debugパッケージで行い、その後だけversionを実行する。version検査は5秒+WaitDelay200ms、出力上限16KiB、stdinなし、正確な`csql <期待版>`を要求する。接続/gcloud Runnerとは分け、実行引数へ外部文字列を埋め込まない。
- [x] 準備/取得の失敗・取消では作成したstagingだけを削除する。通知用API/キャッシュは既存の挙動を保つ。focused `go test -race ./... -run 'TestSelfUpdateReleaseContract|TestSelfUpdatePreparation|TestStartupUpdate' -count=1`がPASSしたら関連変更を1コミットにまとめる。

## 段階2: 配置・復元・単独コマンド

対象: selfupdate.go、selfupdate_unix.go、selfupdate_windows.go、cli.go、selfupdate_test.go、既存pathsetup_test.go。
入力: 段階1のOffer/Target/準備済みpayload。成果: 管理版だけを確認付きで更新し、失敗時には旧版へ戻せる。

- [x] `TestSelfUpdateApplyContract`に一時管理領域を用意する。成功時の新版/receipt一致・バックアップの旧版一致・profiles/PATH/ツール無変更、管理外/receipt不一致/リンク、`.path-lock`競合、準備中の配置変更を確認する。競合時は既存ファイルと接続を変えない。
- [x] 同testの障害ケースは、旧版退避、新版rename、正式パスversion、receipt書込みをそれぞれ失敗させる。旧版/receiptを戻し、復元自体の失敗では残存先と復旧手順を表示することを検証する。Windows共有違反はrename境界で再現し、成功後に旧版を削除できなくても失敗やプロセス強制終了にしない。
- [x] 期待するREDを確認後、Acquire/applySelfUpdate/Closeを実装する。Acquireはsetupと同じmkdir `.path-lock`を用い、既存ロックを消さず即時エラー。最終適用前にpayloadのハッシュ・経路とBinary/Receipt snapshotを再検査する。UIが停止を始める前にAcquireを済ませる。
- [x] バックアップはbin直下の専用dirに置き、`update-state.json`へSchema=1、管理先、旧/新版hash、版、専用dir basename、適用phaseを記録する。受け取った任意パスで削除/配置しない。旧版・receiptの復元用snapshotを確保してから変更する。
- [x] Unixは旧版をバックアップして新版を同一filesystem上でrename、Windowsは旧版をバックアップへrenameして新版を正式名へrenameする。ファイルハンドルを閉じてからrenameし、receipt確定前のエラーは取消ctxと独立した短い復元処理で旧版/receiptを復元する。成功/復元後はロックを解放する。
- [x] 復旧に必要な旧版を最後まで保持する。以前のバックアップ整理は記録・basename・リンクなし・ハッシュ一致が揃う自作dirだけを対象とし、未確定/使用中/確認不能なdirを消さない。通常のversion/helpには復旧書込みを追加しない。管理先検査で次の更新に状態不整合を見つけても黙って上書きせず、具体的な旧版絶対パスと公式setup復旧コマンドを表示する。電源断の完全原子性を保証しない。
- [x] `TestSelfUpdateCLI`は実際の`a.Run(..., []string{"update"})`から、確認拒否/成功/最新版/通信失敗/非対話/追加引数拒否を通す。非対話とhelpはHTTPや配置を開始しない。確認文に現在版→新版・公式配布元・絶対配置先、終了結果に版/復旧案内があることを確かめる。
- [x] UpdateSelfとCobraの引数0のupdateコマンドを実装する。確認は既存a.confirmを利用し既定N。単独updateは再起動や既存Proxyの停止を行わず、新版番号を表示して終了する。
- [x] OS別restartSelfUpdateを実装する。Unixは復元済み端末の既存FDを維持してexec、Windowsは新しいconsole/process groupを作らずstdin/stdout/stderrを渡してRun/Waitする。古い親は入力を読まない。Windowsの子は自分で終了処理を行い、親ctx取消でCommandContext.Killを発生させない。子の失敗/終了を成功とすり替えない。
- [x] focused `go test -race ./... -run 'TestSelfUpdate|TestSetupPath' -count=1`をPASSさせる。Mac一時領域の実バイナリでversion置換を確認し、実インストールは触らない。段階1との整合を確認して関連変更をコミットする。

## 段階3: 画面内更新と端末引継ぎ

対象: selection.go、connections_test.go、selection_test.go、sessions_test.go、selfupdate_test.go、README.md。
入力: 段階1/2の共通処理。成果: 検証完了後だけ自画面の接続を止め、新版の一覧へ戻れる。

- [x] `TestConnectionsSelfUpdate`を既存connectionsHarnessの実キーで追加する。Ctrl+K→更新検索→選択→確認/取消、検索0件・接続選択行消失・保存済み設定/検索エラーで共通入口を確認し、profile addには更新/接続が出ないことを守る。
- [x] 同testで更新確認/ダウンロード中の背景進捗、失敗/取消で接続維持、二重開始拒否、setup競合と配置変更で停止前拒否を検証する。staleな結果から接続停止/画面上書きしないこと、Esc後に次の更新を開始できることを確認する。
- [x] `TestConnectionsSelfUpdateShutdown`で2本の自画面接続と別所有fixtureを起動し、検証終了/Acquire前には停止せず、その後は自画面だけ停止することを観測する。正常な更新停止とCtrl+Cで終了理由が混ざらないことも確認する。コピー等のworkerも終了を待ち、準備結果がCtrl+Cと競合してもstaging/ロックを解放する。
- [x] RED確認後、共通操作ID`self-update`を追加する。captureActionsの行なしを許容するcontextを作り、行操作は対象がなければ無効のままにする。self-updateだけは行のowner/attempt/generation検査へ結び付けず、UIのexit/addOnly/処理中制約を先に検査する。既存の行操作の対象固定を緩めない。
- [x] UI状態は`updateChecking`（最新取得）、`updateConfirm`（既定取消）、`updatePreparing`、`updateCancelling`、既存`stopping`を利用する。元のstate/listLocationを専用値へ捕捉し、Esc/準備失敗時に検索/選択を戻す。F1・resize・背景イベントを受け付け、フォーム/保存/出力/Port処理から開始できないようにする。
- [x] `uiSelfUpdateResult {operation uint64; offer *selfUpdateOffer; prepared *preparedSelfUpdate; err error}`と専用operation/cancelを使う。準備進捗はmutex付きsnapshotへ書き、UI tickで読む。接続管理workerの進捗から推測しない。
- [x] 更新workerは結果配送前に準備結果をjob所有として記録する。Escは取消完了を待ってから入口へ戻し、終了時はworker待機後に未移譲結果をCloseする。結果が未配送でもロック/stagingを解放できるようにする。遅い結果は専用operation一致を確認し、不採用の準備結果を冪等Closeする。
- [x] 取得/形式/version検証後、同じworkerでAcquireと再検査を終えてからreadyを配送する。採用したpreparedの所有を`m.pendingUpdate`へ移し、更新専用終了理由`self-update`でbeginShutdownを通す。通常取消は`cancelled`として区別し、Ctrl+Cが優先した場合は適用せずpreparedをCloseする。
- [x] runConnectModelはworker待機・代替画面復元後、親ctx取消/通常終了を検査する。self-updateの場合だけpreparedを共通applyへ渡す。applyには取消済みのm.ctxを渡さず、ユーザー終了要求を反映する外側のctxを渡す。失敗なら停止済みと復元状態を表示し、旧版/復元失敗時の復旧方法を示す。旧版一覧を開くか終了の選択は既定終了。UIを再開する場合は新規モデル/新規ctxで起動し、閉じたwork gateを再利用しない。
- [x] 成功時はロック/一時FDを解放してからrestartSelfUpdateへ管理先だけを渡す。元のos.Argsを再利用しない。新版の起動に失敗した場合は「更新は完了、再起動に失敗」と絶対パスからの起動方法を表示し、配置成功まで巻き戻さない。
- [x] 既存runUIFixture/TestMainにテスト専用の公式API/CLI/version/restart境界を追加する。`TestSelfUpdateRestartContract`で古い--profile/位置引数を渡さないこと、古いモデルが入力を読まないこと、親から子を強制killせず終了状態を返すことを検証する。productionに環境変数で配布先/管理先を差し替える機能は追加しない。
- [x] READMEに画面とupdateコマンド、接続停止/再接続、確認・失敗時復旧、raw CLIと初回移行を記載する。既存F1/ヘッダーのURL案内を保ち、Ctrl+K→更新を添える。60x16・NO_COLORでも版/停止/取消/終了を読めるようにする。
- [x] focused `go test -race ./... -run 'TestSelfUpdate|TestConnections|TestConnectUI|TestStartupUpdate|TestRich' -count=1`をPASSさせる。既存保存/Port/出力とhelpの回帰も確認し、関連変更をコミットする。

## 段階4: 配布の準備・独立レビュー・最終検証

対象: scripts/package-release.py、cli.go/updates_test.goの版、README、除外済み`.superpowers/sdd/2026-10-16-self-update/`。
成果: 同一候補の検証証拠とv0.7.0配布物。公開・実端末更新はしない。

- [ ] cli.goを0.7.0、同版fixtureを0.7.0、必要な新版fixtureをそれより新しい版に揃える。無関係な過去版のfixtureは変えない。
- [ ] scripts/package-release.pyは引数`--go`（任意）だけを受け、cli.goから版を読み、既存build-installers.pyとlinux2環境のCLIビルドを実行する。Goは1.26.9固定、CGO_ENABLED=0のCLI、`-trimpath -buildvcs=false`、installer GUIだけ既存CGO条件を使う。
- [ ] 版別`dist/releases/v<版>/`が既存なら上書きせず停止し、symlink/不正版を拒否する。ZIP3種/raw CLI5種/日本語README/ライセンス/全8バイナリ配布物のSHA256を用意する。公開対象一覧とhash/size/候補SHAは同dirの`release-assets.json`へ保存する（自己参照hashを含めない）。ローカルの公開前履歴や秘密・設定を含めない。
- [ ] ZIP名は既存の`csql-setup-<版>-macos-<CPU>.zip`/`csql-setup-<版>-windows-amd64.zip`を維持する。通常CLIは設計の版付き名でコピーし、manifest版/OS/CPU/hashとinstaller内部のpayload・Mac署名・ZIP内mode/path・LICENSE/案内の一致を検査する。過去の版別releaseは保持する。
- [ ] `python3 scripts/package-release.py --help`で書込みがないことを確認する。本ビルドと成果物照合で配布契約を検証し、単純なコピー処理だけのunit testを追加しない。
- [ ] 候補をcommitしclean/SHAを固定して、履歴なしの独立Reviewer1名へ設計・計画・base/candidate・focused証拠を渡す。全差分と重点5項目・復元/信頼/所有権/入力を最後まで監査する。修正は親が行い、同じReviewerで再確認する。別のユーザーTaskは作成しない。
- [ ] 同候補で`go test -race ./... -count=1 -timeout=120s`、`go test -race -tags installer ./... -run 'TestInstaller|TestSetupPathCLI' -count=1`、通常/installer両vet、gofmt、git diff --checkを実行する。
- [ ] 同候補から配布スクリプトを実行し、全5CLI/3installerの版・形式・embedded payload一致と公開予定の全asset hash/sizeを検証する。package-release.pyのasset一覧はraw CLI5/ZIP3/README/SHA256SUMSの10件とし、メタデータ自身や単体ライセンスは含めず、ライセンスは各ZIPに同梱する。版別出力作成前に他の全gateを完了し、失敗時は不完全と確認できた今回の一時出力だけを補正する。
- [ ] 既存terminal-dashboardのPTY検証を参考に今回用の手順を除外済み作業dirへ用意する。auto/light/NO_COLORで複数の偽接続→共通操作→取消→取得失敗で接続維持→成功して自画面接続だけ停止→端末復元/新版一覧、CLI updateの確認/成功、Mac一時領域で実バイナリ置換/execを確認する。4形式出力/Port/検索消去も既存手順で保護する。
- [ ] Windowsのrename/rollback/親子入出力と未対応OSは境界テストとcross-buildで確認し、Windows実機がない限り実機済みとしない。実GCP/DB、DBツールGUI、ユーザー実clipboard/設定/稼働中Proxyを使わない。
- [ ] 候補SHA/plan/scope/argv/Go版・cache/env/各gate/result/未確認範囲をprivate台帳へ記録し、レビュー/配布物と候補一致を確認する。新しい変更・失敗・未解決事項がなければ成功済みgateを繰り返さない。最後は変更点・検証・残件を日本語で報告する。

## 実行方法と停止条件

Go実体は`.tools/go/bin/go`、`GOTOOLCHAIN=local`、`GOCACHE=$PWD/.cache/build`、`GOMODCACHE=$PWD/.cache/mod`。上記のgoはこの実体を指す。loopback/子process/PTY/Git管理情報は必要に応じて環境の承認機構を使う。
作業場所は利用者指定の既存checkout、ブランチfeat/self-update。新しいworktreeやユーザーTaskを増やさない。実装は現在担当が直列、独立性の必要な最終reviewだけ内部委譲する。候補凍結後の文書だけの更新はprivate台帳を利用する。
設計の状態遷移・更新対象・信頼境界・外部副作用を変える必要が出たら実装を止め、差異を親へ戻す。実装不具合は同じ担当が修正・再検証する。plan承認後は実装・独立レビュー・補正・最終検証まで継続し、同じ範囲の途中承認を求めない。
