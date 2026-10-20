#!/usr/bin/env bash
# alert-bridge provision —— 本机桥一键配给（dev 机 systemd+SG 凭据面）
# 三段：①本机 unit/env/hits 目录 ②SG psql 只读 role ③SG ssh 只读 key
set -euo pipefail
SG_HOST="${SG_HOST:-root@192.229.85.35}"
SG_KEY="${SG_KEY:-$HOME/.ssh/id_ed25519}"

echo "=== ① 本机 unit+hits 目录+env 模板"
sudo cp tools/alert-bridge/alert-bridge.service /etc/systemd/system/
sudo mkdir -p /var/lib/alert-bridge && sudo chown mluser:mluser /var/lib/alert-bridge
if [[ ! -f /etc/alert-bridge.env ]]; then
    sudo tee /etc/alert-bridge.env >/dev/null <<'ENVEOF'
# alert-bridge 环境（BRIDGE_TOKEN=app_secrets 同源值）
BRIDGE_PORT=8099
BRIDGE_TOKEN=
RUNBOOK=/opt/nox/skills/告警分诊/SKILL.md
DRAFTS=/opt/nox/docs/handoff/STATE-分诊草案.md
HITS=/var/lib/alert-bridge/hits.jsonl
SESSION_TIMEOUT=600
ENVEOF
    sudo chmod 600 /etc/alert-bridge.env
    echo "[提醒] /etc/alert-bridge.env BRIDGE_TOKEN 须填（同 SG app_secrets 值）"
fi
sudo systemctl daemon-reload && sudo systemctl enable --now alert-bridge
systemctl status alert-bridge --no-pager | head -6

echo "=== ② SG psql 只读 role nox_ro"
ssh -i "$SG_KEY" "$SG_HOST" "docker exec nox-postgres psql -U nox -d nox -c \"
CREATE ROLE nox_ro LOGIN PASSWORD 'BRIDGERO_PLACEHOLDER';
GRANT CONNECT ON DATABASE nox TO nox_ro;
GRANT USAGE ON SCHEMA public TO nox_ro;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO nox_ro;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO nox_ro;\"" 2>&1 | tail -6

echo "=== ③ SG ssh 只读 key（command= 前缀限令族）"
ssh -i "$SG_KEY" "$SG_HOST" 'test -f /root/.ssh/bridge_ro_key.pub ||
  ssh-keygen -t ed25519 -f /root/.ssh/bridge_ro_key -N "" -C "bridge-ro" &&
  echo "只读 key 生成毕——authorized_keys 前缀配给："' && \
  cat <<'KEYEOF'
command="docker exec nox-postgres psql -U nox_ro -d nox -c \"$SSH_ORIGINAL_COMMAND\" 2>/dev/null || docker logs nox-core --tail ${SSH_ORIGINAL_COMMAND#logs }",no-port-forwarding,no-X11-forwarding,no-agent-forwarding,no-pty <pubkey>
KEYEOF

echo "=== ④ 隧道 key+unit（dev→SG ssh -R 反向隧道=SG localhost:8099 可达）"
if [[ ! -f ~/.ssh/alert_bridge_tunnel ]]; then
    ssh-keygen -t ed25519 -f ~/.ssh/alert_bridge_tunnel -N "" -C "alert-bridge-tunnel"
fi
ssh -i "$SG_KEY" "$SG_HOST" "mkdir -p /root/.ssh && chmod 700 /root/.ssh &&
  grep -qF 'alert-bridge-tunnel' /root/.ssh/authorized_keys ||
  echo '$(cat ~/.ssh/alert_bridge_tunnel.pub)' >> /root/.ssh/authorized_keys"
sudo cp tools/alert-bridge/alert-bridge-tunnel.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now alert-bridge-tunnel
systemctl status alert-bridge-tunnel --no-pager | head -6
# SG 侧 sink 配置键（桥地址=SG 本机 127.0.0.1:8099 走隧道）
ssh -i "$SG_KEY" "$SG_HOST" "docker exec nox-postgres psql -U nox -d nox -c \
  INSERT INTO app_config (key,value,note) VALUES
    ('NOX_ALERT_BRIDGE_URL','http://127.0.0.1:8099','告警桥地址=SG localhost:8099 走 dev→SG 反向隧道')
  ON CONFLICT (key) DO UPDATE SET value='http://127.0.0.1:8099';
  INSERT INTO app_config (key,value,note) VALUES
    ('NOX_ALERT_BRIDGE_WARN_PATTERNS','[前提塌],[stale],[attach]','warn 白名单三族起手')
  ON CONFLICT (key) DO NOTHING;
  SELECT key,value FROM app_config WHERE key LIKE 'NOX_ALERT_BRIDGE%';\" | tail -4

echo "=== 配给毕——验收三件（S2 成本实测/health/穿通）见 README"
