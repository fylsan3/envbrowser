#!/bin/bash
# 打包 游龙.app（阶段 5）：程序 + 内置 fingerprint-chromium，本机临时签名（ad-hoc）。
#   游龙.app/Contents/MacOS/envbrowser        主程序（双击即进入管理页面，后台运行、不显示 Dock 图标）
#   游龙.app/Contents/Resources/Chromium.app  浏览器（安装前按 SHA-256 校验过的 148.0.7778.215）
# 用法：scripts/build-app.sh <版本号> <输出目录>
# 不含任何环境数据；环境数据仍在 ~/BrowserEnvs。
set -euo pipefail

TAG="${1:?用法: scripts/build-app.sh <版本号> <输出目录>}"
OUTDIR="${2:?用法: scripts/build-app.sh <版本号> <输出目录>}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

SRC_CHROMIUM="$ROOT/.toolchain/fpchromium-148/Chromium.app"
scripts/install-browser.sh --check >/dev/null || { echo "错误: 浏览器未安装或版本不对，先运行 scripts/install-browser.sh" >&2; exit 1; }

export GOTOOLCHAIN=local
[ -x .toolchain/go/bin/go ] && export PATH="$ROOT/.toolchain/go/bin:$PATH"

APP="$OUTDIR/游龙.app"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X main.version=$TAG" -o "$APP/Contents/MacOS/envbrowser" ./cmd/envbrowser

# ditto 保留浏览器包内的符号链接和原有签名
ditto --norsrc --noextattr "$SRC_CHROMIUM" "$APP/Contents/Resources/Chromium.app"

VER="${TAG#v}"
cat > "$APP/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>游龙</string>
  <key>CFBundleDisplayName</key><string>游龙</string>
  <key>CFBundleIdentifier</key><string>io.github.fylsan3.envbrowser</string>
  <key>CFBundleExecutable</key><string>envbrowser</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$VER</string>
  <key>CFBundleVersion</key><string>$VER</string>
  <key>LSMinimumSystemVersion</key><string>12.0</string>
  <key>LSArchitecturePriority</key><array><string>arm64</string></array>
  <key>LSUIElement</key><true/>
</dict>
</plist>
EOF
plutil -lint "$APP/Contents/Info.plist" >/dev/null

# 本机临时签名：只签外层，内置浏览器保留它自己的签名（不用 --deep，避免改动浏览器的签名）
codesign --force --sign - --identifier io.github.fylsan3.envbrowser "$APP/Contents/MacOS/envbrowser"
codesign --force --sign - "$APP"
codesign --verify --strict "$APP"
codesign --verify --deep --strict "$APP/Contents/Resources/Chromium.app"
echo "已生成 ${APP}（$(du -sh "$APP" | cut -f1)）"
