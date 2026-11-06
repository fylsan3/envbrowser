#!/bin/bash
# 打包发布用的程序，产物在 dist/（已 gitignore），都不含任何环境数据：
#   1. envbrowser-版本-macos-arm64.zip       命令行版：程序 + README + 需求文档 + 脚本（不含浏览器）
#   2. 游龙-版本-macos-arm64.zip             游龙.app（内置浏览器，本机临时签名）+ 安全自检脚本
# 两个 zip 都算 SHA-256。
# 用法：scripts/release.sh v0.2.0
# 发布到 GitHub：gh release create v0.2.0 dist/envbrowser-v0.2.0-macos-arm64.zip --notes-file ...
set -euo pipefail

TAG="${1:?用法: scripts/release.sh <版本号，如 v0.2.0>}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

[ -z "$(git status --porcelain)" ] || { echo "错误: 工作区有未提交改动，先提交再打包" >&2; exit 1; }
scripts/hooks/secret-scan.sh history

export GOTOOLCHAIN=local
[ -x .toolchain/go/bin/go ] && export PATH="$ROOT/.toolchain/go/bin:$PATH"

go test ./cmd/... ./internal/... ./templates/...

NAME="envbrowser-${TAG}-macos-arm64"
OUT="dist/$NAME"
rm -rf "$OUT" "dist/$NAME.zip"
mkdir -p "$OUT/bin" "$OUT/scripts" "$OUT/docs"
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X main.version=$TAG" -o "$OUT/bin/envbrowser" ./cmd/envbrowser
cp README.md "$OUT/"
cp docs/需求规格-v2.html "$OUT/docs/"
cp scripts/install-browser.sh scripts/field-test.sh scripts/sandbox-check.sh "$OUT/scripts/"

# --norsrc --noextattr：不把 macOS 扩展属性打成 ._ 文件
(cd dist && ditto -c -k --norsrc --noextattr --noacl --keepParent "$NAME" "$NAME.zip")
shasum -a 256 "dist/$NAME.zip" | tee "dist/$NAME.zip.sha256"
echo "已生成 dist/$NAME.zip"

# 游龙.app
APPNAME="游龙-${TAG}-macos-arm64"
APPOUT="dist/$APPNAME"
rm -rf "$APPOUT" "dist/$APPNAME.zip"
mkdir -p "$APPOUT/scripts"
scripts/build-app.sh "$TAG" "$APPOUT"
cp scripts/sandbox-check.sh "$APPOUT/scripts/"
cp README.md "$APPOUT/"
(cd dist && ditto -c -k --norsrc --noextattr --noacl --keepParent "$APPNAME" "$APPNAME.zip")
shasum -a 256 "dist/$APPNAME.zip" | tee "dist/$APPNAME.zip.sha256"
echo "已生成 dist/$APPNAME.zip"
