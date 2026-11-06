#!/bin/bash
# 阶段 5 安全自检：确认浏览器在正式参数下（不带 --no-sandbox）各子进程都处在 macOS 沙箱里。
#   - 不走代理、不访问任何网站（只打开 about:blank），不需要管理员密码；
#   - 会弹出一个空白浏览器窗口约 8 秒，然后自动关闭；
#   - 用系统函数 sandbox_check 逐个检查渲染进程、GPU 进程、网络服务进程等是否在沙箱里。
# 用法：scripts/sandbox-check.sh [Chromium.app 路径]
#   不给路径时依次找：游龙.app 内置浏览器、<程序目录>/.toolchain/fpchromium-148/Chromium.app
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
APP="${1:-}"
if [ -z "$APP" ]; then
  for p in "/Applications/游龙.app/Contents/Resources/Chromium.app" \
           "$ROOT/游龙.app/Contents/Resources/Chromium.app" \
           "$ROOT/.toolchain/fpchromium-148/Chromium.app"; do
    [ -d "$p" ] && { APP="$p"; break; }
  done
fi
[ -n "$APP" ] && [ -d "$APP" ] || { echo "错误: 找不到 Chromium.app，请把路径作为参数传入" >&2; exit 1; }
BIN="$APP/Contents/MacOS/Chromium"

DIR="$(mktemp -d -t envbrowser-sandbox)"
JS="$DIR/check.js"
cat > "$JS" <<'EOF'
ObjC.import('stdlib');
ObjC.bindFunction('sandbox_check', ['int', ['int', 'void*', 'int']]);
function run(argv) { return argv.map(p => p + ' ' + $.sandbox_check(parseInt(p), null, 0)).join('\n'); }
EOF
cleanup() { [ -n "${PID:-}" ] && kill "$PID" 2>/dev/null || true; sleep 1; rm -rf "$DIR"; }
trap cleanup EXIT

echo "== 游龙 沙箱自检 =="
echo "浏览器：$APP"
# 与正式启动一致的安全相关参数；不加 --no-sandbox
"$BIN" --user-data-dir="$DIR/ud" --no-first-run --no-default-browser-check \
  --disable-background-networking --disable-component-update --use-mock-keychain \
  --password-store=basic about:blank >"$DIR/chromium.log" 2>&1 &
PID=$!
sleep 8

# 列出本次浏览器的全部进程：PID 与进程类型（写到文件，避免 bash 3.2 解析 $( ) 里的 case 出错）
kind_of() {
  local rest="$1" t s
  t=$(echo "$rest" | grep -o -- '--type=[a-z-]*' | head -1 | cut -d= -f2 || true)
  s=$(echo "$rest" | grep -o -- '--utility-sub-type=[A-Za-z.]*' | head -1 | cut -d= -f2 || true)
  if [ -z "$t" ]; then echo "主进程"
  elif [ "$t" = "utility" ] && [ "$s" = "network.mojom.NetworkService" ]; then echo "网络服务进程"
  elif [ "$t" = "utility" ]; then echo "utility:${s##*.}"
  else echo "$t"; fi
}
LIST="$DIR/procs.txt"
: > "$LIST"
pgrep -lf "user-data-dir=$DIR/ud" > "$DIR/pgrep.txt" || true
while read -r pid rest; do
  [ -n "$pid" ] && echo "$pid $(kind_of "$rest")" >> "$LIST"
done < "$DIR/pgrep.txt"

bad=0; n=0
while read -r pid kind; do
  [ -z "$pid" ] && continue
  sb=$(osascript -l JavaScript "$JS" "$pid" | awk '{print $2}')
  n=$((n+1))
  if [ "$sb" = "1" ]; then st="在沙箱里"; else st="不在沙箱里"; fi
  # 浏览器主进程本身不在沙箱里，这是 Chromium 的正常设计；其余子进程必须在沙箱里
  if [ "$kind" != "主进程" ] && [ "$sb" != "1" ]; then mark="未通过"; bad=$((bad+1)); else mark="通过"; fi
  printf '  [%s] PID %-6s %-22s %s\n' "$mark" "$pid" "$kind" "$st"
done < "$LIST"

if grep -q -- "--no-sandbox" "$DIR/pgrep.txt"; then
  echo "  [未通过] 进程参数里出现了 --no-sandbox"; bad=$((bad+1))
fi
if grep -q "Failed to initialize sandbox\|sandbox initialization failed" "$DIR/chromium.log"; then
  echo "  [未通过] 浏览器日志报沙箱初始化失败"; bad=$((bad+1))
fi
[ "$n" -ge 3 ] || { echo "  [未通过] 只找到 $n 个浏览器进程，浏览器可能没正常启动"; bad=$((bad+1)); }

echo
if [ "$bad" = 0 ]; then echo "==== 沙箱自检通过（检查 $n 个进程）===="; else echo "==== 沙箱自检未通过 $bad 项 ===="; exit 1; fi
