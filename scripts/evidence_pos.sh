#!/usr/bin/env bash
# evidence_pos.sh — 真钱仓位证据采集器（posID 参数化）。
# 动机（业主令 9-24）：real 盘全部数据留证——滑点/手续费/价差/funding/venue原始记录，
#   为以后提供证据。DB 日备份只留 7 天，carry 仓可能跑数周→本脚本独立目录长期留存。
#
# 产出：/opt/nox-backups/evidence/pos<ID>/<UTC时间戳>/ 下：
#   db_*.csv        — 该 pos 相关各表行（legs/evals/cashflows/settlements/swap快照/autotrade决策）
#   okx_*.json      — venue 原生记录（positions/bills资金费流水/balance）signed REST 裸 JSON
#   core_tail.log   — nox-core 近 500 行日志（当时点上下文）
#
# 凭据：OKX_* 经 tools/credread 从 exchange_configs 解密读（D-564 S5：.env 已退役；
# DB_DSN/ARB_MASTER_KEY 基建键照旧）。credread 失败=fail-closed 跳过 venue 侧（DB 侧已采）。
# crontab：7 */2 * * * /opt/nox/scripts/evidence_pos.sh 502 >> /opt/nox-backups/evidence/pos502/cron.log 2>&1
set -u
POSID="${1:?usage: evidence_pos.sh <position_id>}"
TS=$(date -u +%Y%m%dT%H%M%SZ)
BASE="/opt/nox-backups/evidence/pos${POSID}/${TS}"
mkdir -p "$BASE"
PSQL="docker exec nox-postgres psql -U nox -d nox -A -F,"

# D-508 S13：cron 退役自检——持仓已平（CLOSED/FAILED）则不再采集，提示摘除 cron。
STATUS=$($PSQL -t -c "SELECT status FROM positions WHERE id=${POSID}" | tail -1)
case "$STATUS" in
  CLOSED|FAILED)
    echo "$(date -u +%FT%TZ) pos${POSID} status=${STATUS} —— 已退役可摘 cron"
    exit 0
    ;;
esac

# ── DB 侧：该 pos 全证据面 ──────────────────────────────
$PSQL -c "SELECT * FROM positions WHERE id=${POSID}" > "$BASE/db_positions.csv"
$PSQL -c "SELECT * FROM position_legs WHERE position_id=${POSID} ORDER BY leg_no, attempt_no" > "$BASE/db_legs.csv"
$PSQL -c "SELECT * FROM position_evals WHERE position_id=${POSID} ORDER BY evaluated_at" > "$BASE/db_evals.csv"
$PSQL -c "SELECT * FROM position_cashflows WHERE position_id=${POSID} ORDER BY ts" > "$BASE/db_cashflows.csv"
$PSQL -c "SELECT * FROM position_settlements WHERE position_id=${POSID} ORDER BY id" > "$BASE/db_settlements.csv" 2>/dev/null || true
$PSQL -c "SELECT * FROM position_swap_snapshots WHERE position_id=${POSID} ORDER BY id" > "$BASE/db_swap_snapshots.csv" 2>/dev/null || true
$PSQL -c "SELECT * FROM position_daily_income WHERE position_id=${POSID} ORDER BY id" > "$BASE/db_daily_income.csv" 2>/dev/null || true
OPP=$($PSQL -t -c "SELECT opportunity_id FROM positions WHERE id=${POSID}" | tail -1)
[ -n "$OPP" ] && $PSQL -c "SELECT * FROM autotrade_logs WHERE opportunity_id='${OPP}' ORDER BY ts" > "$BASE/db_autotrade_logs.csv"

# ── venue 侧：OKX 原生裸 JSON（签名，凭据不回显）─────────
# D-564 S5：credread 解密读 OKX 凭据（工具失败=stderr 记因+跳过 venue 侧）。
CREAD="${CREDREAD_BIN:-/opt/nox/bin/credread}"
if [ ! -x "$CREAD" ] && command -v go >/dev/null 2>&1; then
  CREAD="$(mktemp)"; go build -o "$CREAD" /opt/nox/tools/credread || CREAD=""
fi
OKX_ENV=""
if [ -n "$CREAD" ] && [ -x "$CREAD" ]; then
  # D-114：credread 需 TCP DSN，端口取 nox-postgres 运行容器发布值（5433=alphaforge 化石端口）。
  NOX_PG_PORT="$(docker port nox-postgres 5432 2>/dev/null | head -n1 | awk -F: '{print $NF}')"
  if [ -n "$NOX_PG_PORT" ]; then
    OKX_ENV=$(DB_DSN="${DB_DSN:-postgres://nox:nox@127.0.0.1:${NOX_PG_PORT}/nox?sslmode=disable}" "$CREAD" -venue okx -type perp 2>/dev/null) || OKX_ENV=""
  fi
fi
if [ -z "$OKX_ENV" ]; then
  echo "$(date -u +%FT%TZ) credread OKX 凭据不可得（env 已退役；检查 exchange_configs/ARB_MASTER_KEY）——跳过 venue 侧"
fi
export OKX_ENV
python3 - "$BASE" <<'PYEOF'
import base64, hashlib, hmac, json, urllib.request, ssl, datetime, sys, os
outdir = sys.argv[1]
env = {}
for line in os.environ.get("OKX_ENV", "").splitlines():
    if "=" in line:
        k, v = line.split("=", 1); env[k.strip()] = v.strip()
key = env.get("API_KEY"); sec = env.get("API_SECRET"); pp = env.get("PASSPHRASE")
base = env.get("BASE_URL", "https://www.okx.com") or "https://www.okx.com"  # pg base_url 权威；空=官方直连
tok = os.environ.get("ARB_BINANCE_PROXY_TOKEN", "")
if not all([key, sec, pp]):
    sys.exit(0)  # 凭据缺则跳过 venue 侧，DB 侧已采
ctx = ssl.create_default_context()
def probe(path, fname):
    ts = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.000Z")
    sig = base64.b64encode(hmac.new(sec.encode(), (ts+"GET"+path).encode(), hashlib.sha256).digest()).decode()
    hdrs = {"OK-ACCESS-KEY": key, "OK-ACCESS-SIGN": sig, "OK-ACCESS-TIMESTAMP": ts,
            "OK-ACCESS-PASSPHRASE": pp, "User-Agent": "Go-http-client/1.1"}
    if False:  # D-564 S5：代理端点归 exchange_configs base_url（.env 退役；credread BASE_URL 已含）
        hdrs["X-Proxy-Token"] = tok  # 仅代理端点附令牌——直连官方不外泄
    req = urllib.request.Request(base+path, headers=hdrs)
    try:
        with urllib.request.urlopen(req, context=ctx, timeout=20) as r:
            body = r.read().decode()
    except Exception as e:
        body = json.dumps({"error": str(e)})
    open(os.path.join(outdir, fname), "w").write(body)
probe("/api/v5/account/positions?instType=SWAP", "okx_positions.json")
probe("/api/v5/account/bills?instType=SWAP&limit=100", "okx_bills.json")
probe("/api/v5/account/balance", "okx_balance.json")
probe("/api/v5/account/leverage-info?instId=XAU-USDT-SWAP&mgnMode=cross", "okx_leverage.json")
PYEOF

# ── 运行上下文 ──────────────────────────────────────────
docker logs nox-core --tail 500 > "$BASE/core_tail.log" 2>&1 || true
ln -sfn "$BASE" "/opt/nox-backups/evidence/pos${POSID}/latest"
echo "$(date -u +%FT%TZ) pos${POSID} evidence -> $BASE"
