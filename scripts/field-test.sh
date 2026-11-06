#!/bin/bash
# 真实代理实测（需求规格 12 节阶段 1.5b + 12.1 节），在装好游龙的 Mac 上运行一次即可。
#   - 代理用隐藏输入读入，不显示、不进命令历史、不出现在进程列表；
#   - 过程中会要一次开机密码（sudo），只用于抓包：本机 DNS 查询，以及浏览器进程有没有绕过代理直接往外发包；
#   - 会依次打开两个浏览器窗口，每个约 1 分钟，期间不要操作窗口；
#   - 结束后生成 field-report.txt（代理地址、用户名、密码、出口 IP、本机公网 IP 已打码），把它发回即可。
# 用法：scripts/field-test.sh            （在程序目录里运行）
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
BIN="$ROOT/bin/envbrowser"
OUT="$ROOT/field-report.txt"
# 浏览器在实测中会访问的外部站点（与 cmd/envbrowser/fieldtest.go 的 fieldSites 一致）
SITES=(example.com neverssl.com)

die() { echo "错误: $*" >&2; exit 1; }

[ "$(uname -s)" = "Darwin" ] || die "只支持 macOS"
[ "$(uname -m)" = "arm64" ] || die "只支持 Apple 芯片（arm64）"
[ -x "$BIN" ] || die "找不到 $BIN，请在解压后的程序目录里运行"
"$ROOT/scripts/install-browser.sh" --check >/dev/null || die "浏览器未安装，先运行 scripts/install-browser.sh"

echo "== 游龙 真实代理实测 =="
echo "格式：socks5h://用户名:密码@IP:端口 或 http://用户名:密码@IP:端口"
read -r -s -p "请粘贴代理（输入不会显示）: " PROXY
echo
[ -n "$PROXY" ] || die "没有输入代理"
case "$PROXY" in socks5h://*|http://*) ;; *) die "代理需以 socks5h:// 或 http:// 开头" ;; esac

echo "需要开机密码，用于抓包（DNS 查询、浏览器进程外发报文，结束即停）："
sudo -v || die "未获得管理员权限"

WORK="$(mktemp -d -t envbrowser-dns)"
DNSLOG="$WORK/dns.log"
cleanup() {
  [ -n "${KEEP:-}" ] && kill "$KEEP" 2>/dev/null || true
  [ -n "${TCPD:-}" ] && sudo -n kill "$TCPD" 2>/dev/null || true
  [ -n "${TCPO:-}" ] && sudo -n kill "$TCPO" 2>/dev/null || true
  rm -rf "$WORK"
  unset PROXY
}
trap cleanup EXIT

# 保持 sudo 授权不过期（实测约 3 分钟）
( while sudo -n true 2>/dev/null; do sleep 50; done ) &
KEEP=$!

# 抓所有网卡（含 VPN）；any 不可用时换 macOS 的 pktap
TCPD=
for IFACE in any pktap; do
  sudo -n tcpdump -i "$IFACE" -n -l 'port 53 or port 5353 or port 853' >"$DNSLOG" 2>"$WORK/tcpdump.err" &
  P=$!
  sleep 2
  # sudo 进程属 root，普通用户 kill -0 会被拒，用 ps 判断是否还在
  if ps -p "$P" >/dev/null 2>&1; then TCPD=$P; break; fi
done
[ -n "$TCPD" ] || echo "警告: DNS 监控没能启动（$(head -1 "$WORK/tcpdump.err")），继续其余检查" >&2

# 浏览器进程发往本机以外的报文（12.1 节第 3 步的最终判定）。macOS 的 pktap 抓包带进程名；
# 不带 lo0，所以浏览器连本机中转的流量不在其中。结束后只统计进程名含 Chromium 的报文，不保存其他软件的记录。
OUTLOG="$WORK/out.log"
TCPO=
# 依次尝试：只抓发出方向 + 排除发往本机；不支持时逐步放宽（事后统计时仍会剔除发往本机的报文）
for OPT in "-Q out" ""; do
  for F in 'not (dst net 127.0.0.0/8 or dst host ::1)' ''; do
    # shellcheck disable=SC2086
    sudo -n tcpdump -i pktap $OPT -k NP -n -q -t -l $F >"$OUTLOG" 2>"$WORK/tcpo.err" &
    P=$!
    sleep 2
    if ps -p "$P" >/dev/null 2>&1; then TCPO=$P; break 2; fi
  done
done
[ -n "$TCPO" ] || echo "警告: 外发抓包没能启动（$(head -1 "$WORK/tcpo.err")），继续其余检查" >&2

RC=0
printf '%s\n' "$PROXY" | "$BIN" fieldtest -o "$OUT" || RC=$?
unset PROXY

# DNS 结论追加到报告：只写站点是否出现，不写抓到的其他查询（可能含你本机其他软件的访问记录）
{
  echo
  echo "【③】本机 DNS 查询（12.1 节第 4 步）"
  if [ -z "${TCPD:-}" ]; then
    echo "  → [未通过] DNS 监控未启动，无法判断"
    RC=1
  else
    sleep 1
    hit=()
    for s in "${SITES[@]}"; do
      grep -qiE "[? ]${s//./\\.}\.? " "$DNSLOG" && hit+=("$s")
    done
    n=$(wc -l <"$DNSLOG" | tr -d ' ')
    if [ ${#hit[@]} -eq 0 ]; then
      echo "  → [通过] 浏览器访问的站点（${SITES[*]}）没有出现在本机 DNS 查询中（期间共抓到 $n 条 DNS 报文，属系统和其他软件）"
    else
      echo "  → [未通过] 本机 DNS 查询中出现了：${hit[*]}（说明浏览器绕过代理自己解析了域名）"
      RC=1
    fi
  fi

  echo
  echo "【③】浏览器进程外发报文抓包（最终判定）"
  if [ -z "${TCPO:-}" ]; then
    echo "  → [未通过] 外发抓包未启动，无法判断"
    RC=1
  else
    sudo -n kill "$TCPO" 2>/dev/null || true
    sleep 1
    # 只看进程名，不写目标地址
    leak=$(grep -i 'chromium' "$OUTLOG" | grep -vE '> (127\.[0-9.]+|::1)[.:][0-9]+' || true)
    n=$(wc -l <"$OUTLOG" | tr -d ' ')
    if ! grep -qi 'envbrowser' "$OUTLOG"; then
      # 游龙主程序实测期间一直在连代理，抓不到它说明这台机器的抓包不带进程名，不能据此判通过
      echo "  → [未通过] 抓包里没有进程名（连游龙主程序连代理的报文都认不出），无法判断；共 $n 行"
      RC=1
    elif [ -z "$leak" ]; then
      echo "  → [通过] 浏览器进程没有向本机以外发出任何报文（期间共抓到 $n 条其他程序的外发报文，含游龙连代理）"
    else
      c=$(printf '%s\n' "$leak" | wc -l | tr -d ' ')
      kinds=$(printf '%s\n' "$leak" | grep -oiE 'chromium[a-z ()]*' | sort | uniq -c | tr -s ' ' | paste -sd';' -)
      protos=$(printf '%s\n' "$leak" | grep -oiE "(UDP|tcp)[ ,]" | tr -d " ," | sort | uniq -c | tr -s ' ' | paste -sd';' -)
      echo "  → [未通过] 浏览器进程直接外发了 $c 条报文（进程：$kinds；协议：$protos）"
      RC=1
    fi
  fi
} >"$WORK/tail.txt"
cat "$WORK/tail.txt" | tee -a "$OUT"

echo
echo "完成。请把 $OUT 发回（已打码，可直接发送）。"
exit $RC
