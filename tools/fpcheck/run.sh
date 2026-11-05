#!/bin/zsh
# 用法: run.sh <Chromium.app> <标签> [额外参数...]
# 无头模式打开本机检测页，输出采集到的指纹 JSON。
APP=$1; TAG=$2; shift 2
BIN="$APP/Contents/MacOS/Chromium"
DIR=$(mktemp -d)
# 注意：--no-sandbox 只因本工具运行环境限制了 macOS 沙箱，仅用于本验证脚本；正式启动器不加。
"$BIN" --headless=new --no-sandbox --user-data-dir="$DIR" --no-first-run \
  --virtual-time-budget=5000 --dump-dom "$@" "http://127.0.0.1:9000/index.html" 2>/dev/null \
  | sed -n 's/.*FPJSON\(.*\)FPEND.*/\1/p' > "/tmp/fp-$TAG.json"
rm -rf "$DIR"
echo "== $TAG"; cat "/tmp/fp-$TAG.json" | python3 -m json.tool --no-ensure-ascii 2>/dev/null || echo "(无输出)"
