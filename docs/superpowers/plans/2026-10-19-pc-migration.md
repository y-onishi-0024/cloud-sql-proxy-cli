# PC間の接続設定移行の実装計画

> 実装担当への指示: 実行方式の選択・承認後に、直列実装は `superpowers:executing-plans`、サブエージェント方式は `superpowers:subagent-driven-development` を使用する。段階ごとのチェック項目を追跡する。

**目標:** CLIと接続画面から、保存済み接続設定を1つの移行ファイルで安全に受け渡せるようにする。

**構成:** `migration.go`に形式検査・出力・プレビュー・取込確定を集約し、既存のプロファイル排他と原子的保存を使う。UIとCLIは同じ検証・競合判定・保存結果を利用する。UIは結果配送前から処理結果を保持し、取消と設定確定を区別する。

**技術:** Go 1.26.9、Cobra、Bubble Tea、標準JSON/ファイルAPI。新規依存なし。

**設計:** `docs/superpowers/specs/2026-10-19-pc-migration-design.md`（利用者承認済み）。

## 正本と共通制約

- 基準main/v0.7.0は `f2181c140696fe0936ffc9da6ef426406d76f014`。設計commitは `ece12e05ba7e992bed5e83f55d78c590b1d49a6e`。作業ブランチは `feat/pc-migration`。
- v0.8.0として実装・独立レビュー・最終検証・ローカル配布物作成までを実行範囲とする。push/PR/merge/Release公開・実端末更新・利用者の実設定移行は含めない。
- 保存済みプロファイル全項目だけを移す。認証・パスワード・セッション秘密情報・ログ・キャッシュ・PATH・ツール/導入記録・DBツールの登録を含めない。
- 移行JSON最大2MiB、`format="csql-migration"`、`version=1`、UTC RFC3339の`createdAt`、正式3数値版の`csqlVersion`、`profiles`配列。保存後は既存Version 1・最大1MiB。
- 未知項目/版・重複キー・不正UTF-8・末尾JSONを拒否し、先頭UTF-8 BOM1個と改行差だけ許容する。プロファイルは現行項目名/入れ子・検査規則を維持する。
- 全項目同一はスキップ。同名異内容・異なる接続先の同Port・同じ接続先の異なるPortは競合として全体拒否。勝手な改名/Port変更・部分適用・全置換なし。
- 通常ファイル/実親ディレクトリのみ。出力は既存上書き禁止、Unix0600。入力JSONの値をパス・コマンドとして解釈しない。
- 出力とdry-runは非対話可、実取込はTTY・既定取消。確認中はロックしない。確認後にprofiles.lock下で元設定を再検査し、原本バックアップ→全体保存の順に確定する。
- 取込自体は接続を開始・停止しない。Ctrl+Cによる画面全体の終了は既存どおり自画面接続を停止する。実際のPort占有検査は接続時に行う。
- 日本語の画面・資料と適切な日本語コメントを維持する。私有helperごとのファイルや無関係なリファクタリングを増やさない。

## レビューの重点

1. 確認画面を開いた後の別プロセス保存・削除・新規作成を検出し、既存の更新を上書きしない（段階2）。
2. 大文字小文字違いのキー別名、深いJSON、null、BOM重複、DB名/パスの日本語や引用符で形式検査と端末表示を迂回しない（段階1/3）。
3. バックアップ後の保存失敗・待機中の取消では元設定を保持し、保存確定直後の取消では成功を失わない（段階2/3）。
4. worker結果未配送のままCtrl+C/外側ctx終了、古い結果の到着、取消後の再試行で、保存結果・ロック・画面の所有が混ざらない（段階3）。
5. 同じ接続先・Portの別名、空/全同一の再取込、容量限界で既存予約・同一判定・無変更の挙動が崩れない（段階1/2）。

## ファイルと境界

| ファイル | 責務 |
| --- | --- |
| `migration.go`（新規） | 移行形式、通常ファイル入力、出力候補、プレビュー、バックアップ/確定、CLIコマンドと共通案内 |
| `profiles.go` | 原本バイト列と同じ読込からプロファイルを得る小さな内部境界の抽出。既存LoadProfilesの制約・エラー案内・保存形式を維持 |
| `cli.go` | migrateコマンド登録、版番号、保存障害だけの差替え境界 |
| `selection.go` | 共通操作・フォーム・確認/取消・worker所有・結果表示・一覧更新 |
| `migration_test.go`（新規） | 実コマンドからの往復/入力拒否/競合/原本保持/バックアップの観測 |
| `connections_test.go` / `selection_test.go` | 実キー、所有接続、未配送/取消/再試行、端末用fixture |
| `README.md` / `scripts/package-release.py` | 操作/移行後準備の日本語案内、配布README |
| 既存版fixtureのtestファイル | 現行版0.8.0と将来更新fixture0.9.0を区別。過去の設計資料や配布物は変更しない |

外部共通境界は次の名前・型で統一する。内部だけの検査helperはこの主要フローを追える範囲に留める。

- `migrationDocument {Format string; Version int; CreatedAt, CSQLVersion string; Profiles []Profile}`（JSONタグは設計の最上位小文字名）。
- `migrationPreview {Source string; Before pathFile; Existing, Incoming, Merged []Profile; Added, Identical []Profile; Conflicts []string}`。Beforeは原本の存在・バイト列・権限を保持し、入力は検証済み内容だけを保持する。
- `migrationResult {Path, Backup string; Added, Identical int; Committed bool}`。Pathは出力先または正式設定先。Committedは設定/出力が確定したことを表す。
- `(*App).prepareMigrationExport() ([]byte, int, error)` / `(*App).defaultMigrationOutput() (string, error)` / `(*App).exportMigration(ctx context.Context, path string, data []byte, count int) (migrationResult, error)`。
- `(*App).previewMigration(ctx context.Context, path string) (*migrationPreview, error)` / `(*App).applyMigration(ctx context.Context, preview *migrationPreview) (migrationResult, error)` / `(*App).migrationCommand() *cobra.Command`。
- `(*App).loadProfilesSnapshot() ([]Profile, pathFile, error)` をprofiles.goへ抽出し、LoadProfilesは同じ読込/検証へ委譲する。初回不存在を空設定と区別する。
- `migrationFileOps {Backup func(string, []byte) (string, error); Save func([]Profile) error}`、`App.MigrationOps *migrationFileOps`。nilは実ファイル操作。障害と確定タイミングだけをtestで制御し、実行時環境変数やCLIから切り替えない。
- UIは `uiMigrationJob`（operation、cancel、mutex、完了したresult/error、結果を表示済みかの情報）と `uiMigrationResult`（operation/job、preview、result/error）をselection.goに置く。jobは結果を配送前から所有し、終了待機後にも確定済み結果を参照可能にする。

## 段階1: 移行ファイルの検査・出力・プレビュー

Primary: migration.go / migration_test.go。Likely: profiles.go。Allowed: cli.goの差替え型。Forbidden: Google認証、session制御、PATH、導入/更新処理。
入力: 既存Profile/LoadProfiles。成果: 正当な移行ファイルと、書込みなしの取込プレビュー。

- [ ] `TestMigrationFormatAndExport`をCLIにつながる共通境界で作り、全項目・日本語・PrivateIP・複数DB/別名を往復し、秘密情報/導入記録を含まず、既存出力を上書きしないことを確認する。有効な契約を通らない状態でREDを確認し、未定義moduleだけを理由にしたREDにしない。
- [ ] 入力はLstat→通常ファイル/親検査→open→Stat/SameFileと上限読込。UTF-8検査・BOM1個除去後、JSONトークンで重複キー/過剰ネスト（上限16）/未知項目を検査し、型付きdecodeを行う。最上位5項目は必須・正確なキー名、profilesはnull不可、メタデータはcreatedAtのUTCとcsqlVersionの3数値版を検証する。Profileのキーは既存JSON表現の正確な項目名だけを許容する。
- [ ] LoadProfilesの検査を壊さず原本と行を同時取得するloadProfilesSnapshotを抽出する。既存の通常ファイル・SameFile・親リンク・1MiB検査とエラー案内を保持する。移行入力の各値と内部予約は同じ規則で検査する。
- [ ] 出力はNowのUTCから `csql-migration-YYYYMMDDTHHMMSSZ.json` を起動ディレクトリへ提案し、競合時は-2〜-1000、超過は明示エラー。任意FILEは絶対パスへ正規化して表示し、既存writeProfileExportの新規作成/close失敗時削除を利用する。出力直前にctxを確認し、close成功後はCommittedを返す。事前確認したJSONの再生成はしない。
- [ ] プレビューは既存と入力を設定名で比較し、全項目一致だけIdenticalへ入れる。競合を全件列挙し、Added/Mergedも保持する。競合を回避するための優先順位や暗黙の値変更を追加しない。結合後のcompact profileStoreの1MiB上限もこの段階で検査する。
- [ ] `TestMigrationInputRejection`で未知形式/版/項目・重複/キー別名・null/必須欠落・UTF-8/BOM・末尾JSON・容量/深さ・制御文字・リンク/特殊ファイルをまとめて拒否する。`TestMigrationPreview`で空/全同一/別名/3競合/入力内矛盾/結合容量を検証し、設定領域への書込みがないことを観測する。
- [ ] `go test -race ./... -run 'TestMigration(FormatAndExport|InputRejection|Preview)|TestProfile' -count=1` がPASS。テストは現実的な契約単位にまとめ、parserのprivate helperごとに追加しない。

## 段階2: 排他・バックアップ・CLI取込

Primary: migration.go / migration_test.go。Likely: cli.go / profiles.go。Allowed: sessions_test.goの既存子プロセスfixture。Forbiddenは段階1と同じ。
入力: migrationPreviewと既存lockProfileStore/writeProfiles。成果: 確認付きCLIと一括保存。

- [ ] `TestMigrationCLI`でexport、import --dry-run、非TTY実取込拒否、既定取消、追加成功、全同一で変更なし、競合拒否、help/引数拒否をa.Runから通す。出力/確認に実絶対パスと件数を含め、取込が外部コマンドを呼ばないことを確認する。観測する契約のRED後に共通処理を実装する。
- [ ] CLIへmigrateを登録する。dry-runと実取込の競合は一覧表示して非0、追加0/競合0は「変更なし」で0。実取込は最初にTTYを検査し、内容/取込先/件数/バックアップdirを表示してa.confirm、同意後にだけapplyMigrationへ進む。確認画面やdry-run中はprofiles.lockを作らない。
- [ ] applyMigrationは競合を拒否し、追加0では無書込みで返す。それ以外はlockProfileStoreを取得し、現在の通常ファイル/存在/原本バイト列を再取得・Beforeと比較する。不一致では再確認を案内する。ロック待機中のctx取消を守り、確認中の別保存を新プレビューで自動承認しない。
- [ ] 元ファイルがある場合、ConfigDirでCreateTempの一意名 `profiles.pre-import-*.json`、Unix0600へ原本を書き、close成功後だけ次へ進む。作成/書込/close失敗では部分バックアップを除去して本体未変更。不存在の初回はバックアップなし。バックアップ完了後の取消/保存失敗では残ったバックアップの絶対パスを結果に保持する。
- [ ] 最終ctx確認→既存writeProfilesによる一括確定。成功した直後にctxが取り消されてもCommitted=trueを返す。CLIは確定後に取消として扱わず、追加件数・バックアップ/初回の区別・doctor/setupとDBツール再出力の案内を表示する。保存失敗は非0で元設定保持と残存バックアップを表示する。
- [ ] `TestMigrationApplySafety`で既存原本の完全一致バックアップと復元読込、初回、バックアップ失敗、保存失敗、ロック待機取消、確認後の別保存/削除/新規作成、保存後取消を観測する。障害境界だけMigrationOpsで差替え、成功時は実ファイルへ保存する。プロファイルの追加と別プロセスの保存が同時進行するケースも既存fixtureで保護する。
- [ ] `go test -race ./... -run 'TestMigration|TestProfile|TestSessionProfile' -count=1` がPASS。fixtureの待機には期限と後始末を付け、実利用者の設定・認証・プロセスは使用しない。

## 段階3: 共通メニュー・確認画面・結果所有

Primary: selection.go / connections_test.go / selection_test.go。Allowed: migration.goの表示整形、README.md。Forbidden: 保存専用画面への接続操作追加、Proxy終了/再起動契約の変更。
入力: 段階1/2の共通処理。成果: 稼働接続を保つ画面内移行と確定結果の正しい報告。

- [ ] `TestConnectionsMigration`でCtrl+Kの行0件/検索一致0件から共通操作、profile addで非表示、パス編集/日本語/引用符/~/~\、確認既定取消、競合一覧、PgUp/PgDn、F1、Escで位置復元、成功後の保存一覧更新を実キーで通す。入口の未実装を契約のREDとして確認してから統合する。
- [ ] availableActionsの行未選択による無効化の後にmigration-export/importを追加する。CLI/DBツールエクスポートと区別するラベル・検索語にし、入力は既存editTextを使う。UIパスは対応する単/二重引用符1組→先頭ホーム展開→Absの順、環境変数やシェル式は評価しない。
- [ ] 状態はmigrationPath（kind=export/import）、migrationPreparing、migrationConfirm、migrationApplying、migrationCancelling。出力は既定絶対パス+対象件数を確認してEnter、取込はパス→preview→既定取消の二択で確定する。競合時は実行できず再入力/戻るを示す。各状態のhelp/フッター/幅高制限/スクロールを統合する。
- [ ] m.workによる実worker登録とoperation照合を使い、移行jobの実行完了result/errorはmutexで配送前から保持する。Escはjobのctxだけ取消して完了を待ち、別の移行や更新を開始しない。完了時にCommittedなら成功、未確定なら取消/エラーとして入力/一覧へ戻す。新操作へ古い結果を適用しない。
- [ ] 通常成功時はLoadProfilesの読込結果でm.profilesだけを更新し、選択/検索/タブ/接続IDを復元する。既存uiConnectionのprofile・progress・cancelは書き換えない。workerからモデルの状態を直接更新しない。
- [ ] 画面終了は既存closeWork/workers.Waitを使う。runConnectModelは待機後・ctx.Err等の早期return前に未表示の確定済みmigration結果/残存バックアップ案内を端末へ出す。配送の有無を副作用の成功判定に使わず、成功済み取込を取消表示で失わない。
- [ ] `TestConnectionsMigrationLifecycle`で偽接続2本と別所有fixtureを保持したexport/import、背景接続終了、Esc→完了→再試行、古いresult、未配送の確定resultからCtrl+C、外側ctx終了を検証する。通常移行では接続を止めず、Ctrl+Cは従来どおり自画面だけ停止することを区別する。
- [ ] `go test -race ./... -run 'TestConnectionsMigration|TestMigration|TestConnectUI|TestConnections(Action|Help)' -count=1` がPASS。READMEへ旧PC出力→受渡し→新PCdry-run/取込→doctor/Googleログイン/DBツール再出力と手動復元を日本語で記載する。

## 段階4: 独立レビュー・最終検証・配布物

Primary: cli.goの版/README.md/scripts/package-release.py、必要な版fixture。Allowed: 全段階で指摘された契約内の修正、除外済み私有台帳とPTY手順。Forbidden: remote操作・実インストール・過去の版別配布物変更。

- [ ] 版を0.8.0へ更新する。現行版fixtureは0.8.0、従来の将来更新fixture0.8.0は0.9.0として現行版と区別し、過去の資料を書き換えない。配布READMEにも移行操作と除外対象を加える。設定形式Version 1/移行形式version 1は製品版と独立に維持する。
- [ ] 自己点検・必要なcommit整理後に候補を凍結する。独立Reviewer1名へ履歴なしでspec/plan/base/candidate/絶対cwd/全差分/重点5項目/検証結果を渡す。修正は同じ実装担当、再レビューは同じReviewerで1回を目安とする。自己レビューを独立レビューの代用にしない。
- [ ] 同一候補で `go test -race ./... -count=1 -timeout=120s`、`go test -race -tags installer ./... -run 'TestInstaller|TestSetupPathCLI' -count=1`、通常/installer両vet、gofmt/diff checkを実施する。
- [ ] 除外済み一時領域のCLI実PTYで既定取消/実取込/再取込を確認する。UIのauto/light/NO_COLORで2接続→移行出力→別の偽PCの取込ファイル→競合/取消/再試行→取込→保存一覧を確認し、Ctrl+C後の端末復元を確かめる。既存4形式出力/検索削除/Port変更/更新入口も回帰確認する。60列16行のパス・競合・helpを確認する。
- [ ] 他のgate完了後に `python3 scripts/package-release.py` でv0.8.0用CLI5環境/installer3環境を作る。既存の署名/形式/版/同梱CLI・manifest/ZIP/ライセンス/全10公開予定assetのsize/SHA256を同じ候補で照合する。既存v0.7.0出力を上書きしない。
- [ ] candidate/plan/scope/argv/Go版/cache/env/各結果/配布manifest/未確認範囲を `.superpowers/sdd/2026-10-19-pc-migration/` へ記録する。レビュー済み候補と最終gateを一致させ、候補凍結後の完了チェックだけの文書commitは追加せず私有台帳へ記録する。

## 実行方式と停止条件

推奨はこのチャットでの直列実装と全差分の独立レビュー。snapshot/排他/取消/画面終了の連動が強く、段階ごとの新しい担当への引継ぎより同じ担当が状態契約を維持しやすい。段階数をTask数へ変換しない。
実行方式と計画の利用者確認後、実装・レビュー・補正・最終検証まで同じ範囲を継続する。別のユーザー所有Taskは作成しない。新規worktreeが必要かは実行時に現行checkout・tool契約とusing-git-worktreesに従って判断し、未コミット変更を移動/破棄しない。
Goは `.tools/go/bin/go`、`GOTOOLCHAIN=local`、`GOCACHE=$PWD/.cache/build`、`GOMODCACHE=$PWD/.cache/mod`。上記のgoコマンドはこの実体を使う。Git管理情報・loopback・PTY・子プロセスは必要な環境権限を取得する。
設計の保存形式/競合方針/取消の意味/外部副作用/scopeの変更が必要なら親へ戻す。契約内の実装不具合は同じ担当で直す。新しい変更・失敗・未解決懸念がなければ成功済みgateを反復しない。
Windows実機がない場合は実機済みとせずcross-build/境界テストの範囲を明示する。実GCP/DB、実DBツールGUI、利用者の認証/設定/稼働Proxyは一切検証対象にしない。公開・mainマージ・端末更新は後続の明示依頼でのみ実行する。
