# DBツール直接登録の実機確認

## 実施範囲

v0.9.0以降のcsqlで確認する。v0.8.0以前にはこの追加機能は含まれない。利用者の通常設定へテスト登録せず、Windows Sandbox・使い捨てVM・新規検証ユーザーを使用する。パスワードは入力せずDBへ接続しない。A5はHKCUへ保存するため、TEMPだけでは隔離できない。

自動テストとクロスビルドはWindows実機での動作を保証しない。pgAdminは9.18/schema52、DBeaver Communityは26.2.3を用意する。A5は通常版amd64の正確な版を記録する。未対応形式として拒否された場合は、普段の保存データでは試行を続けず、版・保存モード・エラーだけを報告する。

## 準備

1. 対象ツールを検証ユーザーで一度起動する。pgAdminは初期化済み設定DB、DBeaverはworkspace6内のGeneralプロジェクト、A5は通常モードを用意する。ツールごとに「既存確認用」の接続定義を1件作り、接続せず終了する。
2. csqlの保存設定を1件用意する。GCPを利用しない検証では、**検証ユーザーのみに**以下のPowerShellで合成設定を新規作成できる。既存ファイルがあれば停止する。

```powershell
$fixtureDir = Join-Path $env:APPDATA 'csql'
$fixturePath = Join-Path $fixtureDir 'profiles.json'
if (Test-Path $fixturePath) { throw '既存設定あり。専用の検証ユーザーを使用してください' }
New-Item -ItemType Directory -Path $fixtureDir -Force | Out-Null
$fixture = @{
 Version = 1
 Profiles = @(@{
  Name = 'fixture'
  Instance = @{
   Project = 'fixture'; Name = 'db'; Region = 'asia-northeast1'
   ConnectionName = 'fixture:asia-northeast1:db'; DatabaseVersion = 'POSTGRES_18'
  }
  Database = 'fixture_db'; Username = 'fixture_reader'; Port = 15439; PrivateIP = $false
 })
}
$json = $fixture | ConvertTo-Json -Depth 5
[IO.File]::WriteAllText($fixturePath, $json, (New-Object Text.UTF8Encoding($false)))
```

3. `csql profile list` で合成設定を確認する。csql connectは不要。ツールの初期状態の件数・登録名・Portだけを記録する。

## 3ツールをまとめて確認する

以下の `<ツール>` を `pgadmin` / `dbeaver` / `a5` に置き換える。`--executable` にはpgAdminのインストールフォルダー、DBeaver/A5の実行ファイルを指定する。標準配置以外では追加の保存先フラグも使用する。

```powershell
csql profile register fixture --tool <ツール> --executable '<配置>' --dry-run
csql profile register fixture --tool <ツール> --executable '<配置>'
```

1. dry-run前後でアプリが起動せず、対象設定・csql登録履歴が増えていないことを確認する。
2. 初回の確認でEnterだけを押し、取消になることを確認する。次は `y` で実行する。
3. 対象ツールを開き、名前・127.0.0.1・15439・fixture_db・fixture_readerを確認する。DB接続は開始しない。既存確認用の定義も変更されていないことを確認する。
4. ツールを通常終了・再起動し、同じ内容が保持されていることを確認して終了する。
5. 同じ登録コマンドを繰り返す。件数が増えず「登録済み」になることを確認する。
6. ツール側でfixtureのPortを15440へ変更して終了する。csqlから登録すると競合し、上書きや追加がないことを確認する。
7. 対象ツールを起動したままcsqlから登録すると、変更せず終了を案内することを確認する。自動終了しないことも確認する。
8. 空白・日本語を含む配置や保存先を試す。pgAdminの別ユーザー・未初期化DB、DBeaverの別workspace/General未作成、A5のPortable/Storeは誤った対象へ書き込まず拒否することを確認する。
9. TUIの保存済み一覧からCtrl+K → ツールへ直接登録を開く。ツール/保存先選択、既定取消、登録結果、Esc/Enterで元の検索・選択へ戻ることを確認する。実Proxy接続がなくても操作できる。
10. バックアップ先と私有権限、一時領域の削除を確認する。登録要求直後の取消は成功と誤表示しないこと、未確認の要求を次回自動再送しないことを確認する。

## ツールごとの追加確認

- pgAdmin: 指定したSQLiteと所有ユーザーにだけ追加されること。設定DBが自動初期化・移行されないこと。`--replace` は使用しない。
- DBeaver: `General/.dbeaver/data-sources-csql-<ID>.json` が1件追加されること。既存 `data-sources*.json` と `credentials-config.json` は保存直後のバイト列が同じであること。csqlの保存完了とGUIの読込み確認を別々に記録する。通常終了後に追加ファイルが保持されることも確認する。
- A5: `/AddDB` と `/Exit` の動作、通常モードのレジストリ形式、同名拒否、日本語DB名/ユーザー名を確認する。設定形式が対応外の場合は副作用なしで拒否されるべきで、推測で別の登録方法を試さない。

## 報告する内容

OS/CPU、csql候補SHA、各ツール版・モード、各手順の合否、変更前後の件数、秘密情報を除いたエラー文をまとめる。設定DB・レジストリのバックアップ・資格情報ファイル・パスワードは共有しない。検証後は使い捨て環境を廃棄するか、検証ユーザーの領域として保持する。

## バックアップから手動で戻す場合

結果に表示するバックアップ内の `復元手順.txt` に、正確な元の保存先とファイル対応を記録する。対象ツールとcsqlを閉じ、現在状態を別の私有フォルダーへ先に退避してから行う。csqlは自動復元しない。

- pgAdminはSQLite backupで作成した `pgadmin4.db` を元の設定DB名へコピーする。元のDBに対応するWAL/SHM/journalも先に別退避し、古い補助ファイルと復元DBを混在させない。JSONは定義確認用でDBの代わりにはならない。
- DBeaverの追加取消は専用追加JSONだけを別退避する。既存ファイルの復元が必要な場合は、復元手順内の `連番-バックアップ名 → 元の絶対パス` に従ってコピーする。既存JSONや資格情報ファイルを無条件で削除しない。
- A5の `000-registry.json` は.regとして取り込めない。復元手順に記載したPowerShellは、現在状態を.regへ別退避し、件数と対象を確認後に `RESTORE` を入力した場合だけ既存値を戻す。追加だけの取消はA5の一覧で該当名だけを手動削除する。

バックアップには元の資格情報が含まれ得る。ログ・チャットへ内容を貼り付けず、不明点があれば原本と現在状態を両方保持して管理者へ確認する。
