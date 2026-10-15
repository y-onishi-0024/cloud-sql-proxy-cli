# PATH設定サポートの実装計画

> 実装担当: superpowers:executing-plansを使い、このチャットの親が連続したフローを実装する。

目的: csql・導入済み管理版gcloud・cloud-sql-proxyを確認付きでユーザーPATHへ登録する。
設計: [PATH設定仕様](../specs/2026-10-15-path-support-design.md)。初期仕様と競合するPATH変更の契約は新仕様を優先。
技術: 現行Go 1.26.9、Cobra、標準ライブラリ、既存golang.org/x/sys/windows。新規依存なし。
base: main / 8d709621bd849c3e4a2d3d29805c6e1488ad461a、着手時clean。
lane: repository固有定義なし。running-staged-developmentを適用。

## 担当と停止境界

親が設計・実装・focused検証・修正まで所有する。既存setupと確認・保存状態を共有するため実装を並列分割しない。
別ユーザーTaskは依頼されていないため作成しない。独立監査は既存Reviewer /root/reviewへ固定候補と本spec/planを渡す。
レビュー1回、blocking修正後は同じReviewerで再確認1回を目安とする。
状態遷移・外部契約・対象範囲を変える必要があれば親へ戻して補正する。push/PR/merge/deploy、開発中の実ユーザーPATH変更は対象外。
設計・計画をユーザーに確認してから実装する。コードと候補を変更しなければgate証拠を再利用する。

## Stage 1: 設定の計画・確認・適用

Primary: 新規pathsetup.go、pathsetup_unix.go、pathsetup_windows.go、pathsetup_test.go。
責務: 共通ファイルで対象選定・確認・コピー・バックアップ・設定ブロックを追え、OSファイルはWindowsのレジストリ/通知境界とUnixの対応境界だけを分離する。

- [ ] `App.SetupPath(ctx context.Context, shell string) error` を実装する。読み取りと変更案作成、対話確認、再検証、コピー/適用、結果案内の順に進める。
- [ ] 設定保存先・自身の実行ファイルを一時領域へ差し替えられる境界をAppへ追加し、テストが実ホームを操作しないようにする。ホーム・実行ファイル・WindowsユーザーPathの取得/保存は依存境界として扱う。
- [ ] pathsetup_test.goで確認前副作用なし、既存内容保存/バックアップ、シェル起動からの名前解決、再実行、既存優先、版更新をRED→GREENで保護する。
- [ ] 文字引用、壊れたマーカー、リンク、未知ファイル上書き、同時実行/同時変更、部分失敗、Windowsの値/種類/所有項目保持を観測可能な結果で検証する。
- [ ] WindowsのユーザーPath読み書き・通知をx/sysで実装する。レジストリ値の未展開文字列と種類を保持し、プロセスPATHのコピーをユーザーPathへ書かない。
- [ ] focused: `go test ./... -run 'TestSetupPath'`。必要な場合だけloopback用の権限を利用する。

## Stage 2: CLI統合と導入手順

Primary: cli.go、setup.go、setup_test.go、cli_test.go、README.md。
Allowed: dependencies.go（管理版検証の既存処理を再利用する局所変更）、初期specへの新仕様リンク。
Forbidden: discovery.go、sessions.go、process_*、認証/接続/キャッシュ契約変更。

- [ ] setupへ `--path` / `--shell` を追加する。通常setup、install単独、path単独、install成功後pathの4経路を維持する。
- [ ] 確認入力は1つのバッファで管理し、既存インストール確認とPATH確認の回答を先読みで失わない。確認待ちのcontext取消を保持する。
- [ ] install失敗/取消ならPATHへ進まない。成功済み部分は再利用でき、PATH失敗をインストール失敗と混同しない。
- [ ] TestSetupPathCLIで引数不備・2回の確認・最初/2回目の取消・インストール失敗後のPATH非変更を保護する。既存setup試験を再利用/更新する。
- [ ] READMEへ初回 `./dist/csql setup --install --path`、再設定、--shell、端末再起動、名前解決確認、復旧、既存コマンド衝突の手順を日本語で記載する。
- [ ] 通常setupの案内は書き込みをしない。実ユーザーPATHの検証は実施しない。

## Stage 3: 独立レビューと最終gate

- [ ] 自己確認とfocused試験後、実装を意味のある1〜2個のローカルcommitへまとめる。
- [ ] 固定candidate SHA、clean状態、baseからの全差分、spec/plan、検証証拠を既存Reviewerへ渡す。所有境界・設定破壊・コマンド注入・PATH優先順位・Windows保存契約を重点監査する。
- [ ] BLOCKER/HIGHは解消必須。MEDIUMも本scope内なら修正し、それ以外は実害と残件を報告する。
- [ ] 最終候補で `go test -race ./...`、`go vet ./...`、gofmt/diff check、darwin amd64/arm64・linux amd64/arm64・windows amd64 build。
- [ ] dist/csqlを更新し、setupヘルプと一時ホームでPATH設定→別ディレクトリでversionの実行を確認する。
- [ ] 変更・証拠・実OS未確認範囲を最終報告する。利用者の実ホームへの適用を完了扱いにしない。
