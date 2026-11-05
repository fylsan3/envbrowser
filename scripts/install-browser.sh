#!/bin/bash
# 下载并安装 fingerprint-chromium（游龙使用的浏览器底座），按 SHA-256 校验。
# 用法：scripts/install-browser.sh          安装到 <仓库>/.toolchain/fpchromium-148/Chromium.app
#       scripts/install-browser.sh --check  只校验已安装的版本
# 版本、地址、校验值与 docs/需求规格-v2.html 第 8.0 节保持一致；升级时三处一起改。
set -euo pipefail

VERSION="148.0.7778.215"
URL="https://github.com/adryfish/fingerprint-chromium/releases/download/${VERSION}/ungoogled-chromium_${VERSION}-1.1_macos.dmg"
SHA256="b72f091e2e1a7583eed389c4b8e3534ed355e568af8c8bbf8fc30a25e23ca679"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TC="$ROOT/.toolchain"
DMG="$TC/fpchromium-148.dmg"
DEST="$TC/fpchromium-148"
APP="$DEST/Chromium.app"

die() { echo "错误: $*" >&2; exit 1; }

check_installed() {
  [ -d "$APP" ] || die "未安装: $APP"
  local v arch
  v=$(/usr/libexec/PlistBuddy -c "Print :CFBundleShortVersionString" "$APP/Contents/Info.plist")
  arch=$(lipo -archs "$APP/Contents/MacOS/Chromium")
  [ "$v" = "$VERSION" ] || die "已安装版本 ${v}，需要 ${VERSION}"
  [[ "$arch" == *arm64* ]] || die "架构为 ${arch}，需要 arm64"
  echo "已安装 fingerprint-chromium $v ($arch): $APP"
}

[ "$(uname -s)" = "Darwin" ] || die "只支持 macOS"
[ "$(uname -m)" = "arm64" ] || die "只支持 Apple 芯片（arm64）"

if [ "${1:-}" = "--check" ]; then check_installed; exit 0; fi

if [ -d "$APP" ]; then
  check_installed
  echo "如需重装，请先删除 $DEST"
  exit 0
fi

mkdir -p "$TC"
if [ -f "$DMG" ] && [ "$(shasum -a 256 "$DMG" | awk '{print $1}')" = "$SHA256" ]; then
  echo "使用已下载的安装包 $DMG"
else
  echo "下载 $URL"
  curl -fL --retry 3 -o "$DMG.part" "$URL"
  mv "$DMG.part" "$DMG"
fi

got=$(shasum -a 256 "$DMG" | awk '{print $1}')
if [ "$got" != "$SHA256" ]; then
  rm -f "$DMG"
  die "SHA-256 不一致，已删除安装包。期望 ${SHA256}，实际 ${got}"
fi
echo "SHA-256 校验通过"

MNT=$(mktemp -d /tmp/fpchromium.XXXXXX)
trap 'hdiutil detach -quiet "$MNT" 2>/dev/null || true; rmdir "$MNT" 2>/dev/null || true' EXIT
hdiutil attach -quiet -nobrowse -readonly -mountpoint "$MNT" "$DMG"
SRC=$(find "$MNT" -maxdepth 1 -name "*.app" | head -1)
[ -n "$SRC" ] || die "安装包里没有 .app"
mkdir -p "$DEST"
ditto "$SRC" "$APP"
# 去掉下载隔离标记，避免首次启动被 Gatekeeper 拦截（作者未做 Apple 公证）
xattr -dr com.apple.quarantine "$APP" 2>/dev/null || true

check_installed
