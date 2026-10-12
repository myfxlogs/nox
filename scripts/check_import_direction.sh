#!/usr/bin/env bash
# check_import_direction.sh —— D-514 T1：internal/* 包间 import 方向白名单门禁。
# 白名单=master@D-511-merge 后 go list 实拍边集冻结（170 边）；新增跨包 import 即 FAIL
# （要放行先评审方向合规并更新本表——P4 可检查性）。删边不拦（收敛无害）。
# 红线示例（白名单已含语义）：store/pgstore/bus/fact/keydomain 等底层包不得 import
# dashboard/exec/autotrade/adapter 上层包；逆向依赖一经引入即拦。
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

ALLOWED=$(cat <<'EOF'
internal/explorer -> internal/analyst
internal/explorer -> internal/gen/explorer/v1
internal/explorer -> internal/inboxfmt
internal/explorer -> internal/store/pgstore
internal/gen/mtapi/mt5/mt5connect -> internal/adapter/gen/mtapi/mt5
internal/adapter/gen/mtapi/mt5/mt5connect -> internal/adapter/gen/mtapi/mt5
internal/adapter -> internal/adapter/gen/mtapi/mt5
internal/adapter -> internal/bus
internal/adapter -> internal/collect
internal/adapter -> internal/dephealth
internal/adapter -> internal/fact
internal/adapter -> internal/keydomain
internal/adapter -> internal/safego
internal/adapter -> internal/venue
internal/analyst -> internal/store/pgstore
internal/autotrade -> internal/bus
internal/autotrade -> internal/capitalview
internal/autotrade -> internal/combo
internal/autotrade -> internal/econ
internal/autotrade -> internal/errclass
internal/autotrade -> internal/exec
internal/autotrade -> internal/fact
internal/autotrade -> internal/isreal
internal/autotrade -> internal/keydomain
internal/autotrade -> internal/macro
internal/autotrade -> internal/marketstate
internal/autotrade -> internal/mmm
internal/autotrade -> internal/position
internal/autotrade -> internal/safego
internal/autotrade -> internal/settlement
internal/autotrade -> internal/spread
internal/autotrade -> internal/store/pgstore
internal/autotrade -> internal/usdnotional
internal/bus -> internal/keydomain
internal/bus -> internal/safego
internal/cexscan -> internal/bus
internal/cexscan -> internal/config
internal/cexscan -> internal/fact
internal/cexscan -> internal/gen/dashboardv1
internal/cexscan -> internal/httpx
internal/cexscan -> internal/keydomain
internal/cexscan -> internal/store/pgstore
internal/cexscan -> internal/venue
internal/collect/cmc -> internal/fact
internal/collect/cmc -> internal/safego
internal/collect/cmc -> internal/store/pgstore
internal/collect/deribit -> internal/fact
internal/collect/eventwatch -> internal/fact
internal/collect/eventwatch -> internal/store/pgstore
internal/collect/exchange -> internal/bus
internal/collect/exchange -> internal/dephealth
internal/collect/exchange -> internal/fact
internal/collect/exchange -> internal/alerttext
internal/collect/exchange -> internal/httpx
internal/collect/exchange -> internal/keydomain
internal/collect/exchange -> internal/safego
internal/collect/exchange -> internal/signutil
internal/collect/exchange -> internal/venue
internal/collect/exchange -> internal/store/pgstore
internal/calib -> internal/explorer
internal/calib -> internal/inboxfmt
internal/calib -> internal/keydomain
internal/calib -> internal/store/pgstore
internal/calib -> internal/sysretro
internal/collect -> internal/bus
internal/collect -> internal/fact
internal/collect -> internal/safego
internal/collect/trx -> internal/fact
internal/collect/trx -> internal/store/pgstore
internal/capitalview -> internal/bus
internal/capitalview -> internal/keydomain
internal/capitalview -> internal/reconcile
internal/capitalview -> internal/store/pgstore
internal/dashboard -> internal/adapter
internal/dashboard -> internal/adapter/gen/mtapi/mt5
internal/dashboard -> internal/alertcool
internal/dashboard -> internal/analyst
internal/dashboard -> internal/bus
internal/dashboard -> internal/capitalview
internal/dashboard -> internal/collect/cmc
internal/dashboard -> internal/collect/exchange
internal/dashboard -> internal/combo
internal/dashboard -> internal/cryptoutil
internal/dashboard -> internal/config
internal/dashboard -> internal/dephealth
internal/dashboard -> internal/detector
internal/dashboard -> internal/econ
internal/dashboard -> internal/errclass
internal/dashboard -> internal/exec
internal/dashboard -> internal/fact
internal/dashboard -> internal/gen/dashboardv1
internal/dashboard -> internal/httpx
internal/dashboard -> internal/judge
internal/dashboard -> internal/kbreport
internal/dashboard -> internal/marketstate
internal/dashboard -> internal/keydomain
internal/dashboard -> internal/notional
internal/dashboard -> internal/position
internal/dashboard -> internal/reconcile
internal/dashboard -> internal/safego
internal/dashboard -> internal/scan
internal/dashboard -> internal/spread
internal/dashboard -> internal/store/pgstore
internal/dciexec -> internal/bus
internal/dciexec -> internal/dephealth
internal/dciexec -> internal/store/pgstore
internal/dashboard -> internal/sysretro
internal/dashboard -> internal/trxvote
internal/dashboard -> internal/usdnotional
internal/dashboard -> internal/venue
internal/dashboard -> internal/verification
internal/dashboard -> internal/explorer
internal/detector -> internal/bus
internal/detector -> internal/fact
internal/detector -> internal/isreal
internal/detector -> internal/keydomain
internal/engine -> internal/bus
internal/engine -> internal/detector
internal/engine -> internal/evaluator
internal/engine -> internal/fact
internal/engine -> internal/keydomain
internal/engine -> internal/store/pgstore
internal/errclass -> internal/keydomain
internal/evaluator -> internal/detector
internal/evaluator -> internal/keydomain
internal/evaluator -> internal/calib
internal/exec -> internal/adapter/gen/mtapi/mt5
internal/exec -> internal/bus
internal/exec -> internal/combo
internal/exec -> internal/dephealth
internal/exec -> internal/fact
internal/exec -> internal/httpx
internal/exec -> internal/keydomain
internal/exec -> internal/marketstate
internal/exec -> internal/notional
internal/exec -> internal/safego
internal/exec -> internal/signutil
internal/exec -> internal/store/pgstore
internal/gen/dashboardv1/dashboardv1connect -> internal/gen/dashboardv1
internal/judge -> internal/analyst
internal/judge -> internal/config
internal/judge -> internal/longbridge
internal/judge -> internal/store/pgstore
internal/kbreport -> internal/analyst
internal/kbreport -> internal/store/pgstore
internal/notify -> internal/analyst
internal/notify -> internal/bus
internal/notify -> internal/macro
internal/notify -> internal/safego
internal/notify -> internal/store/pgstore
internal/notional -> internal/keydomain
internal/oppmatch -> internal/dciexec
internal/oppmatch -> internal/store/pgstore
internal/position -> internal/bus
internal/position -> internal/safego
internal/position -> internal/store/pgstore
internal/position -> internal/verification
internal/reconcile -> internal/adapter
internal/reconcile -> internal/adapter/gen/mtapi/mt5
internal/reconcile -> internal/exec
internal/reconcile -> internal/gen/dashboardv1
internal/reconcile -> internal/httpx
internal/reconcile -> internal/keydomain
internal/reconcile -> internal/safego
internal/reconcile -> internal/signutil
internal/reconcile -> internal/store/pgstore
internal/reconcile -> internal/venue
internal/replay -> internal/bus
internal/replay -> internal/detector
internal/replay -> internal/evaluator
internal/replay -> internal/keydomain
internal/replay -> internal/notional
internal/replay -> internal/settlement
internal/replay -> internal/store/pgstore
internal/review -> internal/store/pgstore
internal/macro -> internal/safego
internal/macro -> internal/store/pgstore
internal/marketstate -> internal/bus
internal/marketstate -> internal/fact
internal/marketstate -> internal/keydomain
internal/marketstate -> internal/store/pgstore
internal/scan -> internal/calib
internal/scan -> internal/fact
internal/scan -> internal/keydomain
internal/scan -> internal/store/pgstore
internal/sleepmon -> internal/inboxfmt
internal/sleepmon -> internal/store/pgstore
internal/settleadapter -> internal/autotrade
internal/settleadapter -> internal/exec
internal/settleadapter -> internal/settlement
internal/settleadapter -> internal/store/pgstore
internal/settlement -> internal/keydomain
internal/settlement -> internal/mmm
internal/settlement -> internal/notional
internal/settlement -> internal/store/pgstore
internal/settlement -> internal/verification
internal/store/pgstore -> internal/alerttext
internal/store/pgstore -> internal/capital
internal/store/pgstore -> internal/errclass
internal/store/pgstore -> internal/fact
internal/store/pgstore -> internal/cryptoutil
internal/store/pgstore -> internal/keydomain
internal/store/pgstore -> internal/normalize
internal/store/pgstore -> internal/notional
internal/store/pgstore -> internal/verification
internal/swapledger -> internal/decimalutil
internal/swapledger -> internal/fact
internal/swapledger -> internal/keydomain
internal/swapledger -> internal/safego
internal/swapledger -> internal/store/pgstore
internal/symdig -> internal/analyst
internal/symdig -> internal/inboxfmt
internal/symdig -> internal/fact
internal/symdig -> internal/scan
internal/symdig -> internal/store/pgstore
internal/selfwatch -> internal/fact
internal/selfwatch -> internal/safego
internal/selfwatch -> internal/macro
internal/selfwatch -> internal/scan
internal/selfwatch -> internal/store/pgstore
internal/selfwatch -> internal/sysretro
internal/sysretro -> internal/analyst
internal/sysretro -> internal/inboxfmt
internal/sysretro -> internal/kbreport
internal/sysretro -> internal/mmm
internal/sysretro -> internal/store/pgstore
internal/testutil/storetest -> internal/store/pgstore
internal/testutil/storetest -> internal/testutil
internal/trxvote -> internal/safego
internal/trxvote -> internal/store/pgstore
internal/usdnotional -> internal/bus
internal/verification -> internal/keydomain
EOF
)

LIST_ERR=$(mktemp)
if ! RAW=$(go list -f '{{.ImportPath}}|{{join .Imports " "}}' ./internal/... 2>"$LIST_ERR"); then
	echo "FAIL: go list 失败（逆向依赖常先炸成 import cycle）——方向违例已发生：" >&2
	cat "$LIST_ERR" >&2
	rm -f "$LIST_ERR"
	exit 1
fi
rm -f "$LIST_ERR"
ACTUAL=$(printf '%s\n' "$RAW" | awk -F'|' '{split($2,a," "); for(i in a) if(a[i] ~ /^arb\/internal\//) print $1" -> "a[i]}' | sed 's|arb/||g' | sort -u)

VIOL=""
while IFS= read -r e; do
	[ -z "$e" ] && continue
	if ! printf '%s\n' "$ALLOWED" | grep -qxF "$e"; then
		VIOL="$VIOL$e\n"
	fi
done <<< "$ACTUAL"

if [ -n "$VIOL" ]; then
	echo "FAIL: 新增跨包 import 方向违例（D-514 分层白名单）——确需引入先评审方向并更新 scripts/check_import_direction.sh 白名单：" >&2
	printf '%b' "$VIOL" >&2
	exit 1
fi
echo "OK: import direction gate（$(printf '%s\n' "$ACTUAL" | grep -c .)/$(printf '%s\n' "$ALLOWED" | grep -c .) 边）"

# D-212 M1 §3 硬红线（白名单之外加判，空壳期零报）：
# internal/risk 不 import trading|research|portfolio；internal/ledger 不 import trading；
# internal/research 不 import trading。
RED=""
while IFS= read -r e; do
	src="${e%% -> *}"; dst="${e##* -> }"
	case "$src" in
		internal/risk*)
			case "$dst" in internal/trading*|internal/research*|internal/portfolio*) RED="$RED$e\n";; esac;;
		internal/ledger*|internal/research*)
			case "$dst" in internal/trading*) RED="$RED$e\n";; esac;;
	esac
done <<< "$ACTUAL"
if [ -n "$RED" ]; then
	echo "FAIL: D-212 §3 部门红线违例（risk↛trading/research/portfolio；ledger/research↛trading）：" >&2
	printf '%b' "$RED" >&2
	exit 1
fi
