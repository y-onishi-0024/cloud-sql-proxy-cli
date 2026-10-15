# macOS/Windowsセットアップアプリ実装計画

> 実装: superpowers:executing-plansを使い、このチャットの親が継続する。

目的: ダブルクリックで開くネイティブの確認・進捗画面から、既存setupとPATH登録を実行する。
正本: [GUIセットアップ仕様](../specs/2026-10-15-gui-installer-design.md)、既存CLI/PATH仕様。
base: feat/path-support / 1009fe3b625ab832d9b7f880b85cef828700c555、clean。
承認: 2026-10-15の両OSダブルクリック導入案への「OKそれで進めて」。画面実装/通常CLI同梱/ユーザー領域導入を具体化し、この範囲を再承認待ちにしない。
lane: 固有定義なし、running-staged-developmentを適用。Go1.26.9、既存x/sys、AppKit/Win32のみ。新規Goライブラリ依存なし。

## 担当・境界

設計・実装・focused検証・修正は親1名。共有controller/確認/同梱CLIを使うためOS別に実装担当を分けない。
独立性のため最後に実装者と別のReviewerが全差分を監査する。内部Reviewer1名、必要修正後は同じ担当が再確認する。
別ユーザーTaskの依頼はないため作成しない。モデルの自動切替を主張せず、親は現在のモデルで継続。レビューのモデル/effortはrouting skillで照合する。
契約/外部副作用を変える必要があれば親へ戻す。push/PR/merge/deploy/公開署名や公証は実行しない。

## Stage 1: 共通controllerと確認境界

Primary: 新規installer.go、installer_test.go、installer_payload/README.md。
Modify: main.go、cli.go、setup.go、dependencies.go。

- [ ] 通常CLI入口を保持し、installer tagのinitが設定する入口だけGUIへ分岐する。mainからCLI設定や認証の処理を移動しない。
- [ ] App.ConfirmAction(context.Context,string)(bool,error) を追加する。未設定時のTTY拒否/既存確認を保持。設定時もcontext取消を前後で確認する。
- [ ] osRunnerへGUI専用のexec.Cmd準備フックを追加する。通常runnerは従来のまま。
- [ ] controllerのStart(shell)/Answer(id,bool)/Cancel/Snapshotを実装。状態・ログ・確認要求の同期、単一worker、再試行、ログ上限を持つ。
- [ ] 同梱payloadをmanifest（kind=csql-cli、version=現行版、OS/CPU、SHA256）で検証し一時配置する。閉じる前にworkerとcleanupが完了する。
- [ ] GUI非TTYの2回確認と通常CLI配置、取消・失敗/再試行・二重開始/古いID・payload不整合をRED→GREENで保護する。既存setup/PATH fixtureを再利用する。
- [ ] focused: `go test -tags installer ./... -run 'TestInstaller|TestSetupPathCLI'`。GUIを開かず注入したApp/表示snapshotで副作用を検証する。

## Stage 2: OS標準画面と配布物

Primary: 新規installer_native_darwin.go / installer_native_darwin.m / installer_native_windows.go / installer_native_other.go、scripts/build-installers.py。
Allowed: .gitignore、README.md（利用/開発/署名状況）、必要な最小のC宣言。
Forbidden: discovery.go、sessions.go、process_*、検索・認証・Proxyの状態契約変更。

- [ ] macOSはAppKit main threadで画面を開き、短いtimerからsnapshotを反映する。workerはGo goroutine。シェル選択と確認待ちボタンを使う。
- [ ] WindowsはWin32 message loop、readonly multiline log、主操作/取消、状態表示を作る。GUI用外部コマンドはHideWindow/CreateNoWindowを設定する。
- [ ] 起動時に未導入処理を始めない。閉じる/取消はworkerを取り消し、終了まで画面を保持する。完了/失敗/取消を区別し、失敗と取消では再試行できる。
- [ ] Finderの最小PATHに一般的なHomebrew設置先をプロセス内で補完し、シェル設定は実行しない。
- [ ] build scriptは通常CLI→payload manifest→CPU別GUI/.app/ZIPの順で独立stage内に生成。通常go buildやgo testが生成payloadを必要としない形にする。
- [ ] Windows amd64のGUI subsystem exe、macOS amd64/arm64の.app/ZIPをdistへ出す。macOSはアドホック署名・構造を検証する。証明書付き配布と混同しない。
- [ ] READMEへダブルクリック導入、確認/取消/再試行、Googleログイン、OS別実行要件、ビルド方法と未署名/未公証の配布前工程を日本語で記載する。

## Stage 3: 実画面・独立レビュー・最終gate

- [ ] macOSのパッケージ済み画面を実際に開いて表示と取消を確認する。導入する検証は一時HOME・偽ツール/fixtureだけに限定する。
- [ ] script生成物のmanifest SHA/OS/CPU、CLI payloadがGUI本体ではないこと、ZIPの実行権限と.appの署名検証を確認する。
- [ ] focused成功後、意味のある1〜3個のlocal commitへまとめる。固定候補・clean状態・base全差分と検証をReviewerへ渡す。
- [ ] BLOCKER/HIGHと本scope内MEDIUMを修正し、同じReviewerに確認する。新機能や任意refactorは増やさない。
- [ ] 最終候補で通常CLI `go test -race ./...`、installer tagのfocused/race、通常/installer vet、5target CLI build、3target GUI build、gofmt/diff check。
- [ ] 同条件で成功したgateは再利用する。コード変更・失敗・新懸念がある部分だけ再検証する。
- [ ] 生成物の絶対パス、レビュー/検証結果、Windows GUI実行/公開署名/公証の未確認範囲を報告する。実HOME/GCP/DBの導入成功は主張しない。

## レビュー重点

確認のない導入、古い確認IDの流用、取消後の副作用継続、GUI自体のCLI配置、payload不整合、ログ/状態のdata race、native UI thread違反、Windowsコンソールの誤表示、GUI起動のPATH差、部分失敗の成功表示。
