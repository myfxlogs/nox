#!/usr/bin/env bash
# check-env-reads.sh —— D-564 S5 U7 防偷懒门禁：venue/凭据域键禁 env 直读。
# 病灶根：「.env 里能读到=合法」——凭据权威在 exchange_configs 密文列/app_secrets/
# app_config（D-515 轨）。三层判定：
#   ① 硬拒（无论白名单）：venue 凭据域键（*_API_KEY/*_API_SECRET/*_PASSPHRASE 等）
#     —— 内部/脚本/工具中出现即拒（gatefundingprobe/evidence_pos 型病灶）；
#   ② IsAppSecretKey 域（config.IsAppSecretKey 同源模式：ARB_AI_/LONGBRIDGE_/CMC_/
#     ARB_FRED_/TRONGRID_API_KEY…）在 Go 消费面 env 直读 → 拒（app_secrets 轨权威）；
#   ③ 其余 env 键 → 必须在 scripts/env-allowlist.txt（存量 ratchet 基线+显式新增评审）。
# 自身排除（本脚本含键名字面量示例）。
set -u
cd "$(dirname "$0")/.."

ALLOWLIST="scripts/env-allowlist.txt"
[ -f "$ALLOWLIST" ] || { echo "FAIL: $ALLOWLIST 缺失"; exit 1; }

# ① 硬拒模式（venue 凭据域——永不豁免）：
HARD='ARB_GATE_API_KEY|ARB_GATE_SECRET|ARB_GATE_BASE|OKX_API_KEY|OKX_API_SECRET|OKX_API_PASSPHRASE|BINANCE_API_KEY|BINANCE_API_SECRET|GATE_API_KEY|GATE_API_SECRET|GATE_BASE|_PRIVATE_KEY|TRX_VOTE_PRIVATE_KEY'
# ② app_secret 域（Go 消费面拒；外部脚本/工具面 ratchet 域）：
SECRET='ARB_AI_|ARB_FEISHU_APP_SECRET|LONGBRIDGE_APP_KEY|LONGBRIDGE_APP_SECRET|LONGBRIDGE_ACCESS_TOKEN|CMC_API_KEY|ARB_CMC_API_KEY|ARB_FRED_API_KEY|FRED_API_KEY|TRONGRID_API_KEY|ARB_TRON_API_KEY|ARB_ANT_API_KEY|ANT_CLAW|_ACCESS_TOKEN'
fails=0

# Go 消费面：Getenv/LookupEnv 逐键判定
while IFS= read -r f; do
  while IFS= read -r ln; do
    lnno=$(echo "$ln" | cut -d: -f1)
    key=$(echo "$ln" | grep -oE '(os\.Getenv|os\.LookupEnv)\("[^"]+"' | sed -E 's/.*"([^"]+)"/\1/' | head -1)
    [ -z "$key" ] && continue
    if echo "$key" | grep -qE "$HARD"; then
      echo "FAIL: $f:$lnno venue 凭据键 $key env 直读（硬拒——凭据走 exchange_configs 密文列）"
      fails=$((fails+1)); continue
    fi
    if echo "$key" | grep -qE "$SECRET"; then
      if grep -qxF "$key" "$ALLOWLIST"; then
        continue # 存量冻结（boot-seed 双轨/一次性工具——ratchet 基线，新键禁入）
      fi
      echo "FAIL: $f:$lnno app_secret 域键 $key Go 面 env 直读（走 app_secrets 轨）"
      fails=$((fails+1)); continue
    fi
    if ! grep -qxF "$key" "$ALLOWLIST"; then
      echo "FAIL: $f:$lnno $key env 直读但不在白名单 $ALLOWLIST（新键须过评审）"
      fails=$((fails+1))
    fi
  done < <(grep -nE 'os\.Getenv\("|os\.LookupEnv\("' "$f" 2>/dev/null)
done < <(find internal cmd tools -name '*.go' | grep -v _test)

# 脚本/工具面（sh/py 无 os.Getenv 惯例——键名字面量扫描，仅硬拒域）：
while IFS= read -r f; do
  while IFS= read -r ln; do
    echo "FAIL: $f:$(echo "$ln" | cut -d: -f1) venue 凭据键字面量（硬拒——走 credread/exchange_configs）"
    fails=$((fails+1))
  done < <(grep -inE "$HARD" "$f" 2>/dev/null | grep -vE '^\s*[0-9]+:\s*#')
done < <(find tools scripts -name '*.sh' -o -name '*.py' 2>/dev/null | grep -v check-env-reads.sh | grep -v env-allowlist)

# T7 锚支持：额外扫描路径（临时文件/目录参数——新增键即时判定）。
for extra in "$@"; do
  while IFS= read -r ln; do
    key=$(echo "$ln" | grep -oE '(os\.Getenv|os\.LookupEnv)\("[^"]+"' | sed -E 's/.*"([^"]+)"/\1/' | head -1)
    [ -z "$key" ] && continue
    if echo "$key" | grep -qE "$HARD"; then
      echo "FAIL: $extra venue 凭据键 $key env 直读（硬拒）"; fails=$((fails+1)); continue
    fi
    if ! grep -qxF "$key" "$ALLOWLIST"; then
      echo "FAIL: $extra $key env 直读但不在白名单（新键须过评审）"; fails=$((fails+1))
    fi
  done < <(grep -nE 'os\.Getenv\("|os\.LookupEnv\("' "$extra" 2>/dev/null)
done

if [ $fails -gt 0 ]; then
  echo "check-env-reads: $fails 处违规（凭据只入 pg——practices「凭据归 pg」规则）"
  exit 1
fi
echo "check-env-reads: OK（硬拒域/app_secret 域零直读；白名单 $(grep -cE '^[A-Z]' "$ALLOWLIST") 键）"
