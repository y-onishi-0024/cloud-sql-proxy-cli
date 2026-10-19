# DBツールへの接続設定直接登録の実装計画案

> 実装担当への指示: 利用者による設計・計画・実行範囲の承認後に `superpowers:executing-plans` を使い、このチャットで直列実装する構成を推奨する。チェック項目を追跡し、状態/外部契約の設計変更は親へ戻す。現在は計画作成のみの依頼であり、実装は未承認。

**目標:** 保存済み接続設定を、パスワードやDB接続の開始を伴わずpgAdmin・DBeaver・A5へ追加する。

**構成:** 共通の検出・プレビュー・実行・実保存先照合をCLI/TUIから呼ぶ。ツール固有のコマンド・設定読取りは少数のファイルにまとめ、外部要求送信後の結果不明を成功/取消と区別する。

**技術:** Go 1.26.9、Cobra、Bubble Tea、標準API、既存golang.org/x/sys。pgAdminは同梱Pythonを使用する。新しいGo依存やツールの自動インストールは予定しない。

**設計案:** [直接登録の設計](../specs/2026-10-19-db-tool-registration-design.md)。未承認の提案である。

## 正本・範囲・実行方式

- 基準mainは`25c63fbf474f022e28269c4057fabea7c4e20bfc`。文書ブランチは`docs/db-tool-registration-plan`。実装開始時にmainとの差異とdirty状態を照合し、既存変更を上書きしない。
- ユーザーが計画作成を依頼したため、設計案と実行計画案を併せて用意した。実装前には両文書と範囲をまとめてレビューする。
- 提案する実行範囲は、段階0の互換性確認 → 共通処理/3アダプター → CLI/TUI → 独立レビュー/最終検証。Release公開、mainマージ、端末更新、利用者の実設定への登録、パスワード等の抽出/復号は含めない。版番号更新と配布物更新も別依頼まで行わない。
- UI・履歴・外部書込みの状態が密に連動するため親が実装担当を兼ねて直列に所有する。別のユーザー所有Taskは作成しない。独立Reviewer1名に全差分を渡し、実装修正後は同じReviewerへ再監査を依頼する。
- 担当選定: 計画は現在の親と既存文脈を継続する。新規担当へ移すと現行TUI・保存・エラー契約の説明と再調査が重なる。実装/監査の推奨はSol high相当で、利用可能モデルとtool契約は担当起動前に確認する。モデルを切替済みとは扱わない。

## 共通制約

- 対象ツールを利用者が閉じてから追加する。起動中への転送は初期対象外。DBeaverの登録操作で起動したGUIは強制終了しない。
- Mac/WindowsのpgAdmin・DBeaver Community、Windows amd64通常モードのA5を候補とし、段階0で検証済み版と保存形式を固定する。未検証の版/構成を自動で対応扱いにしない。
- 既存のファイル出力・URI・設定保存形式・Google認証・PATH・インストーラー・自己更新・セッション制御を変更しない。
- `--replace`、`/SetDB`、削除、DBeaverの`-stop`/`-disconnectAll`/`-reuseWorkspace`、DBパスワード、shell経由の動的実行を使用しない。
- 全工程は日本語のUI/資料と適切な日本語コメント。設定・パス・外部出力をsafeLog等で安全に表示し、バックアップ内容や資格情報をログへ出さない。
- dry-run/検出は外部プロセスと書込みなし。pgAdminの重複/スキーマ検査は「確認後に検査」と表示して延期し、dry-runで重複なしと断定しない。確認は既定取消・実登録はTTY。確認後の再検査/バックアップ/pending記録に失敗した場合は外部書込みを始めない。
- 外部起動前まで取消可能、起動後は結果未確認を許す。自動再試行/自動復元をしない。成功には実登録先の読取り照合を必須にする。
- 履歴は最大1MiB、対象のJSON読取りはファイルごと最大16MiB・最大128ファイル、外部stdout/stderrは各64KiB、コマンド/照合は合計120秒を上限案とする。超過は無変更または結果未確認として明示する。
- 一時領域/バックアップ/履歴は通常ファイル・正規化した許可対象だけを扱い、OS別の私有権限で保存する。実行ファイルの正規リンクと、書込み対象の不意なリンクを混同しない。

## レビューの重点

1. 別workspace・別pgAdminユーザー・別A5保存モードへ登録しない（段階0/1）。
2. 空白/日本語/引用符/区切り文字やWindowsの引数解釈で、別接続プロパティ・別コマンドを増やさない（段階1）。
3. 同名異内容、Port変更、確認中の設定変更、履歴紛失後の再実行で、既存設定を上書き/重複追加しない（段階1/2）。
4. 終了コード0の失敗、要求後のタイムアウト/取消、GUI継続、履歴保存失敗を成功/未登録/取消と取り違えない（段階1/2）。
5. worker結果未配送と画面終了が重なっても、既に発生した外部追加を失わず、別所有Proxyを終了しない（段階2/3）。

## ファイルと境界

| ファイル | 役割 |
| --- | --- |
| `dbtools.go`（新規） | 検出/入力・プレビュー・排他/履歴・コマンド作成・結果照合・CLI。3ツールの主要フローをここから追えるようにする |
| `dbtools_windows.go` / `dbtools_unix.go`（新規） | Windowsの通常モードA5設定読取り・退避とOS固有のプロセス検査。非Windowsの非対応処理。DBツールを終了する権限は持たせない |
| `dbtools_test.go`（新規） | 公開されたCLI/共通処理から、引数・外部設定・履歴・結果を観測する回帰テスト |
| `profiles.go` | profile register登録、A5の登録名/接続文字列生成を既存出力と共有する小さな抽出。従来の出力バイト列は維持 |
| `selection.go` | 行操作、ツール/対象選択、確認、worker所有、結果保持、元画面復帰 |
| `connections_test.go` / `selection_test.go` | キー操作・背景更新・取消/未配送・複数接続と実端末fixture |
| `README.md` | 各ツールの前提・既定登録先・CLI例・競合/未確認/バックアップ案内 |

無関係なファイル分割や新規service層は作らない。OSファイルはビルドタグとWindows APIの分離に必要なため設ける。計画時点で新規依存なし。

主要境界の名前/型は次の契約で揃える。段階0の結果で変更が必要なら実装前に設計案と合わせて修正する。

- `dbToolOptions {Tool, Executable, PGAdminSQLite, PGAdminUser, DBeaverWorkspace string}`。
- `dbToolTarget {Tool, Version, Executable, Python, SetupScript, Store, User, Project, Identity string}`。Identityは正規化した登録先・OSユーザーを含む。
- `dbToolRegistrationPlan {Profile Profile; Target dbToolTarget; Name, Fingerprint, BeforeDigest, Disposition string}`。Dispositionは`add` / `already-present` / `conflict` / `unsupported` / `needs-inspection`。pgAdminは静的検出だけでは`needs-inspection`とし、確認後のスナップショット/重複検査で確定する。BeforeDigestには必要な設定DB/WAL等の対象集合を含め、ファイル本体だけで変更なしと判定しない。
- `dbToolRegistrationResult {Status, Name, Target, Backup, Detail string; Submitted, Verified bool}`。Statusは`registered` / `already-present` / `conflict` / `cancelled` / `failed` / `unknown`。Submitted後のerrorは結果を消さない。
- `(*App).planDBToolRegistration(ctx context.Context, p Profile, options dbToolOptions) (*dbToolRegistrationPlan, error)` は無起動/無書込みの検出・読取りを担当する。
- `(*App).applyDBToolRegistration(ctx context.Context, plan *dbToolRegistrationPlan) (dbToolRegistrationResult, error)` は確認後の再検査・排他・バックアップ・pending・実行・照合を担当する。CLIとUIのどちらも同じ境界を使う。
- `(*App).profileRegisterCommand() *cobra.Command` をprofileCommandへ登録する。
- `uiDBToolRegistrationJob`はoperation/起点のconnection ID・attempt/プロフィール指紋、cancel、mutex、完了結果、表示済み状態を所有する。外部起動と結果の保持を画面のポインターだけに依存させない。
- 既存`CommandRunner`は終了するpgAdmin処理等に再利用する。GUIの生存時間と登録完了が異なるDBeaver/A5用にはStart・Wait回収と状態照合を別に行う。プロセスをctx取消でkillする既存runnerをそのままGUIに流用しない。テスト差替えはApp上の最小境界に限定する。

## 段階0: 互換性と実登録先の確認（実装前ゲート）

Primary: 設計/計画と私有の検証記録。Allowed: 破棄可能な設定領域の手順。Forbidden: 利用者の本来のDBツール設定、DBログイン、Google認証、PATH、コード実装。

- [ ] 対象OSごとにツール名・正確な版・実行ファイル/Python・設定形式・検証環境を表に記録する。Mac/WindowsのpgAdminとDBeaver、Windows通常モードのA5を別行で扱う。
- [ ] pgAdminは専用Pythonとsetup.pyの対、sqlite/userの指定、同じschemaの読み取り専用スナップショット、dump→load→dump、同名追加と終了コード0失敗を、一時DBだけで確認する。ユーザーDBへ暗黙の初期化/マイグレーションをしない判定方法を固定する。静的dry-runと、確認後のPythonによる検査を分ける。
- [ ] DBeaverは一時workspace/Generalと初期データで、ドライバーID、`-data`の対象、保存フラグ、永続化時点、GUIが残るときの親子プロセス、複数data-sourcesファイルの読取りを確認する。新規設定がDB接続を開始しないことを観測する。Host/Port/DB/User/driverをパスワード復号なしで比較できるか確認し、できない版はREADYにしない。
- [ ] A5はWindowsの破棄可能なOSユーザー等で、通常モードの保存先、/AddDBの同名拒否、/Exit、引数の日本語、設定読取りによる成功判定を確認する。ポータブル/Store構成の判別も確認する。
- [ ] 各ツールが閉じていることをOSごとに検査できる条件と、起動プロセスを誤ってkillしない条件を固定する。
- [ ] `READY`は対象版・保存先・重複判定・結果照合・既存保持が確認できた組合せだけ。別方式への変更は`NEEDS_SPEC_FIX`として設計に戻す。実行環境不足は`BLOCKED`として明示し、Windowsビルド成功で代替しない。

この段階の未確認事項を実装者の推測へ渡さない。互換性の確認だけで成立する数値/パスの確定は同じ案へ記録し、対応OS/対象版・登録方式・取消契約・scopeの変更は利用者レビューに戻す。READYにならない組合せを黙って除外して「3ツール対応完了」としない。

## 段階1: 検出・重複防止・登録処理とCLI

Primary: dbtools.go / dbtools_windows.go / dbtools_unix.go / dbtools_test.go / profiles.go。Forbidden: 自己更新・Google認証・session停止・自動インストール。

- [ ] `TestDBToolRegistrationPlan`で、対応OS/DB/検証版、標準/明示/複数配置、未初期化、既登録/競合、許容されない区切りをCLI dry-runから観測する。pgAdminは重複/スキーマ検査未実施と表示し、確認後の検査で既登録/競合を判定する。dry-run前後のファイル一覧・バイト列・外部起動なしを確認する。
- [ ] `TestDBToolRegistrationApply`で初回追加、同一再登録、Port変更、確認後の別プロセス変更、backup/pending失敗、結果照合の失敗を再現する。実際に書き換えるfixtureを使い、mockの呼出し回数だけで完了にしない。
- [ ] `TestDBToolRegistrationOutcome`で「終了コード0だが定義なし」「外部追加済みでタイムアウト」「履歴保存失敗」「pending後のcsql終了」を再現し、成功誤判定・自動再試行・重複を防ぐ。
- [ ] A5出力の生成を小さく抽出し、CLI引数と既存出力が同じ接続文字列を使うようにする。profiles_test.goの既存A5/CSV/JSON/URIテストを更新・再利用する。
- [ ] レジストリ/SQLite/JSONの読取りは必要項目に限定し、バックアップは原本保持・一意パス・私有権限を検査する。外部ファイルの任意キーを実行引数へ転送しない。
- [ ] `profile register`、各フラグ、TTY既定取消、read-only dry-run、非対応/競合/未確認時の日本語手順を実装する。正常追加/既登録は終了0、拒否/未確認は非0と結果を表示する。
- [ ] Focused検証: `.tools/go/bin/go test ./... -run 'TestDBToolRegistration|TestProfile' -count=1`。現実的な回帰のテストだけを先に失敗させて実装し、helper単位の網羅テストは作らない。

## 段階2: 一覧からの直接登録と結果保持

Primary: selection.go / connections_test.go / selection_test.go。Allowed: dbtoolsの境界修正、README。Forbidden: 接続先の自動停止、既存エクスポートのキー/手順変更。

- [ ] Ctrl+Kの「ツールへ直接登録」を保存済み設定と接続一覧に追加する。未保存・停止中・Port未確定では対象に応じた案内とし、選択行なしで別行へ作用しない。
- [ ] ツール選択、検出先/登録先の確認・修正、内容確認、登録/結果表示を共通境界へ配線する。対応外ツールは理由を示し、既存ファイル出力へ移れるようにする。
- [ ] `TestConnectionsDBToolRegistration`で保存済み未接続/接続中からの入口、既定取消、既登録、競合、元のtab/query/cursor/connection ID、選択後の対象変更拒否を観測する。
- [ ] `TestConnectionsDBToolRegistrationDelivery`で、背景の停止/再試行・外部確定直後のEsc/Ctrl+C・worker未配送・履歴保存失敗・再起動後のpendingを検証する。外部追加結果は配送前から所有し、画面終了後にも要確認とバックアップを出す。
- [ ] 検索/一覧は維持し、登録処理のためにProxyを止めない。Ctrl+Cだけは既存の自画面全停止を維持する。別端末所有のfixture接続を含めて確認する。
- [ ] Focused検証: `.tools/go/bin/go test -race ./... -run 'TestConnectionsDBToolRegistration|TestConnectionsWorkspace|TestConnectionsSelfUpdate' -count=1 -timeout=120s`。操作案内と結果画面を60×16/100×30、auto/light/NO_COLORで確認する。

## 段階3: 独立レビューと最終検証

Primary: 変更全差分と私有の検証記録。Allowed: 同一実装担当による指摘修正。Forbidden: 未承認の公開・mainマージ・利用者実設定への登録。

- [ ] 候補をコミットし、base/candidate SHA・絶対作業パス・clean状態・変更範囲・段階0の互換性表・focused結果を独立Reviewerへ渡す。全差分の契約/正しさ/security/実害あるテスト不足を監査する。
- [ ] BLOCKER/HIGHを解消し、同じReviewerで最新候補を再監査する。MEDIUMも内容と実害を報告し、残す場合は理由を明示する。設計/範囲の指摘は勝手に実装で補正しない。
- [ ] 最終候補で`.tools/go/bin/go test -race ./... -count=1 -timeout=180s`、`.tools/go/bin/go vet ./...`、変更Goファイルのgofmt、`git diff --check`を実行する。
- [ ] Mac arm64/amd64・Windows amd64の通常CLIビルドと、installerタグのビルド/既存テストを確認する。installerへGUI登録コマンドが混入しないことを確認する。
- [ ] PTYで登録/取消/結果未確認/一時ファイル削除/一覧復帰を確認する。F2更新、4形式ファイル出力、Port変更、PC間移行は既存テストを再利用し、変更影響のある経路だけ実端末fixtureでも確認する。
- [ ] 段階0の実ツール版で一時設定への初回追加・二回目の無変更・同名異内容の拒否・永続化・既存定義保持・新規DB接続なしを再確認する。起動中の場合の拒否もOS別に確認する。実機不足は残件にし、対応完了の条件を満たしたと報告しない。
- [ ] レビュー済みSHAとgate対象SHAを一致させ、ソース・自動テスト・Mac実ツール・Windows実機・未実施範囲を分けて報告する。候補とツールチェーン/検証範囲が不変の証拠は再利用し、返信を跨いだだけで全体テストを繰り返さない。

## 現時点の状況

設計・計画案の作成前にmainがcleanであることを確認した。コード変更・実ツール起動・実登録・テスト実行はまだ行っていないため、機能のbaseline PASSや実機対応は主張しない。実装を始めるには、両文書のレビュー、初期版の閉じたツールへの登録という範囲、直列実装/独立レビュー/実機確認範囲の合意が必要である。
