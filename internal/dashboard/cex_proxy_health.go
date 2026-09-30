// CexProxyHealthMonitor CEX 静态代理健康钉（D-301b S1）。
// 背景：三家 CEX REST 全走单点静态代理（BINANCE_PROXY_URL），代理 VPS 挂=CEX 全盲，
// 此前只能靠下游 ticks 断流间接发现。探测走透传路径 GET {ep}/fapi/v1/ping
// （httpx.Client 注入 CA+token）——一次探测同时验证 代理活+鉴权通+上游可达。
// 状态机：连续 3 败 → critical 一条（同态不重发，连败计数仍逐 tick 记日志）；恢复 → info recovered。
// backup（BINANCE_PROXY_URL_BACKUP）非空时一并探测；告警文案带对端状态行——
// primary dead+backup alive=可切换信号、两端皆 dead=全盲，显式区分（D-301b §2）。
// BINANCE_PROXY_URL 空 → 不监控（构造返回 nil，warn 一次，fail-open 不阻塞启动）。
// v1 只探测+告警+位预留，不自动切换。
package dashboard

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"arb/internal/httpx"
)

const (
	cexProxyPingPath    = "/fapi/v1/ping"
	cexProxyDeadAfter   = 3 // 连续失败阈值（防单次瞬断误报）
	cexProxyPollTimeout = 5 * time.Second
	cexProxyTick        = 60 * time.Second
)

// CexProxyHealthMonitor 静态代理健康监控器（装配范式同 TableFreshnessMonitor）。
type CexProxyHealthMonitor struct {
	insert  alertInserter
	client  *http.Client
	primary string
	backup  string // 空=单端
	tick    time.Duration

	mu        sync.Mutex
	consec    map[string]int  // endpoint → 连续失败数
	deadNoted map[string]bool // endpoint → crit 已发（同态不重发）
	lastOK    map[string]bool // endpoint → 最近一次探测结果（对端状态行用）
}

// NewCexProxyHealthMonitor 从 env 构造：BINANCE_PROXY_URL 空 → nil（不监控，fail-open）；
// BINANCE_PROXY_URL_BACKUP 空 = 单端。客户端复用 httpx.Client（CA+token 单源），超时压到 5s。
func NewCexProxyHealthMonitor(insert alertInserter) *CexProxyHealthMonitor {
	primary := os.Getenv("BINANCE_PROXY_URL")
	if primary == "" {
		log.Printf("[cex-proxy-health] BINANCE_PROXY_URL 未配置 → 不监控（fail-open）")
		return nil
	}
	client := httpx.Client()
	client.Timeout = cexProxyPollTimeout
	return &CexProxyHealthMonitor{
		insert:    insert,
		client:    client,
		primary:   primary,
		backup:    os.Getenv("BINANCE_PROXY_URL_BACKUP"),
		tick:      cexProxyTick,
		consec:    make(map[string]int),
		deadNoted: make(map[string]bool),
		lastOK:    make(map[string]bool),
	}
}

// Run 轮询直至 ctx 取消（模式同 TableFreshnessMonitor.Run）。
func (m *CexProxyHealthMonitor) Run(ctx context.Context) {
	m.poll(ctx)
	ticker := time.NewTicker(m.tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.poll(ctx)
		}
	}
}

// poll 一轮探测（primary 及非空 backup）+ 状态机转换 + 告警 + 日志。
func (m *CexProxyHealthMonitor) poll(ctx context.Context) {
	for _, ep := range m.endpoints() {
		ok, httpStatus := m.ping(ctx, ep)
		m.mu.Lock()
		m.lastOK[ep] = ok
		if ok {
			wasDead := m.deadNoted[ep]
			m.consec[ep] = 0
			m.deadNoted[ep] = false
			m.mu.Unlock()
			if wasDead {
				m.alert(ctx, "info", fmt.Sprintf("cex-proxy 通道 %s 已恢复", ep))
			}
			continue
		}
		m.consec[ep]++
		n := m.consec[ep]
		first := n >= cexProxyDeadAfter && !m.deadNoted[ep]
		if first {
			m.deadNoted[ep] = true
		}
		m.mu.Unlock()
		log.Printf("[cex-proxy-health] %s poll fail consec=%d http=%d", ep, n, httpStatus) // 同态不重发，计数逐 tick 可见
		if first {
			if httpStatus > 0 {
				m.alert(ctx, "critical", fmt.Sprintf("cex-proxy 通道 %s 探测被拒（上游 HTTP %d，连续 %d 次——代理活着但上游不通）；%s", ep, httpStatus, n, m.peerLine(ep)))
			} else {
				m.alert(ctx, "critical", fmt.Sprintf("cex-proxy 通道 %s 连接失败（连续 %d 次）；%s", ep, n, m.peerLine(ep)))
			}
		}
	}
}

// endpoints 本轮探测序：primary 在前，backup 非空在后。
func (m *CexProxyHealthMonitor) endpoints() []string {
	if m.backup == "" {
		return []string{m.primary}
	}
	return []string{m.primary, m.backup}
}

// peerLine 对端状态行：primary 的对端=backup、backup 的对端=primary（空=n/a）。
func (m *CexProxyHealthMonitor) peerLine(ep string) string {
	peer, label := m.backup, "备通道"
	if ep != m.primary {
		peer, label = m.primary, "主通道"
	}
	if peer == "" {
		return label + "未配置"
	}
	if m.lastOK[peer] {
		return label + " " + peer + " 存活"
	}
	return label + " " + peer + " 同样失败"
}

// ping GET {ep}/fapi/v1/ping，5s 超时（client.Timeout）；返回 (活, 败因HTTP码)——
// conn err/超时 → (false,0)；拿到响应非 200 → (false,code)；200 → (true,0)（D-589 分级）。
func (m *CexProxyHealthMonitor) ping(ctx context.Context, ep string) (bool, int) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep+cexProxyPingPath, nil)
	if err != nil {
		return false, 0
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return false, 0
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body) // 排干小响应体，保 keep-alive 复用
	if resp.StatusCode != http.StatusOK {
		return false, resp.StatusCode
	}
	return true, 0
}

// alert 插告警 + 日志（插入失败只记日志，不打断监控循环）。
func (m *CexProxyHealthMonitor) alert(ctx context.Context, level, msg string) {
	if err := m.insert.InsertAlert(ctx, nil, level, msg); err != nil {
		log.Printf("[cex-proxy-health] alert insert: %v", err)
	}
	log.Printf("[cex-proxy-health] %s: %s", level, msg)
}
