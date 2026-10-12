#!/usr/bin/env bash
# check_import_direction.sh —— D-514 T1：internal/* 包间 import 方向白名单门禁。
# 白名单=新仓 M2.1 搬迁后 go list 实拍边集冻结（部门目录 D-212）；新增跨包 import 即 FAIL
# （要放行先评审方向合规并更新本表——P4 可检查性）。删边不拦（收敛无害）。
# 红线示例（白名单已含语义）：store/pgstore/bus/fact/keydomain 等底层包不得 import
# dashboard/exec/autotrade/adapter 上层包；逆向依赖一经引入即拦。
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

ALLOWED=$(cat <<'EOF'
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
internal/data/collect/cmc -> internal/data/fact
internal/data/collect/cmc -> internal/platform/safego
internal/data/collect/cmc -> internal/store/pgstore
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
internal/gen/mtapi/mt5/mt5connect -> internal/data/adapter/gen/mtapi/mt5
internal/ir/notify -> internal/data/bus
internal/ir/notify -> internal/data/macro
internal/ir/notify -> internal/platform/analyst
internal/ir/notify -> internal/platform/safego
internal/ir/notify -> internal/store/pgstore
internal/platform/analyst -> internal/store/pgstore
internal/platform/errclass -> internal/data/keydomain
internal/platform/notional -> internal/data/keydomain
internal/platform/testutil/storetest -> internal/platform/testutil
internal/platform/testutil/storetest -> internal/store/pgstore
internal/platform/usdnotional -> internal/data/bus
internal/research/scan -> internal/data/fact
internal/research/scan -> internal/data/keydomain
internal/research/scan -> internal/store/pgstore
internal/review/verification -> internal/data/keydomain
internal/store/pgstore -> internal/data/fact
internal/store/pgstore -> internal/data/keydomain
internal/store/pgstore -> internal/data/normalize
internal/store/pgstore -> internal/platform/alerttext
internal/store/pgstore -> internal/platform/cryptoutil
internal/store/pgstore -> internal/platform/errclass
internal/store/pgstore -> internal/platform/notional
internal/store/pgstore -> internal/review/verification
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
