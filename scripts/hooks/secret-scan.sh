#!/bin/bash
# 铁律检查：真实代理 IP、端口、用户名、密码不得进入仓库。
#
# 两类检查：
#   1. 通用规则：新增内容里出现的 IPv4 地址，只允许本机、内网、文档示例段
#      （127/8、0.0.0.0、10/8、172.16/12、192.168/16、169.254/16、
#        192.0.2/24、198.51.100/24、203.0.113/24），其他一律拦截。
#   2. 禁用词清单：.secrets/forbidden.txt（已被 .gitignore 排除，不进仓库），
#      每行一个字面量（代理 IP、IP:端口、用户名、密码），# 开头为注释。
#
# 用法：
#   secret-scan.sh staged          检查暂存区新增内容（pre-commit 调用）
#   secret-scan.sh range A..B      检查一段提交的新增内容（pre-push 调用）
#   secret-scan.sh history         用禁用词清单扫描全部历史
set -u
ROOT=$(git rev-parse --show-toplevel) || exit 2
LIST="$ROOT/.secrets/forbidden.txt"
fail=0

added_lines() {   # 从 diff 中取出新增行（去掉 +++ 文件头）
  grep -E '^\+' | grep -vE '^\+\+\+ ' | cut -c2-
}

check_ips() {     # stdin: 文本；输出不在允许范围内的 IPv4
  grep -oE '(^|[^0-9.])([0-9]{1,3}\.){3}[0-9]{1,3}([^0-9.]|$)' \
  | grep -oE '([0-9]{1,3}\.){3}[0-9]{1,3}' \
  | awk -F. '
    { for (i=1;i<=4;i++) if ($i>255) next
      a=$1; b=$2; c=$3
      if (a==127 || a==10 || $0=="0.0.0.0") next
      if (a==172 && b>=16 && b<=31) next
      if (a==192 && b==168) next
      if (a==169 && b==254) next
      if (a==192 && b==0   && c==2)   next
      if (a==198 && b==51  && c==100) next
      if (a==203 && b==0   && c==113) next
      print }' | sort -u
}

check_list() {    # stdin: 文本；输出命中的禁用词序号（不回显内容本身）
  [ -f "$LIST" ] || return 0
  local text n=0 w
  text=$(cat)
  while IFS= read -r w || [ -n "$w" ]; do
    w=${w%$'\r'}
    case "$w" in ''|'#'*) continue ;; esac
    n=$((n+1))
    if printf '%s' "$text" | grep -qF -- "$w"; then
      echo "禁用词清单第 $n 项（${w:0:2}***）"
    fi
  done < "$LIST"
}

report() {        # $1 标题  $2 命中内容
  if [ -n "$2" ]; then
    echo "✗ $1：" >&2
    printf '%s\n' "$2" | sed 's/^/    /' >&2
    fail=1
  fi
}

[ -f "$LIST" ] || echo "⚠ 未找到 .secrets/forbidden.txt，只做通用 IP 检查（建议补上，见 README）" >&2

case "${1:-staged}" in
  staged)
    diff=$(git diff --cached -U0 --no-color | added_lines)
    report "新增内容含非示例 IP 地址" "$(printf '%s' "$diff" | check_ips)"
    report "新增内容命中禁用词" "$(printf '%s' "$diff" | check_list)"
    ;;
  range)
    # $2 不加引号：pre-push 可能传入 "<sha> --not --remotes" 这样的多个参数
    # shellcheck disable=SC2086
    diff=$(git log -p --no-color $2 | added_lines)
    report "待推送提交含非示例 IP 地址" "$(printf '%s' "$diff" | check_ips)"
    report "待推送提交命中禁用词" "$(printf '%s' "$diff" | check_list)"
    ;;
  history)
    report "历史提交命中禁用词" "$(git log -p --all --no-color | check_list)"
    ;;
  *) echo "用法: $0 staged | range A..B | history" >&2; exit 2 ;;
esac

if [ $fail -ne 0 ]; then
  echo "已拦截。请把真实代理信息换成 x.x.x.x / 代理用户名 / 代理密码 等占位符后再提交。" >&2
  exit 1
fi
exit 0
