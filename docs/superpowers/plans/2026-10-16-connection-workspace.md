# 保存済みポート編集と一覧操作の実装計画

> 実装担当向け: `superpowers:executing-plans`を使い、このチャットの現在の担当が直列に実装する。実装を別Taskや複数エージェントへ分割しない。チェック項目は各段階の完了を記録する。

**目的:** 保存済み設定のポートを削除せず変更でき、接続一覧から出力・ログ・保存・停止を操作できるようにする。

**構成:** 既存の設定ファイルロックをProxy起動とポート更新に共用する。画面は接続一覧を基本とし、入力操作の対象・戻り先を保持する。永続化、Proxyの所有権、画面の実ポートを別の担当へ分割しない。

**技術:** Go 1.26.9、既存のCobra・Bubble Tea・描画処理。依存追加なし。

**設計:** `docs/superpowers/specs/2026-10-16-connection-workspace-design.md`（2026-10-16利用者承認済み）。

**基準:** 公開main `53942fbe2ed1fd7b2efedabcfc46b079eb9179ee`。設計コミット`6a817262776283f85616e4b5521f1032a9b4e5ab`、作業ブランチ`feat/connection-workspace`。この作業場所を継続利用し、履歴バックアップと除外済み生成物を保持する。

## 共通契約と範囲

- 保存済みポートの自動移行は追加しない。編集を実行した対象だけを変更する。
- 接続中のポートは、停止を確認してから変更する。自動停止・再接続は行わない。
- 同じインスタンスの保存済み設定は1つのポートを共有し、名前・DB・ユーザー・接続方式・保存形式Version 1を維持する。
- 起動中・停止中・応答不明の対象、他接続の予約、使用中ポート、同時編集の衝突では書き換えない。同じ値への更新は書き込みなしで成功とする。
- 自動割り当てはPostgreSQL 15432、MySQL 13306、SQL Server 11433から最大20増分のままとする。
- 検索・通常文字入力、4形式の出力、既存ファイル非上書き、署名通信、Proxy所有権、NO_COLOR、60列×16行、更新通知を維持する。
- 対象は`profiles.go`、`sessions.go`、`connections.go`、`selection.go`、`presentation.go`、対応する既存テスト、`README.md`。CLI追加は既存の`profileCommand`内で行う。
- 機能ごとのservice/helperファイル、製品用のテスト待機フック、汎用設定編集、認証・制御プロトコル変更は追加しない。
- 版番号変更、push、PR、mainへのマージ、リリース、端末の実設定変更はこの計画の実行に含めない。

## レビューで重点確認する条件

1. 編集前に起動が旧ポートを読んだ直後、編集だけ成功して起動と保存設定が食い違う競合。段階1の別プロセス検証で保護する。
2. 同じインスタンスの複数DB・ユーザーが一部だけ更新される、または別インスタンスの設定を巻き込む更新。段階1のCLI往復で保護する。
3. 検索後の選択行と以前の接続IDが異なる状態で、保存・停止・出力が別接続に向く操作。段階2の画面テストで保護する。
4. ポート更新後も停止済み行の古い進捗情報が実ポートとして優先され、出力・再試行だけ旧ポートへ戻る状態。段階2の更新・再試行で保護する。
5. 狭い画面や長いログで一覧の選択行、エラーの対処案内、Ctrl+Cのガイドが隠れる表示。段階2の描画テストと段階3のPTYで保護する。

## 段階1: ポート更新と起動の排他

**対象:** `profiles.go`、`profiles_test.go`、`sessions.go`、`sessions_test.go`、READMEのCLI説明。

**インターフェース:**

- `func (a *App) lockProfileStore(ctx context.Context) (func(), error)`を`profiles.go`内に設け、既存`profiles.lock`の親・通常ファイル・同一ファイル検証、取得後の再確認、解除とCloseを集約する。返した解除関数は多重呼び出しでも安全にする。
- `func (a *App) writeProfiles(rows []Profile) error`に既存の整列・Version 1・容量制限・原子的書き込みを集約する。ロック取得は呼び出し元が所有する。既存`changeProfiles`もこの2つを利用する。
- `func (a *App) UpdateProfilePort(ctx context.Context, name string, expectedPort, newPort int) ([]Profile, error)`を追加する。成功時は保存後の全設定を返し、失敗時は更新しない。`expectedPort`は確認済みの旧ポートで、CLI・画面とも事前の読込みから渡す。

- [x] 既存CLI・永続化テストへ、旧5432→15432、同一インスタンス2設定の一括変更、別インスタンス不変、再読込み、4形式の新ポート出力を追加する。変更後の外部契約を検出するテストとし、未実装コマンド・旧ポート出力による失敗を確認する。
- [x] 変更先使用中、別インスタンスの保存予約、対象Proxy稼働中・応答不明、無効ポート、存在しない名前、旧ポートの不一致、取消でファイルのバイト列が変わらないことを検証する。既存の設定破損・リンク拒否テストは更新せず再利用する。
- [x] `UpdateProfilePort`はロック取得後に再読込して旧ポートを比較する。同じ値なら変更なしで返す。それ以外は管理記録の署名付き状態確認を行い、対象が停止済みと確認できない場合はセッションIDと停止・確認手順を付けて拒否する。
- [x] 他の管理セッションも新ポートの予約候補に含める。起動中・応答不明の記録を単に空きとみなさない。新ポートの待受可否を確認後、同じ`ConnectionName`のPortだけを変え、`writeProfiles`で1回保存する。
- [x] `profileCommand`に`update NAME --port`を追加する。`--port`は必須、1〜65535の整数とし、対象の設定・旧/新ポート・影響する設定名を表示する。明示引数を更新指示として扱い、成功後だけ完了とDBツール側の更新・再出力を案内する。
- [x] `Connect`は`resolve`後、保存ポートの参照・予約前に同じファイルロックを取得する。子起動・制御サーバー開始・管理記録公開まで保持し、公開後は起動確認を待たず解除する。失敗時は子の終了と記録の後片付けを経て必ず解除する。ファイルロック→App内ポートmutexの順に統一し、逆順の取得を作らない。
- [x] `TestProfilesConcurrentProcesses`と偽Proxyを使う既存の別プロセスfixtureを拡張し、実ファイルロックで起動/編集の順序を固定する。起動が先なら編集拒否、編集が先なら新ポートで起動または古い明示ポートとの不一致を案内し、旧ポートで黙って起動しないことを確認する。ロック取得待ちの取消、子起動失敗後に次の編集・接続が進むことも確認する。
- [x] focused raceを実行し、この段階をローカルコミットする。起動・停止・通常の設定追加が回帰しないことも既存テストで確認する。

focused: `go test -race ./... -run 'TestProfile|TestProfiles|TestSession|TestConcurrentConnect|TestPort' -count=1`。

## 段階2: 一覧操作・編集フォーム・ログ

**対象:** `connections.go`、`selection.go`、`presentation.go`、`connections_test.go`、`selection_test.go`、READMEの操作説明。

**使う契約:** 段階1の`UpdateProfilePort`。既存の`uiConnection.id/attempt`、`uiResult.operation/owner/attempt`、`connectionProgress.Snapshot`、4形式の出力と非上書き保存を継続利用する。

**画面内の型と入口:**

- `listLocation`を`selection.go`内へ追加し、`tab connectionTab`、`query string`、`inputPos, cursor int`、`connectionID uint64`を保持する。入力操作開始時にこの戻り先を保存し、終了時は同じ行IDを優先して復元する。対象行が消えた場合だけ有効な行へ丸める。
- `func (m *connectUI) actionConnection() *uiConnection`は`matchedConnections`と現在のcursorから対象を返す。一覧の操作で古い`selectedID`を直接使わない。
- `func (m *connectUI) openPortForm(p Profile)`は現在値・旧ポート・影響する設定名・接続ID/試行番号を保持する。接続一覧で設定名が未関連付けなら同じ`ConnectionName`の保存設定を使い、保存設定が0件なら先に保存するよう案内する。
- 状態`actions`、`portForm`を既存状態に加え、`uiResult.kind == "portUpdate"`で段階1の結果を受け取る。操作番号と対象の一致を既存方式で確認する。

- [x] 既存の接続画面テストを一覧主体へ更新する。インスタンス/保存設定のEnter後は接続一覧に選択行があり、背景の成功・失敗・停止でタブ・検索・フォームが変わらないことを失敗として確認する。
- [x] `startConnection`と同一Proxy再利用は接続一覧へ移る。接続一覧のEnterまたはCtrl+Oは操作メニュー、保存済み設定タブはCtrl+Oでポート変更を含むメニューを開く。保存済み設定のEnterは引き続き接続する。
- [x] 一覧のCtrl+E/Ctrl+L/Ctrl+S/Ctrl+D/Ctrl+Rと操作メニューを、同じ選択行から実行する。通常のE/L/S入力は検索文字とし、0件・検索後の別行・同じProxyに紐づく別設定を検証する。メニュー内にキーの対応を表示し、狭い画面のフッターはEnter操作・Ctrl+C全停止を優先する。
- [x] `openForm`、保存結果、`openExportMenu`、出力先の確定/取消を`listLocation`へ戻す。単に保存したことで未接続の行を自動起動しない。出力先のEscはツール選択、ツール選択のEscは元の一覧とする。`profile add`の単独フォーム終了は維持する。
- [x] 保存・出力の完了メッセージ、出力ファイル名、ツール別の取り込み手順を一覧の選択行パネルまたは通知領域へ表示する。背景の状態更新で消さず、既存の出力成功案内を維持する。
- [x] `portForm`は1項目の入力に旧/新ポートと影響設定の確認を付ける。複数設定名の確認領域はPgUp/PgDnで見られるようにし、Enter/Ctrl+Sで更新、Escで無変更のまま一覧へ戻す。待ち時間中のCtrl+Cを含め、既存workerの終了・取消を使う。
- [x] 更新成功では戻り値の全設定を画面へ反映し、同じインスタンスの停止済み行のPortを更新する。DB・ユーザーや他の行を置き換えない。`effectiveProfile`は稼働中なら実ポート、停止後に明示設定があれば設定値、未保存なら前回の実ポートを使い、過去ログは保持する。
- [x] 更新後の再試行と4形式出力が新ポートになること、背景終了中の更新/保存/出力で対象が混線しないこと、更新失敗時に入力値と理由を残すことを画面テストで確認する。
- [x] 接続一覧のログ表示は選択行に追従する。上下は行選択、PgUp/PgDnはログ・対処案内のスクロールに分ける。検索時の注意表示と混同しない。広い画面は右ペイン、狭い画面は一覧を残した上下配置とする。
- [x] 既存の長い対処案内・保存結果・狭幅描画テストを更新する。60列×16行、日本語、更新通知、NO_COLORでも選択行・状態・一覧への戻り・終了案内が欠けないことを確認する。
- [x] focused raceを実行し、READMEとともにローカルコミットする。

focused: `go test -race ./... -run 'TestConnectUI|TestConnections|TestRich|TestProfile' -count=1`。

## 段階3: 独立レビューと最終検証

- [ ] 候補コミットと全差分を固定し、実装者と別のReviewerを1名起動する。設計・本計画・基準/候補SHA・focused証拠を渡し、設定更新の排他、終了順序、操作対象の保持、狭幅表示を監査する。自己確認を独立レビューと呼ばない。
- [ ] 指摘があれば同じ実装担当が補正し、同じReviewerが候補全体を再確認する。外部契約・保存形式・状態遷移の設計変更が必要なら実装を止めて設計へ戻す。
- [ ] 最終候補で`go test -race ./... -count=1`、`go test -race -tags installer ./... -run 'TestInstaller|TestSetupPathCLI' -count=1`、`go vet ./...`、`go vet -tags installer ./...`、gofmt、`git diff --check`を確認する。
- [ ] 最終候補からCLI5環境（darwin/arm64・amd64、linux/arm64・amd64、windows/amd64）を`CGO_ENABLED=0`、`-trimpath -buildvcs=false`でビルドする。`python3 scripts/build-installers.py`でMac2版・Windows版の同梱CLI/manifestと署名・構造を確認する。既存の過去リリースZIPは保持する。
- [ ] 偽gcloud/Proxyと一時設定のみを使うPTY手順を、既存の`.superpowers/sdd/2026-10-16-db-tool-exports/verify-release-pty.py`を参考に更新する。公開前の旧checkoutやユーザーの実プロファイルは検証先に使わない。
- [ ] カラー/NO_COLORで、2接続の追加→一覧でログ切替→停止後ポート更新→再試行→4形式出力→保存/取消を確認する。検索後の対象、背景エラー、長いログのスクロール、100列以上/60列×16行、更新通知、日本語消去、Ctrl+Cと外部接続の維持、端末復元を確認する。
- [ ] 同一候補SHA、実行引数、Go版、環境、結果、未確認範囲を除外済みの`.superpowers/sdd/2026-10-16-connection-workspace/`へ記録する。新しい変更・失敗がなければ成功済み全体gateを理由なく繰り返さない。
- [ ] 製品の受入条件と工程の完了を分けて報告する。GUIでのDBツール取込、実DB接続、Windowsでのセットアップ操作は未確認とし、公開・インストール済みとは扱わない。

## 実行環境と完了境界

Go実体は`.tools/go/bin/go`、`GOTOOLCHAIN=local`、`GOCACHE=$PWD/.cache/build`、`GOMODCACHE=$PWD/.cache/mod`を使用する。focusedの`go`はこの実体を指す。loopbackと別プロセスを使うテスト、およびGit管理情報の更新は必要に応じて環境の承認機構を使う。

3段階を別Taskに変換しない。親が現在の文脈で実装・修正・最終検証を継続し、独立レビューのみ内部委譲する。共有する型と終了順序が多く、実装の並列化は採用しない。設計・計画のモデル設定は現在の設定を維持し、切替済みとは扱わない。

承認後は、実装→独立レビュー→補正→同一候補の最終検証まで自動で継続する。ローカルの意味あるコミットはポート更新・一覧UIを中心に1〜3件とし、テストやdocsだけで細切れにしない。実行前の計画確認を終えた後は、同じ範囲の承認を繰り返さない。
