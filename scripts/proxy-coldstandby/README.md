# cex-proxy 代理冷备模板（D-146 S3 · 9-28 按现网拓扑重写）

> 角色：Claude 设计定稿（2026-08-29），施工方交付通用模板。代理实现细节由业主补充后实例化。
> 背景：CEX 通道（Binance/OKX/Gate REST+WS）经单台代理机（`tools/cex-proxy`）转发，代理机=SPOF。
> 目标：冷备部署模板——主机故障时分钟级拉起备机恢复 CEX 通道。
> 不做热备（CEX 断连=fail-closed 不开新仓+仓位有 CEX 端生存期，分钟级可容忍）。

## 现网拓扑（化石警戒——以此为准）

```
arb-core ──TLS──> cex.myfxlogs.org (CF 边缘) ──CF Tunnel──> 代理机 loopback:8443 (cex-proxy)
```

- 代理机仅绑 `lo:8443`，iptables 对外 DROP——**裸 IP:8443 直连不可达**（9-27 加固）。
- 公网只认域名 `cex.myfxlogs.org`；CF 边缘终结 TLS（系统 CA 链），自签 CA 仅裸连场景用。
- arb 侧权威端点（.env）：`BINANCE_PROXY_URL` / `ARB_OKX_PROXY_URL` = `https://cex.myfxlogs.org`、
  `ARB_CEX_WS_BASE` = `wss://cex.myfxlogs.org`、`exchange_configs.base_url` 同域名。
- 路由清单 SSOT = `tools/cex-proxy/config.json`（与 `tools/cex-proxy/README.md` 同步受 fossil-lint 护）。

## 产物清单

| 文件 | 用途 |
|------|------|
| `README.md`（本文件） | 配置快照说明 + 演练清单 |
| `cold-standby.sh` | 一键拉起脚本骨架（systemd/compose 二选一） |

## 配置快照（冷备机需与主机一致）

| 参数 | 来源 | 同步方式 |
|------|------|----------|
| 代理二进制 | `tools/cex-proxy`（`go build` 产物） | 复制二进制或源码到备机重新编译 |
| 路由配置 | `config.json`（工作目录） | **整文件复制**——路由漂移=断流（D-537 教训） |
| 鉴权令牌 | `-token=xxx`（主机生成的 token） | **必须与主机一致**——arb 端 `ARB_BINANCE_PROXY_TOKEN` 指向同一值 |
| CF Tunnel | 主机 `cloudflared` 的 tunnel token + ingress 配置 | 备机跑同一 named tunnel → `cex.myfxlogs.org` 自动落备机（named tunnel 单活，接管即切换） |
| 自签证书（仅裸连回退） | `./certs/`（CA + 服务器证书） | 复制主机 certs；无 CF 回退时 arb 端须 `ARB_BINANCE_PROXY_CA` pin |
| SANs（仅裸连回退） | `-sans=<主机公网IP>` | 备机公网 IP 不同 → 须重新生成证书（备机 IP 加入 SANs） |

## 部署架构

### 方案 A：CF Tunnel 接管（现网推荐，零 arb 侧改动）

1. 备机安装 `cloudflared`，写入与主机一致的 tunnel token + ingress（`cex.myfxlogs.org` → `localhost:8443`）。
2. 复制 `cex-proxy` 二进制 + `config.json` 到备机（如 `/opt/cex-proxy/`）。
3. `cold-standby.sh start`（绑 lo:8443），再启 `cloudflared tunnel run`——named tunnel 单活，
   主机隧道断后 CF 自动把域名落到备机连接。
4. arb 侧 `.env` **不动**（域名不变，CF 层切换）。

### 方案 B：裸 IP 回退（无 CF 场景，自签证书）

1. 复制 `certs/` 到备机，`cold-standby.sh` 填 `TOKEN`/`SANS`（备机公网 IP 进 SANs 或重生成）。
2. 备机防火墙放行 8443/TCP（**现网主机默认 DROP 对外，回退时须显式放开**）。
3. arb 侧 `.env` 改 `BINANCE_PROXY_URL`/`ARB_OKX_PROXY_URL`/`ARB_CEX_WS_BASE` 指向 `https://<备机IP>:8443`，
   且 `ARB_BINANCE_PROXY_CA` 指向备机 CA；`exchange_configs.base_url` 同步改裸 IP。
   **回切后必须改回域名**——裸 IP 在现网 iptables 下不可达，残留即 D-538 化石。

### 方案 C：Docker Compose

1. 复制 `docker-compose.yml`（业主补充）+ `config.json` 到备机；`docker compose up -d`。
2. CF tunnel 用同方案 A 的 token 接管。

## 演练清单（切换步骤）

主机故障时执行以下步骤切换到备机：

1. **确认主机故障**：`curl -sS https://cex.myfxlogs.org/ca-cert` 超时/非 200（`/ca-cert` 为公开
   探活路径，不碰上游不验令牌），且代理机侧 `systemctl status cex-proxy cloudflared`
   确认非单进程抖动。
2. **拉起备机**：方案 A——备机 `cold-standby.sh start` + `cloudflared tunnel run`。
3. **验证备机**：同上 curl 经域名返回 200（CF 已把隧道流量落备机）。
4. **切换 arb 指向**：方案 A 无需改动；方案 B 按上文改 `.env` + `exchange_configs`。
5. **重启 arb core**：`docker compose restart arb-core`（compose 服务名=arb-core）。
6. **验证通道恢复**：`mt-health` gate/okx/binance 报价恢复秒级新鲜；`docker logs arb-core --since 5m` 无 venue EOF。
7. **演练记录**：记录切换耗时（目标 <5 分钟）、遇到的问题、改进项。

## 回切主机

主机恢复后：
1. 停备机 `cloudflared` + `cold-standby.sh stop`；主机 `cloudflared` 重新接管 named tunnel。
2. 方案 B 走过的：`exchange_configs`/`.env` 改回 `cex.myfxlogs.org` 域名，`docker compose restart arb-core`。
3. 确认 `grep -r 192\.229` .env / DB 无裸 IP 残留（fossil-lint 同样护此签名）。

## 不做热备的理由

- CEX 断连 = fail-closed（不开新仓），不会产生错误交易
- CEX 仓位有 broker 端生存期（仓位不会因 arb 断连而消失）
- 分钟级切换可容忍（套利机会非连续，错过不亏钱）
- 热备需状态同步（连接池/会话/证书一致性），复杂度不匹配收益

## 业主待补充

- [ ] 备机硬件/网络准备（loopback 服务 + cloudflared 即可，无需公网入站端口）
- [ ] 主机 `cloudflared` tunnel token/ingress 配置的安全快照位置（与 token 同密级管理）
- [ ] `docker-compose.yml`（若选方案 C）
- [ ] 首次演练日期 + 演练记录归档位置
