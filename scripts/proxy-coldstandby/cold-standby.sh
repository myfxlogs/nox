#!/usr/bin/env bash
# cex-proxy 代理冷备一键拉起脚本骨架（D-146 S3 · 9-28 现网拓扑校正）
# 用法：./cold-standby.sh {start|stop|status|health}
#
# 现网拓扑：CF Tunnel → 代理机 loopback:8443；裸 IP 直连对外 DROP。
# 前置：复制 cex-proxy 二进制 + config.json 到备机工作目录；填入 TOKEN（与主机 -token 一致）。
#   CERT_DIR/SANS 仅裸 IP 回退（方案 B）需要；CF 接管（方案 A）另需 cloudflared tunnel token。
set -euo pipefail

# === 配置（须与主机一致，按业主环境修改）===
LISTEN=":8443"
TOKEN=""           # 必填：与主机 -token 一致（nox 端 NOX_BINANCE_PROXY_TOKEN 指向同值）
SANS=""            # 仅裸连回退必填：备机公网 IP（证书 SANs，与主机不同须重新生成或用域名）
CERT_DIR="./certs" # 仅裸连回退需要：从主机复制的 certs 目录（CA + 服务器证书）
CONFIG="./config.json" # 从主机复制的路由清单（缺失走内置默认，路由漂移=断流）
BINARY="./cex-proxy" # 代理二进制（从主机复制或备机重新编译：go build -o cex-proxy ./tools/cex-proxy）
PID_FILE="./cex-proxy.pid"
LOG_FILE="./cex-proxy.log"

# === 健康检查端点（/ca-cert 为公开探活路径，返 CA PEM，不过令牌闸不碰上游）===
HEALTH_URL="https://127.0.0.1${LISTEN}/ca-cert"

cmd_start() {
    if [ -f "$PID_FILE" ] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
        echo "already running (pid=$(cat "$PID_FILE"))"
        return 0
    fi
    if [ -z "$TOKEN" ]; then
        echo "FAIL: TOKEN 未配置——编辑本脚本填入与主机一致的 -token 值" >&2
        exit 1
    fi
    if [ ! -x "$BINARY" ]; then
        echo "FAIL: 代理二进制 $BINARY 不存在或不可执行" >&2
        echo "  复制主机二进制或备机编译：go build -o cex-proxy ./tools/cex-proxy" >&2
        exit 1
    fi
    if [ ! -f "$CONFIG" ]; then
        echo "WARN: 路由配置 $CONFIG 不存在——走内置默认清单，与主机漂移风险自负" >&2
    fi
    if [ -n "$SANS" ] && [ ! -d "$CERT_DIR" ]; then
        echo "FAIL: 裸连回退需证书目录 $CERT_DIR——从主机复制 certs/ 目录" >&2
        exit 1
    fi
    echo "starting cex-proxy cold standby..."
    # SANS 非空时传入（备机 IP 加入证书 SANs）；空则用 certs/ 已有证书/无证书模式
    local sans_arg=""
    if [ -n "$SANS" ]; then
        sans_arg="-sans=$SANS"
    fi
    local cfg_arg=""
    if [ -f "$CONFIG" ]; then
        cfg_arg="-config=$CONFIG"
    fi
    nohup "$BINARY" -listen="$LISTEN" -token="$TOKEN" $sans_arg $cfg_arg >"$LOG_FILE" 2>&1 &
    echo $! >"$PID_FILE"
    sleep 1
    if kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
        echo "started (pid=$(cat "$PID_FILE")), log=$LOG_FILE"
    else
        echo "FAIL: 启动后进程已退出，查看 $LOG_FILE" >&2
        rm -f "$PID_FILE"
        exit 1
    fi
}

cmd_stop() {
    if [ ! -f "$PID_FILE" ]; then
        echo "not running (no pid file)"
        return 0
    fi
    local pid
    pid="$(cat "$PID_FILE")"
    if kill -0 "$pid" 2>/dev/null; then
        kill "$pid"
        sleep 1
        if kill -0 "$pid" 2>/dev/null; then
            kill -9 "$pid"
        fi
        echo "stopped (pid=$pid)"
    else
        echo "process $pid not running (stale pid file)"
    fi
    rm -f "$PID_FILE"
}

cmd_status() {
    if [ -f "$PID_FILE" ] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
        echo "running (pid=$(cat "$PID_FILE"))"
        return 0
    fi
    echo "stopped"
    return 1
}

cmd_health() {
    if ! curl -sk --max-time 5 "$HEALTH_URL" >/dev/null 2>&1; then
        echo "FAIL: health check failed ($HEALTH_URL)"
        exit 1
    fi
    echo "healthy"
}

case "${1:-}" in
    start)  cmd_start ;;
    stop)   cmd_stop ;;
    status) cmd_status ;;
    health) cmd_health ;;
    *)
        echo "用法: $0 {start|stop|status|health}" >&2
        exit 1
        ;;
esac
