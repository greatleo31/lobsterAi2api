#!/usr/bin/env bash
# checkin.sh — LobsterAI 每日签到
#
# 用法:
#   ./checkin.sh
# cron 示例（需已 export LB2A_UPSTREAM_BASE）:
#   5 9,21 * * * cd /path/to/lobsterai2api && ./checkin.sh >> data/checkin.log 2>&1
set -euo pipefail

cd "$(dirname "$0")"

CHECKIN_BIN="./checkin"
if [[ ! -x "$CHECKIN_BIN" ]]; then
    go build -o "$CHECKIN_BIN" ./cmd/checkin
fi

"$CHECKIN_BIN"
