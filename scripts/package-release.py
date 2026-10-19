#!/usr/bin/env python3
"""同じ候補のinstaller ZIP・通常CLI・照合情報を版別に準備する。公開は行わない。"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import plistlib
import re
import shutil
import struct
import subprocess
import sys
import zipfile

ROOT = Path(__file__).resolve().parents[1]
TARGETS = ["darwin-arm64", "darwin-amd64", "windows-amd64", "linux-arm64", "linux-amd64"]


def no_links(path):
    for item in [path, *path.parents]:
        if item.is_symlink():
            raise RuntimeError(f"リンクを配布先として使いません: {item}")


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def run(argv, env):
    subprocess.run([str(a) for a in argv], cwd=ROOT, env=env, check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", help="使用するGo実行ファイル")
    args = parser.parse_args()
    version = re.search(r'^const version = "([^"]+)"', (ROOT / "cli.go").read_text(), re.M).group(1)
    if not re.fullmatch(r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)", version):
        raise RuntimeError("正式版の版番号が必要です")
    out = ROOT / "dist/releases" / ("v" + version)
    no_links(out)
    if out.exists():
        raise RuntimeError(f"既存の版別成果物は上書きしません: {out}")
    if sys.platform != "darwin":
        raise RuntimeError("全Macセットアップの署名・構造確認にはmacOSが必要です")
    local = ROOT / ".tools/go/bin/go"
    go = args.go or (str(local) if local.is_file() else shutil.which("go"))
    if not go:
        raise RuntimeError("Go 1.26.9が必要です")
    go = shutil.which(go) or str(Path(go).expanduser().resolve())
    env = dict(os.environ, GOTOOLCHAIN="local", GOCACHE=str(ROOT / ".cache/build"), GOMODCACHE=str(ROOT / ".cache/mod"))
    if subprocess.check_output([go, "env", "GOVERSION"], env=env, text=True).strip() != "go1.26.9":
        raise RuntimeError("Go 1.26.9が必要です")
    # GUIとraw CLIを別のソース状態で作らない。ビルド前後のtreeとclean状態も確認する。
    candidate = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    if subprocess.check_output(["git", "status", "--porcelain"], cwd=ROOT, text=True):
        raise RuntimeError("候補をコミットしてcleanにしてから配布物を作成してください")
    run([sys.executable, ROOT / "scripts/build-installers.py", "--go", go], env)
    for arch in ("arm64", "amd64"):
        target = ROOT / "dist" / ("csql-linux-" + arch)
        no_links(target)
        run([go, "build", "-trimpath", "-buildvcs=false", "-o", target, "."], dict(env, CGO_ENABLED="0", GOOS="linux", GOARCH=arch))
        data = target.read_bytes()
        if data[:6] != b"\x7fELF\x02\x01" or struct.unpack_from("<H", data, 18)[0] != {"arm64": 183, "amd64": 62}[arch]:
            raise RuntimeError("Linux CLIのOS/CPU形式が不一致です")
    if subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip() != candidate or subprocess.check_output(["git", "status", "--porcelain"], cwd=ROOT, text=True):
        raise RuntimeError("ビルド中に候補が変更されました")
    out.mkdir(parents=True, exist_ok=False)
    for name in ("LICENSE", "THIRD_PARTY_NOTICES.md"):
        shutil.copy2(ROOT / name, out / name)
    readme = f"""csql v{version} 配布案内

Mac Apple Silicon: csql-setup-{version}-macos-arm64.zip
Mac Intel: csql-setup-{version}-macos-amd64.zip
Windows x64: csql-setup-{version}-windows-amd64.zip
ZIPを展開して.app/.exeを開き、内容を確認してセットアップしてください。
macOSは12.0以降です。Goのインストールは不要です。

更新と接続
搭載版以降は csql update、または起動直後の一覧でF2から「csqlを更新」を選べます。Ctrl+Kの更新操作も利用できます。
v0.6.0以前からの初回移行だけはセットアップで更新してください。
画面では確認後、取得・ハッシュ/版の検証完了から自画面の接続を停止し、更新して一覧を開き直します。
DBへの再接続は手動です。別端末の接続は停止しません。
Escは準備を取消、Ctrl+Cは自画面の接続を停止して終了します。
配置失敗では旧版/管理記録を復元します。復元できない場合は表示されたバックアップ絶対パスと公式setupによる復旧手順を確認してください。
既存の接続設定・ポート・認証・Googleツール・PATH設定を保持します。

PC間の設定移行（両PCともv0.8.0以降が必要です）
旧PCのCtrl+Kから「移行ファイルを書き出す」、新PCで「移行ファイルを取り込む」を選べます。
CLIは csql migrate export / csql migrate import FILE --dry-run / csql migrate import FILE です。
保存済み設定だけを追加し、同一はスキップ・競合は全体を拒否します。既存設定は取込前にバックアップします。
Google認証・PATH・ツール・稼働セッション・DBブラウザ登録は移しません。新PCでdoctor/setupとDBツール向け再出力を行ってください。
移行JSONは暗号化せずプロジェクト・DBユーザー名を含みます。実際の保存先・対象件数を確認して受け渡してください。

通常CLIは csql-{version}-<OS>-<CPU>（Windowsだけ.exe）です。
通常CLIは同じ候補のinstaller内部payloadと一致しています。新規導入ではセットアップがPATHと導入確認を案内します。
SHA256SUMS.txtにはZIP3種と通常CLI5種のSHA-256を記載しています。
DBツールへはCtrl+EからpgAdmin・DBeaver・A5・接続URIを出力できます。パスワードは含まないためDBツール側で管理してください。
保存済み/接続一覧のCtrl+K →「ツールへ直接登録」から登録先を確認して追加できます。
CLI例: csql profile register dev --tool pgadmin --dry-run
実行は対話確認後です。pgAdminは9.18/schema52、DBeaver Communityは26.2.3の形式に対応します。
DBeaverはGeneral/.dbeaverに専用JSONを追加し、既存ファイルと暗号化資格情報は編集/復号しません。
ファイル保存の完了と実GUIでの読込み確認は別です。次回起動後に一覧で確認してください。
A5はWindows amd64通常モードが対象で、未知の保存形式や空の保存先では実行前に拒否します。
Windows実機3ツール・A5の値形式と/Exit・DBeaver GUI読込み/再起動保持は未確認です。
同名異内容は上書きせず、結果未確認の要求は自動再送しません。保存先と元要求の指紋を照合します。
バックアップに復元手順.txtを残します。原本の資格情報を含み得るため、バックアップは共有しないでください。

Macはアドホック署名です。Developer ID署名/公証とWindows配布用証明書署名は未実施です。
実DB接続・DBツールGUI取込・Windows実機の更新/セットアップ操作の確認状況はRelease本文をご確認ください。
配布時はLICENSE（Copyright 2026 PAL）とTHIRD_PARTY_NOTICES.mdを保持してください。
"""
    (out / "README.txt").write_text(readme, encoding="utf-8")
    raw = []
    for target in TARGETS:
        suffix = ".exe" if target.startswith("windows") else ""
        src = ROOT / "dist" / ("csql-" + target + suffix)
        no_links(src)
        dest = out / (f"csql-{version}-{target}" + suffix)
        shutil.copy2(src, dest)
        raw.append(dest)
        if not target.startswith("linux"):
            manifest = ROOT / "dist" / ("csql-setup-" + target + "-manifest.json")
            m = json.loads(manifest.read_text())
            goos, arch = target.split("-")
            if m != dict(kind="csql-cli", version=version, os=goos, arch=arch, sha256=digest(dest)):
                raise RuntimeError("通常CLIと同梱manifestが一致しません")
            if goos == "darwin":
                bundle = ROOT / "dist" / ("csql-setup-" + target + ".app")
                no_links(bundle)
                with (bundle / "Contents/Info.plist").open("rb") as handle:
                    if plistlib.load(handle)["CFBundleVersion"] != version:
                        raise RuntimeError("Macの版が不一致です")
                run(["/usr/bin/codesign", "--verify", "--deep", "--strict", bundle], env)
                gui = bundle / "Contents/MacOS/csql-setup"
            else:
                gui = ROOT / "dist/csql-setup-windows-amd64.exe"
            contents = gui.read_bytes()
            if manifest.read_bytes() not in contents or dest.read_bytes() not in contents:
                raise RuntimeError("installerの同梱CLI/manifestが一致しません")
    archives = []
    for target in TARGETS[:3]:
        goos, arch = target.split("-")
        label = "macos" if goos == "darwin" else "windows"
        archive = out / f"csql-setup-{version}-{label}-{arch}.zip"
        with zipfile.ZipFile(archive, "x", zipfile.ZIP_DEFLATED) as z:
            if goos == "darwin":
                bundle = ROOT / "dist" / ("csql-setup-" + target + ".app")
                for path in sorted(bundle.rglob("*")):
                    no_links(path)
                    z.write(path, path.relative_to(ROOT / "dist"))
            else:
                z.write(ROOT / "dist/csql-setup-windows-amd64.exe", "csql-setup-windows-amd64.exe")
            for name in ("README.txt", "LICENSE", "THIRD_PARTY_NOTICES.md"):
                z.write(out / name, name)
        with zipfile.ZipFile(archive) as z:
            if z.testzip() is not None:
                raise RuntimeError("ZIPが破損しています")
            for info in z.infolist():
                if info.filename.startswith("/") or ".." in Path(info.filename).parts:
                    raise RuntimeError("ZIPのパスが不正です")
                if info.filename.endswith("/csql-setup") and not (info.external_attr >> 16) & 0o111:
                    raise RuntimeError("Macセットアップの実行権限がありません")
            for name in ("README.txt", "LICENSE", "THIRD_PARTY_NOTICES.md"):
                if z.read(name) != (out / name).read_bytes():
                    raise RuntimeError("案内・ライセンスが不一致です")
        archives.append(archive)
    assets = sorted(raw + archives)
    (out / "SHA256SUMS.txt").write_text("".join(f"{digest(p)}  {p.name}\n" for p in assets), encoding="utf-8")
    assets += [out / "README.txt", out / "SHA256SUMS.txt"]
    receipt = dict(candidate=candidate, version=version, assets={p.name:dict(size=p.stat().st_size, sha256=digest(p)) for p in assets})
    (out / "release-assets.json").write_text(json.dumps(receipt, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    shutil.copy2(ROOT / "dist/csql-darwin-arm64", ROOT / "dist/csql")
    print(f"配布物10件の準備・同梱CLI/manifest・署名・ZIP・SHA256照合: PASS\n{out}")


if __name__ == "__main__":
    main()
