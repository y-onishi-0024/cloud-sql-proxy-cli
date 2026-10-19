# csql — Cloud SQL接続CLI

インスタンス名からアクセス可能なGCPプロジェクトを検索し、Cloud SQL Auth Proxyを起動するGo製CLIです。プロジェクト名とリージョンを毎回調べる手間を減らします。

Google Cloudの公式製品ではありません。利用には対象プロジェクトへの権限と、DB側の認証情報が別途必要です。

## ライセンス

本プロジェクトの独自コード・文書は、Copyright (c) 2026 PAL、[MITライセンス](LICENSE)で公開します。
著作権表示と許諾表示を保持する条件で、利用・改変・再配布・商用利用が可能です。無保証で提供します。正確な条件は`LICENSE`の原文を参照してください。ライセンスの定型本文は意味を変えないよう英語の原文を収録しています。

依存ライブラリの条件は[第三者ライセンス表示](THIRD_PARTY_NOTICES.md)を参照してください。第三者のコードや、別途導入するGoogle Cloud CLI・Cloud SQL Auth Proxyを本体のMITライセンスへ変更するものではありません。バイナリを再配布する場合も、`LICENSE`と`THIRD_PARTY_NOTICES.md`を添付してください。

## 利用を始める

CLIの利用にGoは不要です。初回はOS別のセットアップアプリをダブルクリックして起動できます。

[最新版のGitHub Releases](https://github.com/y-onishi-0024/cloud-sql-proxy-cli/releases/latest)から、利用するOSとCPUに合うZIPをダウンロードしてください。各ZIPには日本語の導入案内とライセンスを同梱しています。

| OS | 配布物 |
| --- | --- |
| macOS Apple Silicon | Releasesの `csql-setup-<版>-macos-arm64.zip` を展開して `.app` を開く |
| macOS Intel | Releasesの `csql-setup-<版>-macos-amd64.zip` を展開して `.app` を開く |
| Windows amd64 | Releasesの `csql-setup-<版>-windows-amd64.zip` を展開して `.exe` を開く |

`<版>` は選択したリリースの版番号です。公開中の版はReleasesで確認してください。ローカルで作成した配布用ZIPは `dist/releases/v<版>/` に置きます。

画面の「セットアップ開始」から、導入対象・保存先を確認して実行します。不足ツールの導入とPATH変更は、それぞれ別に承認します。macOSは実際に使うシェルを選んでください。実行中の取消も可能で、取消処理が終わるまで画面を保持します。失敗/取消時は詳細を確認して再試行できます。

通常CLIをセットアップアプリに同梱しており、セットアップアプリ自体をcsqlとして配置しません。Googleのログインは完了後に表示されたコマンドから行います。セットアップ後はターミナルを開き直してください。
macOSのアプリは12.0以降が対象です。ローカル生成物はアドホック署名済みで、公開用Developer ID署名・公証は未実施です。Windowsも公開用の証明書署名は未実施です。

### 起動時の更新通知（v0.2.3以降）

`csql` / `csql connect` の対話起動時に、GitHub Releasesの最新版をバックグラウンドで確認します。新しい正式版があれば、画面上部に版番号を表示します。一覧のF2から更新でき、F1ヘルプから配布先も確認できます。自動インストールは行いません。

確認は最大2秒で、検索・接続操作を待たせません。成功した確認結果を24時間キャッシュし、オフラインやAPI制限時は通常操作を続けます。以前確認した新版がある場合はその案内を保持します。確認先はこの公開リポジトリのGitHub APIだけで、認証情報・接続先・利用者情報は送信しません。

非対話実行、`version` / `--help` などの補助コマンド、`profile add` では更新確認しません。`CSQL_NO_UPDATE_CHECK=1` を環境変数へ設定すると、通信と通知の両方を無効にできます。確認結果はOSのキャッシュ領域の `csql/update-check.json` に保存し、このファイルを削除すると次回の対話起動で再確認します。

### csql内からの更新（v0.7.0以降）

`csql update`で最新版・公式配布元・実際の配置先を確認し、同意するとcsql本体だけを更新できます。非対話では適用しません。公式セットアップでユーザー管理領域へ配置した版が対象です。手動配置などで管理記録が一致しない場合は、公式セットアップで導入状態を確認してください。

起動直後の一覧画面で **F2（csqlを更新）** を押します。接続先の登録・選択やDBへの接続は不要で、インスタンス・保存済み設定・接続一覧のどのタブからも操作できます。検索に失敗した画面でもF2を使えます。従来のCtrl+K →「更新」も利用できます。

版・停止する自画面の接続件数・配置先を確認し、「更新して再起動」を選びます。ファイルの取得・ハッシュと版の検証が終わるまで接続を維持し、検証後に自画面の接続だけを停止して更新します。別端末の接続は停止しません。取消時は一覧の検索・選択と接続を維持します。新版では一覧を開き直すので、DBへの再接続は手動です。長い配布元やパスはPgUp/PgDnで確認できます。

準備中のEscは更新だけを取り消し、Ctrl+Cは従来どおり自画面の接続を停止して終了します。準備失敗では接続と旧版を維持し、配置失敗では旧版と管理記録を戻します。復元自体ができない場合は、画面の旧版バックアップ絶対パスと公式セットアップによる復旧手順を確認してください。Windowsで別のcsqlがファイルを使用中なら、そのcsqlを終了して再試行してください。

起動時の通知と明示更新は別です。`CSQL_NO_UPDATE_CHECK=1`でも、明示した更新操作は最新版を再確認できます。通常CLIのOS/CPU・容量・SHA-256を検証し、設定・認証・Googleツール・PATHは変更しません。配布用の公開鍵署名の検証を行うものではありません。

v0.6.0以前にはこの機能がないため、最初の搭載版への更新だけは従来のセットアップが必要です。搭載版を導入した後の更新から、インストーラーの再取得が不要になります。

ターミナルから設定する場合は、従来のCLIも利用できます。

```sh
./dist/csql setup --install --path
```

不足ツールのインストールとPATH設定をそれぞれ確認してから実行します。既にインストール済みなら `./dist/csql setup --path` だけで設定できます。WindowsのPowerShellでは `./csql.exe setup --install --path` を実行してください。
設定後はターミナルアプリを終了して開き直します。

```sh
csql version
gcloud --version
cloud-sql-proxy --version
```
この作業フォルダでは `./dist/csql` からも実行できます。

```sh
csql doctor
csql setup
csql setup --install
```

`doctor` はgcloudとProxyのパス・バージョン・認証の準備状況を診断します。認証ファイルの存在確認は、認証の有効性・IAM権限・DBログイン・ネットワーク疎通の確認ではありません。
`setup` は導入・PATH設定手順を表示します。`setup --install` は導入対象・配布元・保存先を表示し、対話で確認を得た後に不足コマンドを導入します。
既存の対応コマンドを優先し、足りない場合はユーザー専用領域へ公式配布物を配置します。システム全体のインストールは変更しません。シェル設定の変更は `--path` を指定して確認した場合に限ります。

管理版はgcloud 589.0.0、Proxy 2.24.1です。SHA-256照合、展開先の検証、起動確認を行います。非対話環境ではインストールを開始しません。
自動導入対象はmacOS Intel/Apple Silicon、Linux amd64/arm64、Windows amd64です。他の環境では公式手順を案内します。
macOSとLinux arm64のgcloudにはPython 3.10〜3.15が必要です。対応Pythonが見つからない場合は手動導入を案内します。
途中で片方の導入だけ成功した場合、その結果を保持し、不足を表示して終了します。
同時インストールのロックが異常終了で残った場合は、インストール中のcsqlが存在しないことを確認した上で、表示された管理領域の `.install-lock` ディレクトリを手動で削除してください。

## PATH設定

`setup --path` は起動中のcsqlをユーザー設定領域の `csql/bin/` へ配置し、そのディレクトリと導入済み管理版gcloud・ProxyのディレクトリをPATH末尾へ追加します。未導入の管理版ツールは対象外です。既にPATH上にあるコマンドは引き続き優先されます。表示される衝突情報と設定後の名前解決を確認してください。

```sh
csql setup --path
csql setup --path --shell zsh
csql setup --path --shell bash
csql setup --path --shell fish
```

macOS/LinuxはSHELLから対象シェルを検出します。zshはZDOTDIR以下（未設定ならホーム）の `.zshrc`、bashは `.bashrc` とログイン時に使われる `.bash_profile` / `.bash_login` / `.profile`、fishはユーザー設定の `fish/conf.d/csql.fish` を使います。自動検出と実際の利用シェルが違う場合は `--shell` を指定してください。未対応シェルやリンクされた設定ファイルは自動変更せず、手動設定するディレクトリを案内します。

Windowsで既存PATHの環境変数展開により登録先が変わる可能性がある場合は、自動変更せず手動設定を案内します。
同一の登録項目が複数あり、どの位置がcsqlの所有か判断できない場合も、自動変更せず手動設定を案内します。利用者が設定したPATHの優先順位を推測して変更しません。
Windowsはユーザー環境変数のPathだけを更新します。システムPathは変更しません。--shellの指定は不要です。通知失敗は保存失敗と区別して表示するので、サインアウト後に確認してください。

既存の設定内容・権限を保持し、変更前のファイルを同じディレクトリの `ファイル名.csql-backup-*` へ保存します。再実行で専用ブロックを重複させず、管理版を更新した後は再び `csql setup --path` で登録先を更新できます。csqlの更新は新しい配布バイナリから `setup --path` を実行します。配置済みの別ファイルをcsql所有と確認できない場合は上書きしません。

Unixでの確認:

```sh
command -v csql
command -v gcloud
command -v cloud-sql-proxy
```

PowerShellでは `Get-Command csql,gcloud,cloud-sql-proxy` で確認します。PATH前方の古い版や別コマンドが優先される場合は、表示された既存の設定を利用者が確認してください。

元へ戻す場合は、変更後の利用者編集を残したいかを確認し、表示されたバックアップを元のファイル名へ戻すか、`# >>> csql PATH >>>` から `# <<< csql PATH <<<` のブロックだけを削除してください。fishの専用ファイルはcsqlの設定だけなら削除できます。Windowsは設定領域の `windows-path.csql-backup-*` に元のユーザーPathと種類をJSONで保存します。「ユーザー環境変数の編集」でバックアップのValueに戻します。元にPathが存在しなかった場合（Exists=false）は、csqlが登録した項目だけを削除してください。

途中で失敗した場合は、配置済み/設定済みの表示とバックアップを確認してください。全体成功とは表示しません。`.path-lock` が異常終了で残った場合は、実行中のsetupがないことを確認してユーザー設定領域から削除します。現在の端末のPATHは子プロセスから直接変更できないため、設定後は端末を開き直してください。

## ログイン

検索用のgcloudログインと、Proxy用のApplication Default Credentials（ADC）は別です。

```sh
gcloud auth login
gcloud auth application-default login
csql doctor
```

CLI管理領域へgcloudを導入した場合、`csql setup` が実際の実行ファイルを指定したログインコマンドを表示します。コピーして実行してください。PATHへ追加してgcloud単体で使うこともできます。
CLIはログイン・アカウント変更・プロジェクト切替を自動実行しません。gcloudとADCのアカウントが異なる場合、検索できてもProxyの権限が不足することがあります。
Proxyの認証成功と、DBユーザーによるログイン成功も別です。DBユーザー名・パスワードはDBクライアント側で設定してください。

## 検索・接続

```sh
csql                       # 一覧から選択して接続
csql sample                # 名前で検索して接続
csql connect sample-db     # 明示的な接続コマンド
csql sample-db -p 15433    # ポート指定
csql sample --project my-project
csql sample --private-ip
csql list
csql list sample --refresh
```

インスタンス名の完全一致を優先し、なければ部分一致を使います。大文字・小文字は区別しません。
同名のインスタンスが複数プロジェクトにあれば選択します。対話端末では検索から接続・エラー対処まで全画面で操作します。文字入力で名前・プロジェクトを絞り込み、上下キーで移動、Enterで接続します。検索欄は左右/Home/End・Backspace/Deleteで編集し、Ctrl+Uで全消去できます。Tabでインスタンス・保存済み設定・接続一覧を切り替え、Ctrl+Sで設定を保存します。
F1で現在の画面の操作ヘルプを開き、F1またはEscで戻れます。ヘルプ中も接続状態を更新し、入力内容を保持します。Ctrl+Kで操作メニューを開き、文字入力で「ポート」「port」「ログ」「コピー」などの操作を検索できます。接続一覧のEnter/Ctrl+O、保存済み設定のCtrl+Oからも開けます。利用できない操作には理由を表示します。
表示は端末サイズに追従します。60列×16行以上で利用してください。`NO_COLOR=1 csql`で色を無効にできます。Escは接続を保って接続一覧へ戻ります。Ctrl+Cはこの画面で起動した全接続を停止して終了します。
非対話環境で候補が複数ある場合は自動決定せず候補を表示します。正確な名前と `--project` を指定してください。
### 複数のDBへ同時に接続する（v0.3.0以降）

1. インスタンス・保存済み設定でEnterを押すと、接続一覧に追加し、その行を選択します。接続後も一覧のまま操作できます。
2. Tabで「インスタンス」「保存済み設定」「接続一覧」を切り替え、接続先を追加できます。起動途中でも、他の接続は維持します。
3. 接続一覧はEnterまたはCtrl+Oで操作メニューを開きます。Ctrl+Eはエクスポート、Ctrl+Lはログ、Ctrl+Sは保存・別名保存、Ctrl+Dは個別停止、Ctrl+Rは再試行です。
4. 保存済みポートは、対象を停止後、保存済み設定のCtrl+Oまたは接続一覧の操作メニューから変更できます。同じインスタンスの設定をまとめて変更し、他の接続は維持します。
5. 入力画面やメニューのEscは、元の一覧・検索条件・選択行へ戻ります。Ctrl+Cはこの画面の全接続を停止して終了し、別ターミナルの接続は停止しません。

停止済み・失敗した接続も一覧に残ります。検索欄のE/L/S/N/R等は検索文字、フォームや出力先でも通常文字を入力として扱います。検索中はTabで他のタブへ移動できます。検索中のEscは検索だけを取り消します。一覧のEscは稼働中の接続があれば接続一覧へ戻り、なければ取消終了します。

同じインスタンス・接続方式・ポートの追加は、既存の接続行へ移動します。接続方式や明示ポートが異なる場合は停止してからの変更を案内します。同じProxyを別の保存済み設定で開いた場合はそのDB名・ユーザーを使い、保存・出力には実ポートを使用します。保存済みポートは明示的に編集した場合だけ変更します。初回の`--port`は最初の接続だけに適用します。

非対話実行は従来どおり1件の接続を扱います。`profile add`は設定の追加のみを行います。

`list` や `status` などの予約コマンドと同名のインスタンスには `csql connect list` のように接続します。

検索はgcloudで列挙・参照できるプロジェクトが対象です。権限不足・API無効・通信失敗はプロジェクトごとに表示します。参照不能だったプロジェクトを含む結果では、全体に対象が存在しないとは判断できません。
`--project` を使うとプロジェクト列挙を省略できます。SQL Admin APIの有効化やIAM変更は行いません。
一覧は10分キャッシュします。gcloudアカウント・設定・偽装サービスアカウント・検索範囲が変われば分けて取得します。`--refresh` で更新できます。

Proxyの待受は `127.0.0.1` です。ローカルDBの標準ポートと重なりにくいよう、保存済み設定のないインスタンスでは、ポート未指定時にPostgreSQL 15432、MySQL 13306、SQL Server 11433から最大20増分まで空きと未予約のポートを探します。保存済み設定があれば、一覧からの接続でもそのポートを再利用します。以前の5432などで保存した設定も自動変更しません。使用中でも別ポートへ黙って変更せず、`csql status`で既存セッションを確認するよう案内します。
明示したポートが使えない場合はエラーになります。DB種別を判定できない場合はポートを明示してください。
Proxyはフォアグラウンドで動作し、Ctrl+Cで終了します。表示されたホスト・ポートをDBクライアントへ設定します。
`--private-ip` を使ってもVPNやVPCへの経路は別途必要です。Proxyの構造化ログと起動時の接続テストに対応したProxy v2が必要です（同梱対象2.24.1は対応済み）。起動・接続確認の上限は60秒です。「Proxy利用可能」はCloud SQLへの到達確認であり、DBユーザー名・パスワードによるログインは未検証です。

横幅100列・高さ20行以上では、左の一覧と右の選択詳細を2ペインで表示します。接続一覧は状態・設定/接続先・プロジェクト・Port・DBの列で比較でき、狭い画面では2行表示になります。更新通知があってもタブと現在位置を維持します。それより狭い通常サイズでは1列表示へ戻り、60列×16行未満ではサイズ案内を表示します。接続状態はバッジ、処理中はスピナーで示し、画面下の2行に操作キーを固定表示します。`NO_COLOR`設定時も枠線・選択記号・状態名で判別できます。
認証エラーなどで対処案内が長い場合は、PgUp/PgDnで最後まで確認できます（Macの標準キーボードではfn＋↑/↓）。Ctrl+Lでログを表示でき、PgUp/PgDnでログをスクロールできます。上下キーは一覧の選択に使います。

配色は`CSQL_THEME=auto csql`（既定）、`CSQL_THEME=dark csql`、`CSQL_THEME=light csql`で選べます。autoは`COLORFGBG`で識別できる背景を使い、不明なら端末の標準文字色と反転選択を使います。背景を調べるための端末入力は読みません。`NO_COLOR=1`は色と反転を無効にし、文字と記号で状態を表示します。

認証エラーではCtrl+Kから「コピー」を検索して復旧コマンドを選べます。別ターミナルで実行し、csqlへ戻ってCtrl+Rで再試行してください。検索用の`gcloud auth login`とProxy用の`gcloud auth application-default login`は別の認証です。明示ADCでは通常ADCへの切替を案内しません。csqlが認証コマンドを自動実行することはありません。
コピーはmacOSのpbcopy、WindowsのPowerShell、Linuxのwl-copy/xclipを利用します。未導入・失敗時はコマンド全文を表示するので、PgUp/PgDnで確認して手動コピーしてください。必要ツールの自動導入やクリップボードの読取りは行いません。

## 接続設定を再利用する

インスタンス一覧でCtrl+Sを押すか、次のコマンドで設定名・DB名・ユーザー名を保存します。DB名やユーザー名はインスタンス名・Googleアカウントから推測しません。

```sh
csql profile add dev --project my-project --instance my-db --database app --user reader
csql profile list
csql --profile dev
csql connect --profile dev
```

対話端末では不足項目をフォームへ入力できます。自動選択したポートは保存前に表示するので、確認してEnter/Ctrl+Sで保存してください。非対話環境では必須値をすべて指定します。`--profile`はインスタンス名・`--project`・`--refresh`・`--private-ip`と併用できません。
同じインスタンスの別DB・別ユーザーの設定は同じポートを共有し、別インスタンスへ同じポートを予約しません。設定はPCごとに保存されます。パスワードはDBツール側で管理してください。

### DBツールへ直接登録

保存済み設定の一覧、または接続一覧で **Ctrl+K → ツールへ直接登録** を選ぶと、対象ツール・配置・登録先・登録内容を確認して追加できます。先に対象ツールを終了してください。Proxy接続は維持し、DBへのログインは開始しません。未保存の接続は先に保存してください。

```sh
csql profile register dev --tool pgadmin --dry-run
csql profile register dev --tool pgadmin
csql profile register dev --tool dbeaver --dbeaver-workspace /path/to/workspace6
# Windows amd64の通常版A5
csql profile register dev --tool a5 --executable 'C:\Tools\A5M2\A5M2.exe'
```

`--dry-run` は外部プロセスもファイル書込みも行いません。実登録は対話端末で、既定を取消とする確認後に実行します。`--executable` はpgAdminではインストールフォルダー、他のツールでは実行ファイルを指定します。候補が複数・未検出の場合は明示してください。

| ツール | 登録先・方式 | 互換性と確認状況 |
| --- | --- | --- |
| pgAdminデスクトップ | macOS `~/.pgadmin/pgadmin4.db`、Windows `%APPDATA%\pgAdmin\pgadmin4.db`。同梱Pythonによる公式 `load-servers` | 9.18 / schema 52。macOSでは一時DBで追加・重複防止・既存保持を実測。Windows実機は未確認 |
| DBeaver Community | macOS `~/Library/DBeaverData/workspace6`、Windows `%APPDATA%\DBeaverData\workspace6` の `General/.dbeaver/data-sources-csql-<ID>.json` を新規保存 | 26.2.3の公式形式・parserに対応。保存とJSON照合を確認。実アプリでの読込み・再起動保持は別途確認が必要 |
| A5:SQL Mk-2 | Windows amd64通常モードのHKCU `Software\mmatsubara\A5M2(x64)\DataSources`、公式 `/AddDB` | Windows実機での保存形式・`/Exit`・永続化は未確認。判別できない保存形式は書込み前に拒否 |

pgAdminの保存先・所有ユーザーは `--pgadmin-sqlite` / `--pgadmin-user`、DBeaverは `--dbeaver-workspace` で変更できます。pgAdminのスキーマと重複検査は確認後に行うため、dry-runでは「確認後に検査」と表示します。未初期化・独自設定・対応外の版では、従来のファイル出力を利用してください。A5のPortable・Store・workspaceモード、pgAdminのDocker/Web版、DBeaver PRO、Linuxの直接登録は対象外です。

同じ登録名と接続内容がある場合は追加しません。内容が異なる場合やユーザー名を照合できない場合は競合として止めます。保存済みPortを変更した後も、既存のDBツール設定は自動上書きしません。ツール側でPortを変更するか、別の保存名を使用してください。パスワードは登録しません。

DBeaverでは既存JSONや暗号化資格情報を編集・復号せず、専用JSONだけを追加します。「追加JSONを保存」はファイルの確定を意味します。次回起動時にGeneralプロジェクトで名前・Host・Port・DB・Userを確認してください。ツールの起動やDB接続は自動実行しません。

バックアップはcsql設定ディレクトリの `db-tool-backups/<tool>/<日時と一意ID>/`、登録履歴は `db-tool-registrations.json`、直前の登録先入力は `db-tools.json` に保存します。これら端末固有のデータはPC移行へ含めません。一時領域はcsqlキャッシュの `db-tool-register/` で、処理終了時に削除します。バックアップは元の資格情報を含み得るため共有しないでください。

要求後のタイムアウト・取消では「結果未確認」と表示し、自動再送しません。ツール側で登録有無と重複を確認してください。実保存先で同じ内容を照合できた場合は、再送せず「登録済み」に確定します。自動復元もしません。各バックアップ内の `復元手順.txt` に元の保存先・ファイル対応・OS別の手動手順を残します。画面終了と登録確定が重なった場合も、結果とバックアップ先を端末へ出力します。

Windows実機などの確認は [検証手順](docs/db-tool-registration-verification.md) を参照してください。既存のエクスポートは引き続き利用できます。

### pgAdminデスクトップ版

```sh
csql profile export dev --format pgadmin --output ./dev-servers.json
```

初回だけpgAdminの **Tools → Import/Export Servers** からJSONを取り込みます。**Remove all the existing servers? は無効のまま**対象だけ追加してください。パスワードはJSONに含めず、pgAdmin側で入力・管理します。次回からは`csql --profile dev`を起動して、登録済みの接続を開けます。同じJSONを毎回取り込むと重複する可能性があります。
接続一覧でCtrl+E（エクスポート）を押してpgAdminを選ぶと、`csql`を起動したフォルダの`csql-設定名-pgadmin.json`を保存先として自動入力します。Enterだけで保存でき、必要ならパスを編集できます。同名のファイル・ディレクトリ・リンクがある場合は`-2`、`-3`…を付け、既存ファイルは上書きしません。Escで戻った場合は出力しません。保存完了後も出力先を表示します。
出力先は同じ端末の`127.0.0.1`です。Dockerや別サーバー上のpgAdminでは、同じアドレスでこの端末を参照できません。

### 他のDBツール

```sh
csql profile export dev --format uri
```

パスワードを含まないPostgreSQL/MySQLの接続URIを表示します。対応するDBツールで取り込むか、表示したHost/Port/DB/Userを初回設定に使ってください。SQL Serverの汎用URI出力は未対応です。
接続一覧のCtrl+Eまたは操作メニューのエクスポートからpgAdmin・DBeaver・A5:SQL Mk-2・接続URIを選択できます。↑↓またはTabで選び、Enterで出力先へ進みます。保存先はすべて自動入力され、Enterだけで新規ファイルへ出力できます。出力先のEscはツール選択へ、その画面のEscは元の一覧へ戻ります。長いURIは上のコマンドやファイル出力から全体をコピーできます。出力先の既存ファイルは上書きしません。

### DBeaver

```sh
csql profile export dev --format dbeaver --output ./dev-dbeaver.csv
```

DBeaverの **File → Import → Third Party Configuration → Custom** から、対象DBのドライバーを選び、CSV・UTF-8を指定して取り込みます。PostgreSQL・MySQL・SQL Server向けのHost/Port/DB/Userを出力します。パスワードはDBeaver側で入力してください。画面から出力する場合の保存先は`csql-設定名-dbeaver.csv`です。
取り込み手順と列名は[DBeaver公式ドキュメント](https://dbeaver.com/docs/dbeaver/Admin-Manage-Connections/)に従います。

### A5:SQL Mk-2

```sh
csql profile export dev --format a5 --output ./dev-a5.a5dblist
```

A5の **データベース → データベースの追加と削除 → インポート** から読み込みます。PostgreSQL・MySQL・SQL Server向けの接続一覧をUTF-8 BOM付き・CRLFで出力し、パスワードは含めません。文字列の誤解釈を避けるため、DB名・ユーザー名にセミコロンまたは二重引用符が含まれる場合はA5出力を拒否します。
画面から出力する場合の保存先は`csql-設定名-a5.a5dblist`です。A5はWindows用なので、通常は同じWindows端末でcsqlを起動してください。登録形式は[作者による接続文字列の説明](https://a5m2.mmatsubara.com/wp/?bbp_reps=1&bbp_user=1&paged=73)を参照しています。実際のA5での取り込みは、このMac上の自動検証に含みません。

ツール選択で対象DBに対応しない形式を選んだ場合は、理由を表示し、別の形式を選び直せます。保存完了後はツールごとに取り込み手順を表示します。既存ファイルがある場合は`-2`などを付けて候補を提示し、確認後に生じた競合でも上書きしません。

```sh
csql profile remove dev
```

削除は対話確認後に行い、稼働中ProxyやDBツールの登録は変更しません。保存済みポートは、対象Proxyを停止してから `csql profile update 設定名 --port 15432` で変更できます。同じインスタンスの保存設定をまとめて更新します。DBツール側のPortも変更するか、接続設定を再エクスポートしてください。
設定ファイルが壊れた場合は空の設定として上書きせず停止します。表示した`profiles.json`を退避して修復してください。`profiles.lock`はOSの排他用で、異常終了後もファイル自体は残りますが排他は解除されます。

## 新しいPCへの接続設定の移行

旧PC・新PCのcsqlをv0.8.0以降に更新してから、旧PCで保存済みの接続設定をまとめて出力します。未保存の接続は先に保存してください。

```sh
csql migrate export
csql migrate export --output ./csql-transfer.json
```

省略時は起動フォルダに日時付きの `csql-migration-<UTC日時>.json` を作り、実際の絶対パスと件数を表示します。同名ファイルは上書きしません。画面では起動直後の一覧の **F3 データ移行 → 全接続先を書き出す（旧PC）** から、保存済み接続先を全件まとめて出力します。検索条件・選択行には依存しません。保存先とファイル名は入力済みなので、対象件数を確認してEnterで出力できます。未保存の接続は先にCtrl+Sで保存してください。従来のCtrl+Kから「移行ファイルを書き出す」も利用できます。

ファイルを新PCへ渡し、v0.8.0以降のcsqlで内容を確認して取り込みます。Mac・Windows・Linuxで同じJSONを使えます。

```sh
csql migrate import ./csql-transfer.json --dry-run
csql migrate import ./csql-transfer.json
```

画面では起動直後の一覧の **F3 データ移行 → 移行ファイルを取り込む（新PC）** を選び、ファイルのパスを入力します。保存済み設定や接続が0件でも、接続先を選ばずに利用できます。検索エラー画面にもF3を表示します。ファイル内の全接続設定が対象です。追加・同一・競合の一覧を確認し、「取り込む」を選ぶまで設定は変更しません。従来のCtrl+Kから「移行ファイルを取り込む」も利用できます。`--dry-run`は確認のみ、実取込は対話端末での確認が必要です。画面のパス入力では引用符付きの貼付けと `~/` に対応します。シェル式や環境変数は実行・展開しません。

同じ設定はスキップします。同名で内容が異なる場合や保存済みPortの予約が矛盾する場合は、全件の取込を止めて理由を表示します。Portの自動変更・部分取込・既存設定の上書きはしません。Portの修正は `csql profile update NAME --port PORT`、名前や内容の修正は移行元の設定を修正して再出力してください。新PCの他アプリによるPort使用は接続時に検査します。

既存設定へ追加する際は、設定領域の `profiles.pre-import-*.json` へ元の設定をバックアップしてから保存します。実際のバックアップ絶対パスを完了結果に表示し、画面ではF1から長い結果・復元手順を確認できます。復元する場合は、csqlを終了してからそのバックアップを同じ設定領域の `profiles.json` へ戻してください。移行ファイル自体を `profiles.json` にコピーしないでください。バックアップは自動削除しません。元ファイルのない初回取込や、全件同一で変更がない場合はバックアップを作りません。

移行対象は保存済みの接続先・DB名・DBユーザー名・Port・Private IP設定です。ファイルは暗号化せず、プロジェクト名やDBユーザー名も含むため、渡す相手と保存先を確認してください。Google認証・トークン・秘密鍵・DBパスワード・稼働セッション・ログ・キャッシュ・PATH・導入済みツール・DBブラウザ自体の設定は含みません。移行操作は接続中のProxyを停止・起動しません。Ctrl+Cで画面全体を終了した場合は従来どおりその画面の接続を停止します。

新PCで `csql doctor` / `csql setup` を実行し、足りないツールは `csql setup --install`、Googleログインは表示されたコマンドで準備してください。pgAdmin・DBeaver・A5の設定は新PCのcsqlから必要な形式へ再出力します。移行ファイルは2MiB、取込後の保存設定は1MiBまでです。

## エラーが出たとき

エラーは失敗した操作・原因・次の操作・詳細を表示します。接続一覧ではCtrl+Lでログを切り替え、対処後はCtrl+Rで再試行できます。前の所有Proxyの終了を待ってから再起動します。

| エラー | 対処 |
| --- | --- |
| Proxyの`invalid_rapt` / ADCの再認証 | `gcloud auth application-default login`後にProxyを再起動 |
| 検索用gcloudの未認証・再認証 | `gcloud auth login`後に一覧を再取得 |
| IAM権限不足 | 対象と利用中アカウントの権限を管理者へ確認 |
| Cloud SQL Admin API無効 | 対象プロジェクトのAPI状態を管理者へ確認 |
| 保存済みポートが使用中 | `csql status`と`csql profile list`で確認 |
| 通信時間切れ | DNS・通信状態、Private IPの場合はVPN/VPCへの経路も確認 |

明示した認証ファイルを使う環境では、その認証設定の確認を優先して案内します。DBクライアントのパスワード不一致はProxyから検証できません。ログインやIAM/API変更をCLIが自動実行することはありません。
ログは保持上限内で表示し、既知の秘密値・端末制御文字を除去します。認証ファイル本文の取得や永続ログ保存は行いません。

## 状態確認・停止

接続中のターミナルを開いたまま、別ターミナルから操作できます。

```sh
csql status
csql stop SESSION_ID
csql stop --all
```

起動時と `status` にセッションIDを表示します。`stop` はこのCLIが所有しているProxyだけを停止し、子プロセスの終了を待って結果を返します。
制御通信は要求・応答双方の署名で所有者を確認し、保存された秘密トークンそのものを送信しません。旧形式の記録は問い合わせず「応答なし」とし、起動した端末からCtrl+Cで停止・再起動するよう案内します。
稼働表示は子プロセスの生存確認です。DBログインの成功を意味しません。
親プロセスが強制終了して管理記録だけ残った場合は「応答なし」と表示します。PID番号を頼りに他のプロセスを停止しません。応答なしを停止成功とは扱いません。
通常の停止要求後10秒で終了しなければ、所有するProxyを強制終了します。Windowsで通常終了通知を利用できない場合も強制終了へ切り替えます。
バックグラウンド常駐化・履歴・お気に入りは初期版に含めません。

## 保存先

設定（`profiles.json`）・セッション・管理ツールはOS標準のユーザー設定領域の `csql/`、一覧キャッシュはユーザーキャッシュ領域の `csql/` に保存します。
macOSでは設定は `~/Library/Application Support/csql/`、キャッシュは `~/Library/Caches/csql/` です。
Unixの管理ディレクトリは0700、管理ファイルは0600です。セッション制御用のトークンは保存しますが、GCPのトークン・秘密鍵・DBパスワードはCLIで取得・保存しません。

## 開発と検証

Go 1.26.9を使用します。依存はgo.mod/go.sumで固定しています。
Go 1.26.9がPATH上にある環境では、リポジトリのルートで実行します。

```sh
go test -race ./...
go vet ./...
go build -o dist/csql .
```

`.tools/`は開発者がローカルへ配置する任意のツール領域で、公開ソースには含めません。ここへ導入済みのGoを使う場合:

```sh
export GOTOOLCHAIN=local
export GOCACHE="$PWD/.cache/build"
export GOMODCACHE="$PWD/.cache/mod"
.tools/go/bin/go test -race ./...
.tools/go/bin/go vet ./...
.tools/go/bin/go build -o dist/csql .
```

テストは偽のgcloud・Proxy、一時領域、ループバック通信を使います。実際のGCPリソースやDBには接続しません。
複数OS向けのビルド成功と、各OSでの実行・実DBへの接続成功は分けて確認します。
終了コードは成功0、通常失敗1、入力不備2、利用者の取消130です。

設計と実装計画は [設計書](docs/superpowers/specs/2026-10-14-csql-design.md) と [実装計画](docs/superpowers/plans/2026-10-14-csql.md) を参照してください。

PATH追加の仕様と計画: [設計](docs/superpowers/specs/2026-10-15-path-support-design.md) / [計画](docs/superpowers/plans/2026-10-15-path-support.md)。

## セットアップ配布物のビルド

macOS/Xcode・Python 3・Go 1.26.9がある環境で、3種類の配布物を作ります。

```sh
python3 scripts/build-installers.py
```

ターゲットを限定する場合（Windows上ではWindowsだけを指定）:

```sh
python3 scripts/build-installers.py --target windows/amd64
python3 scripts/build-installers.py --target darwin/arm64
```

通常CLIを先にビルドし、OS/CPU・版・SHA256を記録した同梱物としてGUIへ埋め込みます。GUIの種類、同梱物、macOSの.app/署名/ZIP構造を検証します。生成途中のファイルは `.cache/installer-build/`、配布物と同梱物manifestは `dist/` に保存します。公開用の証明書署名・公証・配布は別途行ってください。

通常CLIはCgoなしのままです。セットアップアプリはmacOSのみAppKit/Cgo、WindowsはWin32を使います。GUIを開かず共通処理を検証する場合:

```sh
.tools/go/bin/go test -race -tags installer ./... -run 'TestInstaller|TestSetupPathCLI'
```

[セットアップ設計](docs/superpowers/specs/2026-10-15-gui-installer-design.md) / [実装計画](docs/superpowers/plans/2026-10-15-gui-installer.md)
