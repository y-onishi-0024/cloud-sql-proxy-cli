#!/usr/bin/env python3
"""通常CLIを同梱した、OS別のセットアップ配布物を作る。"""
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
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parents[1]
TARGETS = ["darwin/arm64", "darwin/amd64", "windows/amd64"]


def run(argv, *, cwd, env):
    subprocess.run([str(v) for v in argv], cwd=cwd, env=env, check=True)


def verify_binary(data, goos, arch, gui):
    if goos == "windows":
        if data[:2] != b"MZ":
            raise RuntimeError("Windows実行ファイルではありません")
        offset = struct.unpack_from("<I", data, 0x3C)[0]
        if data[offset:offset + 4] != b"PE\x00\x00":
            raise RuntimeError("PEヘッダーが不正です")
        machine = struct.unpack_from("<H", data, offset + 4)[0]
        subsystem = struct.unpack_from("<H", data, offset + 24 + 68)[0]
        if machine != 0x8664 or subsystem != (2 if gui else 3):
            raise RuntimeError("WindowsのCPUまたはGUI/CLI種別が不一致です")
    else:
        magic, cpu = struct.unpack_from("<II", data)
        if magic != 0xFEEDFACF or cpu != {"arm64": 0x100000C, "amd64": 0x1000007}[arch]:
            raise RuntimeError("macOSのCPUが不一致です")


def build(target, go, version):
    goos, arch = target.split("/")
    if goos == "darwin" and sys.platform != "darwin":
        raise RuntimeError("macOSセットアップのビルドにはmacOS/Xcodeが必要です")
    env = dict(os.environ, GOTOOLCHAIN="local", GOCACHE=str(ROOT / ".cache/build"),
               GOMODCACHE=str(ROOT / ".cache/mod"), GOOS=goos, GOARCH=arch, CGO_ENABLED="0")
    output = ROOT / "dist"
    output.mkdir(exist_ok=True)
    stages = ROOT / ".cache/installer-build"
    stages.mkdir(parents=True, exist_ok=True)
    base = f"csql-setup-{goos}-{arch}"
    with tempfile.TemporaryDirectory(prefix=base + "-", dir=stages) as temporary:
        stage = Path(temporary)
        # 外部コマンドはすべて引数配列で起動し、生成物はリポジトリのソースへ混ぜない。
        for source in ROOT.iterdir():
            if source.name in ("go.mod", "go.sum") or (source.suffix in (".go", ".m") and not source.name.endswith("_test.go")):
                shutil.copy2(source, stage / source.name)
        payload_dir = stage / "installer_payload"
        payload_dir.mkdir()
        shutil.copy2(ROOT / "installer_payload/README.md", payload_dir / "README.md")
        cli_name = "csql.exe" if goos == "windows" else "csql"
        cli_path = payload_dir / cli_name
        run([go, "build", "-trimpath", "-buildvcs=false", "-o", cli_path, "."], cwd=stage, env=env)
        cli_data = cli_path.read_bytes()
        verify_binary(cli_data, goos, arch, False)
        manifest = dict(kind="csql-cli", version=version, os=goos, arch=arch,
                        sha256=hashlib.sha256(cli_data).hexdigest())
        manifest_bytes = json.dumps(manifest, sort_keys=True).encode()
        (payload_dir / "manifest.json").write_bytes(manifest_bytes)
        if goos == "windows":
            executable = stage / (base + ".exe")
            run([go, "build", "-trimpath", "-buildvcs=false", "-tags", "installer",
                 "-ldflags=-H=windowsgui", "-o", executable, "."], cwd=stage, env=env)
            destination = output / executable.name
            shutil.copy2(executable, destination)
        else:
            env["CGO_ENABLED"] = "1"
            env["MACOSX_DEPLOYMENT_TARGET"] = "12.0"
            bundle = stage / (base + ".app")
            macos = bundle / "Contents/MacOS"
            macos.mkdir(parents=True)
            executable = macos / "csql-setup"
            run([go, "build", "-trimpath", "-buildvcs=false", "-tags", "installer",
                 "-o", executable, "."], cwd=stage, env=env)
            plist = dict(CFBundleExecutable="csql-setup", CFBundleIdentifier="dev.csql.setup",
                         CFBundleName="csql セットアップ", CFBundleDisplayName="csql セットアップ",
                         CFBundlePackageType="APPL", CFBundleVersion=version,
                         CFBundleShortVersionString=version, LSMinimumSystemVersion="12.0",
                         NSHighResolutionCapable=True)
            with (bundle / "Contents/Info.plist").open("wb") as handle:
                plistlib.dump(plist, handle)
            run(["/usr/bin/codesign", "--force", "--sign", "-", bundle], cwd=stage, env=env)
            run(["/usr/bin/codesign", "--verify", "--deep", "--strict", bundle], cwd=stage, env=env)
            destination = output / bundle.name
            if destination.is_symlink():
                raise RuntimeError("既存の配布先がシンボリックリンクです")
            if destination.exists():
                shutil.rmtree(destination)
            shutil.copytree(bundle, destination)
            archive = output / (base + ".zip")
            with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as zip_file:
                for item in sorted(destination.rglob("*")):
                    zip_file.write(item, item.relative_to(output))
        gui_data = executable.read_bytes()
        verify_binary(gui_data, goos, arch, True)
        if manifest_bytes not in gui_data or cli_data not in gui_data:
            raise RuntimeError("GUIの同梱CLI/manifestが一致しません")
        cli_output = output / (f"csql-{goos}-{arch}" + (".exe" if goos == "windows" else ""))
        shutil.copy2(cli_path, cli_output)
        (output / (base + "-manifest.json")).write_bytes(manifest_bytes)
        print(f"作成・構造検証済み: {destination}", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", action="append", choices=TARGETS, help="省略時は3環境すべて")
    parser.add_argument("--go", help="使用するGo実行ファイル")
    args = parser.parse_args()
    local_go = ROOT / ".tools/go/bin" / ("go.exe" if os.name == "nt" else "go")
    go = args.go or (str(local_go) if local_go.is_file() else shutil.which("go"))
    if not go:
        raise RuntimeError("Go 1.26.9が必要です")
    go = shutil.which(go) or str(Path(go).expanduser().resolve())
    tool_version = subprocess.check_output([go, "env", "GOVERSION"], text=True,
                                           env=dict(os.environ, GOTOOLCHAIN="local")).strip()
    if tool_version != "go1.26.9":
        raise RuntimeError(f"Go 1.26.9が必要です（現在: {tool_version}）")
    version = re.search(r'const version = "([^"]+)"', (ROOT / "cli.go").read_text()).group(1)
    for target in args.target or TARGETS:
        build(target, go, version)


if __name__ == "__main__":
    main()
