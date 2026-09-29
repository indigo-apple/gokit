#!/usr/bin/env bash
# scripts/cover-gate.sh —— gokit 覆盖率门禁
#
# 两级门槛：
#   1. 总覆盖率必须 >= TOTAL_MIN（默认 85）
#   2. 每个「有语句的」包必须 >= PKG_MIN（默认 75），防止新增包无测试
#
# 用法：
#   GO=/opt/go_1.21/go/bin/go scripts/cover-gate.sh
#   TOTAL_MIN=90 PKG_MIN=80 scripts/cover-gate.sh
#
# 退出码：0 通过；1 未达标；2 环境错误。

set -euo pipefail

GO="${GO:-go}"
TOTAL_MIN="${TOTAL_MIN:-85}"
PKG_MIN="${PKG_MIN:-75}"
PROFILE="${PROFILE:-cover.out}"

cd "$(dirname "$0")/.."

echo "==> 采集覆盖率（$( "$GO" version | awk '{print $3}' )）"
"$GO" test -count=1 -coverprofile="$PROFILE" -covermode=atomic ./... > /tmp/gokit_cover_raw.txt 2>&1 || {
  echo "!! 测试失败，先修测试："
  cat /tmp/gokit_cover_raw.txt
  exit 2
}

fail=0

# ── 1) 总分 ──
total=$("$GO" tool cover -func="$PROFILE" | tail -1 | awk '{print $3}' | tr -d '%')
echo "==> 总覆盖率: ${total}%（门槛 ${TOTAL_MIN}%）"
awk -v t="$total" -v min="$TOTAL_MIN" 'BEGIN{ if (t+0 < min+0) { printf "!! 总覆盖率 %.1f%% 低于门槛 %s%%\n", t, min; exit 1 } }' || fail=1

# ── 2) 分包 ──
echo "==> 分包覆盖率（门槛 ${PKG_MIN}%）"
while read -r pkg cov; do
  [ -z "${cov:-}" ] && continue
  status="ok"
  if awk -v c="$cov" -v min="$PKG_MIN" 'BEGIN{ exit !(c+0 < min+0) }'; then
    status="!! LOW"
    fail=1
  fi
  printf '    %-58s %6s%%  %s\n' "$pkg" "$cov" "$status"
done < <(
  "$GO" test -count=1 -cover ./... 2>&1 \
    | awk '/coverage: [0-9.]+% of statements/ { print $2, $5 }' \
    | sed 's/%$//' \
    | sort
)

if [ "$fail" -ne 0 ]; then
  echo "==> 覆盖率门禁未通过"
  exit 1
fi
echo "==> 覆盖率门禁通过"
