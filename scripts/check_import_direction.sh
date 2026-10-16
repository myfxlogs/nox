#!/usr/bin/env bash
# check_import_direction.sh —— D-514 T1：internal/* 包间 import 方向白名单门禁。
# 白名单=新仓 M2.1 搬迁后 go list 实拍边集冻结（部门目录 D-212）；新增跨包 import 即 FAIL
# （要放行先评审方向合规并更新本表——P4 可检查性）。删边不拦（收敛无害）。
# 红线示例（白名单已含语义）：store/pgstore/bus/fact/keydomain 等底层包不得 import
# dashboard/exec/autotrade/adapter 上层包；逆向依赖一经引入即拦。
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

ALLOWED=$(cat <<'EOF'
internal/api/dashboard -> internal/data/adapter
internal/api/dashboard -> internal/data/adapter/gen/mtapi/mt5
internal/api/dashboard -> internal/data/bus
internal/api/dashboard -> internal/data/collect/exchange
internal/api/dashboard -> internal/data/fact
internal/api/dashboard -> internal/data/keydomain
internal/api/dashboard -> internal/data/marketstate
internal/api/dashboard -> internal/data/venue
internal/api/dashboard -> internal/gen/dashboardv1
internal/api/dashboard -> internal/infra/dephealth
internal/api/dashboard -> internal/ledger/reconcile
internal/api/dashboard -> internal/platform/alertcool
internal/api/dashboard -> internal/platform/analyst
internal/api/dashboard -> internal/platform/combo
internal/api/dashboard -> internal/platform/config
internal/api/dashboard -> internal/platform/cryptoutil
internal/api/dashboard -> internal/platform/errclass
internal/api/dashboard -> internal/platform/httpx
internal/api/dashboard -> internal/platform/notional
internal/api/dashboard -> internal/platform/safego
internal/api/dashboard -> internal/platform/usdnotional
internal/api/dashboard -> internal/portfolio/capitalview
internal/api/dashboard -> internal/portfolio/trxvote
internal/api/dashboard -> internal/research/econ
internal/api/dashboard -> internal/research/scan
internal/api/dashboard -> internal/review/kbreport
internal/api/dashboard -> internal/review/sysretro
internal/api/dashboard -> internal/review/verification
internal/api/dashboard -> internal/risk/gate
internal/api/dashboard -> internal/store/pgstore
internal/api/dashboard -> internal/trading/exec
internal/data/adapter -> internal/data/adapter/gen/mtapi/mt5
internal/data/adapter -> internal/data/bus
internal/data/adapter -> internal/data/collect
internal/data/adapter -> internal/data/fact
internal/data/adapter -> internal/data/keydomain
internal/data/adapter -> internal/data/venue
internal/data/adapter -> internal/infra/dephealth
internal/data/adapter -> internal/platform/safego
internal/data/adapter/gen/mtapi/mt5/mt5connect -> internal/data/adapter/gen/mtapi/mt5
internal/data/bus -> internal/data/keydomain
internal/data/bus -> internal/platform/safego
internal/data/cexscan -> internal/data/bus
internal/data/cexscan -> internal/data/keydomain
internal/data/cexscan -> internal/data/venue
internal/data/cexscan -> internal/gen/dashboardv1
internal/data/cexscan -> internal/platform/config
internal/data/cexscan -> internal/platform/httpx
internal/data/cexscan -> internal/store/pgstore
internal/data/collect -> internal/data/bus
internal/data/collect -> internal/data/fact
internal/data/collect -> internal/platform/safego
internal/data/collect/deribit -> internal/data/fact
internal/data/collect/exchange -> internal/data/bus
internal/data/collect/exchange -> internal/data/fact
internal/data/collect/exchange -> internal/data/keydomain
internal/data/collect/exchange -> internal/data/venue
internal/data/collect/exchange -> internal/infra/dephealth
internal/data/collect/exchange -> internal/platform/alerttext
internal/data/collect/exchange -> internal/platform/httpx
internal/data/collect/exchange -> internal/platform/safego
internal/data/collect/exchange -> internal/platform/signutil
internal/data/collect/exchange -> internal/store/pgstore
internal/data/collect/trx -> internal/data/fact
internal/data/collect/trx -> internal/store/pgstore
internal/data/macro -> internal/platform/safego
internal/data/macro -> internal/store/pgstore
internal/data/marketstate -> internal/data/bus
internal/data/marketstate -> internal/data/fact
internal/data/marketstate -> internal/data/keydomain
internal/data/marketstate -> internal/store/pgstore
internal/data/symdig -> internal/data/fact
internal/data/symdig -> internal/ir/inboxfmt
internal/data/symdig -> internal/platform/analyst
internal/data/symdig -> internal/research/scan
internal/data/symdig -> internal/store/pgstore
internal/gen/dashboardv1/dashboardv1connect -> internal/gen/dashboardv1
internal/ir/notify -> internal/data/macro
internal/ir/notify -> internal/platform/analyst
internal/ir/notify -> internal/platform/safego
internal/ir/notify -> internal/store/pgstore
internal/ledger/reconcile -> internal/data/adapter
internal/ledger/reconcile -> internal/data/adapter/gen/mtapi/mt5
internal/ledger/reconcile -> internal/data/keydomain
internal/ledger/reconcile -> internal/data/venue
internal/ledger/reconcile -> internal/gen/dashboardv1
internal/ledger/reconcile -> internal/platform/httpx
internal/ledger/reconcile -> internal/platform/signutil
internal/ledger/reconcile -> internal/store/pgstore
internal/ledger/reconcile -> internal/trading/exec
internal/ledger/settleadapter -> internal/ledger/settlement
internal/ledger/settleadapter -> internal/portfolio/lifecycle
internal/ledger/settleadapter -> internal/store/pgstore
internal/ledger/settleadapter -> internal/trading/exec
internal/ledger/settlement -> internal/data/keydomain
internal/ledger/settlement -> internal/platform/notional
internal/ledger/settlement -> internal/review/mmm
internal/ledger/settlement -> internal/review/verification
internal/ledger/settlement -> internal/store/pgstore
internal/ledger/swapledger -> internal/data/fact
internal/ledger/swapledger -> internal/data/keydomain
internal/ledger/swapledger -> internal/platform/decimalutil
internal/ledger/swapledger -> internal/platform/safego
internal/ledger/swapledger -> internal/store/pgstore
internal/platform/analyst -> internal/kbsearch
internal/platform/analyst -> internal/store/pgstore
internal/platform/errclass -> internal/data/keydomain
internal/platform/notional -> internal/data/keydomain
internal/platform/testutil/storetest -> internal/platform/testutil
internal/platform/testutil/storetest -> internal/store/pgstore
internal/platform/usdnotional -> internal/data/bus
internal/portfolio/budget -> internal/store/pgstore
internal/portfolio/capitalview -> internal/data/bus
internal/portfolio/capitalview -> internal/data/venue
internal/portfolio/capitalview -> internal/data/keydomain
internal/portfolio/capitalview -> internal/ledger/reconcile
internal/portfolio/capitalview -> internal/store/pgstore
internal/portfolio/budget -> internal/risk/gate
internal/portfolio/idle -> internal/ir/inboxfmt
internal/portfolio/idle -> internal/store/pgstore
internal/portfolio/trxvote -> internal/platform/safego
internal/portfolio/trxvote -> internal/store/pgstore
internal/research/detector -> internal/data/bus
internal/research/detector -> internal/data/fact
internal/research/detector -> internal/data/isreal
internal/research/detector -> internal/data/keydomain
internal/research/evaluator -> internal/data/keydomain
internal/research/evaluator -> internal/research/detector
internal/research/explorer -> internal/gen/explorer/v1
internal/research/explorer -> internal/ir/inboxfmt
internal/research/explorer -> internal/platform/analyst
internal/research/explorer -> internal/store/pgstore
internal/research/oppmatch -> internal/data/collect/exchange
internal/research/oppmatch -> internal/data/keydomain
internal/research/oppmatch -> internal/platform/httpx
internal/research/oppmatch -> internal/portfolio/lifecycle
internal/research/oppmatch -> internal/research/econ
internal/research/oppmatch -> internal/risk/gate
internal/research/oppmatch -> internal/store/pgstore
internal/research/scan -> internal/data/fact
internal/research/scan -> internal/data/keydomain
internal/research/scan -> internal/store/pgstore
internal/review -> internal/store/pgstore
internal/review/deptreport -> internal/platform/analyst
internal/review/deptreport -> internal/store/pgstore
internal/review/posreview -> internal/platform/analyst
internal/review/hearing -> internal/platform/analyst
internal/review/hearing -> internal/store/pgstore
internal/review/posreview -> internal/store/pgstore
internal/review/kbreport -> internal/platform/analyst
internal/review/kbreport -> internal/store/pgstore
internal/review/sysretro -> internal/ir/inboxfmt
internal/review/sysretro -> internal/platform/analyst
internal/review/sysretro -> internal/review/kbreport
internal/review/sysretro -> internal/review/mmm
internal/review/sysretro -> internal/store/pgstore
internal/review/verification -> internal/data/keydomain
internal/risk/gate -> internal/data/bus
internal/risk/gate -> internal/data/fact
internal/risk/gate -> internal/data/keydomain
internal/risk/gate -> internal/data/macro
internal/risk/gate -> internal/platform/combo
internal/risk/gate -> internal/platform/errclass
internal/risk/gate -> internal/platform/usdnotional
internal/risk/gate -> internal/portfolio/capitalview
internal/risk/gate -> internal/store/pgstore
internal/risk/gate -> internal/trading/exec
internal/risk/position -> internal/data/bus
internal/risk/position -> internal/platform/safego
internal/risk/position -> internal/review/verification
internal/risk/position -> internal/store/pgstore
internal/risk/selfwatch -> internal/data/fact
internal/risk/selfwatch -> internal/data/macro
internal/risk/selfwatch -> internal/platform/safego
internal/risk/selfwatch -> internal/research/scan
internal/risk/selfwatch -> internal/review/sysretro
internal/risk/selfwatch -> internal/store/pgstore
internal/risk/watch -> internal/data/bus
internal/risk/watch -> internal/store/pgstore
internal/store/pgstore -> internal/data/fact
internal/store/pgstore -> internal/data/keydomain
internal/store/pgstore -> internal/data/normalize
internal/store/pgstore -> internal/platform/alerttext
internal/store/pgstore -> internal/platform/cryptoutil
internal/store/pgstore -> internal/platform/errclass
internal/store/pgstore -> internal/platform/notional
internal/store/pgstore -> internal/review/verification
internal/trading/autotrade -> internal/data/bus
internal/trading/autotrade -> internal/data/fact
internal/trading/autotrade -> internal/data/isreal
internal/trading/autotrade -> internal/data/keydomain
internal/trading/autotrade -> internal/data/macro
internal/trading/autotrade -> internal/data/marketstate
internal/trading/autotrade -> internal/ledger/settlement
internal/trading/autotrade -> internal/platform/combo
internal/trading/autotrade -> internal/platform/errclass
internal/trading/autotrade -> internal/platform/safego
internal/trading/autotrade -> internal/platform/usdnotional
internal/trading/autotrade -> internal/portfolio/budget
internal/trading/autotrade -> internal/portfolio/capitalview
internal/trading/autotrade -> internal/portfolio/lifecycle
internal/trading/autotrade -> internal/research/econ
internal/trading/autotrade -> internal/review/mmm
internal/trading/autotrade -> internal/risk/gate
internal/trading/autotrade -> internal/risk/position
internal/trading/autotrade -> internal/store/pgstore
internal/trading/autotrade -> internal/trading/exec
internal/trading/dciexec -> internal/data/bus
internal/trading/dciexec -> internal/data/collect/exchange
internal/trading/dciexec -> internal/infra/dephealth
internal/trading/dciexec -> internal/kbsearch
internal/trading/dciexec -> internal/risk/gate
internal/trading/dciexec -> internal/platform/errclass
internal/trading/dciexec -> internal/platform/signutil
internal/trading/dciexec -> internal/store/pgstore
internal/trading/engine -> internal/data/bus
internal/trading/engine -> internal/data/fact
internal/trading/engine -> internal/data/keydomain
internal/trading/engine -> internal/research/detector
internal/trading/engine -> internal/research/evaluator
internal/trading/engine -> internal/store/pgstore
internal/trading/exec -> internal/data/adapter/gen/mtapi/mt5
internal/trading/exec -> internal/data/bus
internal/trading/exec -> internal/data/fact
internal/trading/exec -> internal/data/keydomain
internal/trading/exec -> internal/data/marketstate
internal/trading/autotrade -> internal/infra/dephealth
internal/trading/exec -> internal/infra/dephealth
internal/trading/exec -> internal/platform/combo
internal/trading/exec -> internal/platform/httpx
internal/trading/exec -> internal/platform/notional
internal/trading/exec -> internal/platform/safego
internal/trading/exec -> internal/platform/signutil
internal/trading/exec -> internal/store/pgstore
EOF
)

RAW=$(go list -f '{{.ImportPath}}|{{join .Imports " "}}' ./internal/... 2>/dev/null)
ACTUAL=$(printf '%s\n' "$RAW" | awk -F'|' '{split($2,a," "); for(i in a) if(a[i] ~ /^nox\/internal\//) print $1" -> "a[i]}' | sed 's|nox/||g' | sort -u)

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
# internal/risk 不 import trading/engine|autotrade|dciexec（引擎侧反向）|research|portfolio/lifecycle；
# internal/ledger 不 import trading/engine|autotrade|dciexec；internal/research 不 import trading。
# D-220：risk/gate→trading/exec（ComboAllowSpec/约束类型）、risk/gate→portfolio/capitalview、
# ledger/reconcile|settleadapter→trading/exec、settleadapter→portfolio/lifecycle 为 D-215/216/217
# 钦定拆分的固有边（原为同包内部引用），白名单冻结。

RED=""
while IFS= read -r e; do
	src="${e%% -> *}"; dst="${e##* -> }"
	case "$src" in
		internal/risk*)
			case "$dst" in internal/trading/autotrade|internal/trading/engine|internal/trading/dciexec|internal/research/detector|internal/research/evaluator|internal/research/econ|internal/portfolio/lifecycle) RED="$RED$e\n";; esac;;
		internal/ledger*)
			case "$dst" in internal/trading/autotrade|internal/trading/engine|internal/trading/dciexec) RED="$RED$e\n";; esac;;
		internal/research*)
			case "$dst" in internal/trading*) RED="$RED$e\n";; esac;;
	esac
done <<< "$ACTUAL"
if [ -n "$RED" ]; then
	echo "FAIL: D-212 §3 部门红线违例（risk↛trading/research/portfolio；ledger/research↛trading）：" >&2
	printf '%b' "$RED" >&2
	exit 1
fi
