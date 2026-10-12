// EgressProbe 出口直连链路健康探针（D-011 S1；看门狗合法形态：只检测+告警，不搬数据不修链路）。
// 实证事故：mihomo.service dead+disabled → 裸奔出口对 www.okx.com / platform.cmcmarkets.com /
// api.gateio.ws 选择性 RST/超时 → 五张表各自喊滞后，无人说出「出口死了」根因。
// 每 5min 探三域直连依赖面 + cex-proxy 代理路径；三域全灭且代理可达 → 单条聚合根因 critical
// 替代各表滞后散报（1h 去重，lastCritAlert 模式）；探活恢复 → info。单域灭走既有 per-venue
// 路径不在此误报。per-domain 状态机复用 dephealth（D-579 键空间 egress:* 自注册进
// ListSourceHealth；3 连败=degraded 兼单次瞬断防误报），聚合判定读同一状态源不另记数。
// 归因逻辑：cex-proxy（免墙 VPS）可达而三个需 mihomo 的域全灭 → mihomo/上游网络中断；
// 代理也不可达=主机网络级故障，归因不成立不聚合（cex-proxy 健康钉 D-301b 另有专道）。
package dashboard

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"nox/internal/infra/dephealth"
	"nox/internal/platform/httpx"
)

const (
	egressProbeTick   = 5 * time.Minute
	egressCritRealert = time.Hour // 聚合 critical 持续同态 1h 重发一条（D-011：同表健康监控同源 1h 去重）
	egressPollTimeout = 10 * time.Second
)

// egressTarget 单个直连依赖面探测项。
type egressTarget struct {
	key   string // dephealth 键（egress:<domain>:rest）
	url   string // 探活端点（公开、零签名、最小响应）
	alias string // 告警/日志域名别名
}

// egressTargets 直连依赖面探测清单=当前直连依赖面的如实枚举（D-011 边界）：
// 新增直连依赖（新 venue 直连 REST 面）时在此同步扩列并注依赖方。
var egressTargets = []egressTarget{
	{"egress:okx:rest", "https://www.okx.com/api/v5/public/time", "okx"},   // 依赖：cexscan spread/earn 直连采集
	{"egress:gate:rest", "https://api.gateio.ws/api/v4/spot/time", "gate"}, // 依赖：gate spot WS 直连同域 REST 等价（ws.gate.io 是 WS 域不可 HTTP 探）
}

// egressSink 状态面注入口（默认 dephealth 适配；测试注入 fake 隔离全局注册表）。
type egressSink interface {
	Mark(key string, ok bool, err error)
	Snapshot() []dephealth.Row
}

type dephealthSink struct{}

func (dephealthSink) Mark(key string, ok bool, err error) { dephealth.Mark(key, ok, err) }
func (dephealthSink) Snapshot() []dephealth.Row           { return dephealth.Snapshot() }

// EgressProbe 出口直连链路健康探针。
type EgressProbe struct {
	insert   alertInserter
	client   *http.Client
	sink     egressSink
	targets  []egressTarget
	proxyURL string // cex-proxy 基址（BINANCE_PROXY_URL；空=聚合归因失效，只记 per-domain）
	tick     time.Duration
	now      func() time.Time

	mu          sync.Mutex
	outage      bool      // 聚合 critical 已发、待探活恢复确认
	outageSince time.Time // 本次全断判定起点（恢复文案算中断时长）
	lastCrit    time.Time // 1h 去重锚（恢复时一并清零——新一轮全断首轮即警，不吞进冷却窗）
}

// NewEgressProbe 装配（proxyURL 空 → 聚合归因失效，fail-open 只记 per-domain）。
// D-145：proxyURL 由装配层 cfg 注入（原 env 直读退役）。
func NewEgressProbe(insert alertInserter, proxyURL string) *EgressProbe {
	c := httpx.Client()
	c.Timeout = egressPollTimeout
	proxyURL = strings.TrimRight(proxyURL, "/")
	if proxyURL == "" {
		log.Printf("[egress-probe] 代理端点未配置 → 聚合归因失效（只记 per-domain dephealth）")
	}
	return &EgressProbe{
		insert:   insert,
		client:   c,
		sink:     dephealthSink{},
		targets:  egressTargets,
		proxyURL: proxyURL,
		tick:     egressProbeTick,
		now:      time.Now,
	}
}

// Run 轮询直至 ctx 取消（模式同 TableFreshnessMonitor.Run）。
func (p *EgressProbe) Run(ctx context.Context) {
	p.poll(ctx)
	ticker := time.NewTicker(p.tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

// poll 一轮：三域探活+记账 → 代理可达性 → 聚合判定。探测=链路级：HTTP 已到达（任何状态码）
// 即链路通（venue 侧语义错误归 per-venue 告警道）；传输层失败才算灭（D-011 实证 RST/超时形态）。
func (p *EgressProbe) poll(ctx context.Context) {
	now := p.now()
	var parts, dead []string
	anyAlive := false
	for _, tg := range p.targets {
		alive, status, perr := p.probe(ctx, tg.url)
		p.sink.Mark(tg.key, alive, perr)
		if alive {
			anyAlive = true
			parts = append(parts, fmt.Sprintf("%s=ok(%d)", tg.alias, status))
		} else {
			dead = append(dead, tg.alias)
			parts = append(parts, tg.alias+"=dead")
		}
	}
	proxyAlive := false
	if p.proxyURL != "" {
		proxyAlive, _, _ = p.probe(ctx, p.proxyURL+cexProxyPingPath)
		if proxyAlive {
			parts = append(parts, "proxy=ok")
		} else {
			parts = append(parts, "proxy=dead")
		}
	}
	log.Printf("[egress-probe] %s", strings.Join(parts, " "))
	p.aggregate(ctx, now, anyAlive, proxyAlive, dead)
}

// probe 单 URL 探活：返回 (alive, httpStatus, err)。
func (p *EgressProbe) probe(ctx context.Context, url string) (bool, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, 0, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return false, 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body) // 排干小响应体，保 keep-alive 复用（cex_proxy_health 先例）
	return true, resp.StatusCode, nil
}

// aggregate 聚合根因判定：三域全灭（sink 态全 degraded=3 连败，防单次瞬断）且代理可达 →
// critical（1h 去重重发）；任一域探活复活 → 恢复 info 并清冷却（下一轮全断首轮即警）。
func (p *EgressProbe) aggregate(ctx context.Context, now time.Time, anyAlive, proxyAlive bool, dead []string) {
	if anyAlive {
		p.mu.Lock()
		was, since := p.outage, p.outageSince
		p.outage, p.outageSince, p.lastCrit = false, time.Time{}, time.Time{}
		p.mu.Unlock()
		if was {
			p.alert(ctx, "info", fmt.Sprintf("出口直连链路已恢复（全断持续约 %s）——直连探活复活，出口链路可达", now.Sub(since).Truncate(time.Second)))
		}
		return
	}
	if !proxyAlive || len(dead) < len(p.targets) {
		return
	}
	allDegraded := true
	for _, row := range p.sink.Snapshot() {
		for _, tg := range p.targets {
			if row.Key == tg.key && row.State != dephealth.StateDegraded {
				allDegraded = false
			}
		}
	}
	if !allDegraded {
		return
	}
	p.mu.Lock()
	due := p.lastCrit.IsZero() || now.Sub(p.lastCrit) >= egressCritRealert
	if due {
		p.lastCrit = now
	}
	if due && !p.outage {
		p.outage, p.outageSince = true, now
	}
	p.mu.Unlock()
	if due {
		p.alert(ctx, "critical", fmt.Sprintf(
			"出口直连链路全断（mihomo 代理/上游网络疑似中断）——%d 个采集面受影响（%s 直连探活全灭，cex-proxy 通道仍可达）；排查提示：systemctl status mihomo && journalctl -u mihomo -n 50",
			len(p.targets), strings.Join(dead, "/")))
	}
}

// alert 插告警 + 日志（插入失败只记日志，不打断监控循环）。
func (p *EgressProbe) alert(ctx context.Context, level, msg string) {
	if err := p.insert.InsertAlert(ctx, nil, level, msg); err != nil {
		log.Printf("[egress-probe] alert insert: %v", err)
	}
	log.Printf("[egress-probe] %s: %s", level, msg)
}
