// canary.go —— D-104：本机出口 canary（60s 看门狗拍）。
// 出口健康是网络路径自身属性，无任何 WS/推送源可订阅，主动拨测为唯一观测形态
// （D-004 例外已留痕 decisions.md D-104：用途限制=自盯告警聚合+复盘，不进交易链）。
// 三锚点 TLS 拨测（cex 代理域/MT 桥外网面/公共互联网；env NOX_EGRESS_CANARY_ANCHORS
// 可覆盖）；连续 2 拍全锚失败=红窗确认（首拍只更新内态——单拍瞬时抖动零告警）：
// 单条 critical「本机出口故障，所有 venue 面按 fail-closed 对待」（30min 去重）+
// 恢复单条 info，红窗态经 EgressRed 供装配层聚合 ws-storm/面判定劣化告警；
// 每拍结果落 facts（kind=egress_canary）供复盘。
package selfwatch

import (
	"context"
	"crypto/tls"
	"log"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"nox/internal/data/fact"
)

// cfgEgressAnchors 锚点 env 覆盖（逗号分隔 host:port）；缺省=D-104 票面三锚。
const cfgEgressAnchors = "NOX_EGRESS_CANARY_ANCHORS"

// canaryCriticalMsg D-104 红窗单条聚合 critical（聚合闸=EgressRed，见 cmd/core）。
const canaryCriticalMsg = "[canary] 本机出口故障，所有 venue 面按 fail-closed 对待"

// canaryRecoverMsg D-104 复审返修②：红窗恢复单条 info（业主收到 critical 后有恢复信号）。
const canaryRecoverMsg = "[canary] 本机出口恢复"

// canaryInterval 看门狗拍间隔；canaryDialTimeout 单锚拨测超时（三锚串行最坏 15s<60s）。
const (
	canaryInterval    = 60 * time.Second
	canaryDialTimeout = 5 * time.Second
)

// defaultEgressAnchors D-104 三锚：cex 代理域（CF 边缘）/MT 桥外网面/公共互联网。
var defaultEgressAnchors = []string{
	"cex.myfxlogs.org:443",
	"mt5grpc3.mtapi.io:443",
	"www.cloudflare.com:443",
}

// egressRed 进程内红窗态（每拍刷新；进程重启归零 fail-open=宁多告不漏告）。
var egressRed atomic.Bool

// EgressRed 报告 canary 红窗态（cmd/core 告警聚合闸读）。
func EgressRed() bool { return egressRed.Load() }

// SetEgressRed 设置红窗态（canaryRound 写；测试注入用）。
func SetEgressRed(red bool) { egressRed.Store(red) }

// dialAnchor 单锚拨测（var=测试注入点；生产=TLS 握手成功即出口通——
// HTTP/gRPC 混合端点统一形态，能完成握手=DNS+TCP+TLS 全链通）。
var dialAnchor = func(addr string) error {
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: canaryDialTimeout}, "tcp", addr, nil)
	if err != nil {
		return err
	}
	return conn.Close()
}

// egressAnchors 锚点集（app_config 行覆盖，trim 空段；行只含分隔符=空集→canary
// 停用显式报缺；行缺→内置默认。D-145 env 收口——store=PG 权威读）。
func egressAnchors(ctx context.Context, d Deps) []string {
	raw, ok, err := d.Store.AppConfigString(ctx, cfgEgressAnchors)
	if err != nil {
		log.Printf("[canary] %s 读取失败（按缺省锚点集跑）: %v", cfgEgressAnchors, err)
	}
	if !ok || raw == "" {
		return defaultEgressAnchors
	}
	var out []string
	for _, a := range strings.Split(raw, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// probeEgress 一拍拨测：串行拨各锚，返回通过数（全败=0 即红窗拍）。
func probeEgress(anchors []string) int {
	ok := 0
	for _, a := range anchors {
		if err := dialAnchor(a); err != nil {
			log.Printf("[canary] %s 拨测失败: %v", a, err)
			continue
		}
		ok++
	}
	return ok
}

// canaryLoop 常驻：启动即拍 → 60s 定点拍（看门狗形态：timer 定点唤醒，拍内全量重测，
// 不缓存上拍结果作现值）。
func canaryLoop(ctx context.Context, d Deps) {
	anchors := egressAnchors(ctx, d)
	if len(anchors) == 0 {
		log.Printf("[canary] %s 空——锚点集缺，出口 canary 停用（显式报缺）", cfgEgressAnchors)
		return
	}
	red, confirmed := false, false
	for {
		red, confirmed = d.canaryRound(ctx, anchors, red, confirmed)
		t := time.NewTimer(canaryInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// canaryRound 一拍：拨测→fact 落库（值=通过锚数）→红窗状态机告警→刷新进程内红窗态。
// 状态机（D-104 复审返修①②）：绿→疑似（首拍全红只更新内态，零告警）；疑似+再拍全红
// =红窗确认→单条 critical（每次红窗恰一条，30min 去重窗兼抑抖动重确认）→聚合闸随确认
// 同步；确认红窗持续零重发（恢复 info 收口）；确认红窗后翻绿→单条恢复 info；疑似单拍
// 红后翻绿=静默收场（无 critical 无 info——15s 级瞬时抖动全程零声，降噪目标本义）。
func (d Deps) canaryRound(ctx context.Context, anchors []string, prevRed, prevConfirmed bool) (red, confirmed bool) {
	okN := probeEgress(anchors)
	red = okN == 0
	confirmed = red && (prevRed || prevConfirmed)
	if err := d.Store.InsertFacts(ctx, []fact.Fact{{
		Kind: fact.KindEgressCanary, Venue: "local:egress",
		Value: float64(okN), Unit: fact.UnitCount, Ts: d.now(), Src: "egress_canary",
	}}); err != nil {
		log.Printf("[canary] fact 落库失败: %v", err)
	}
	if confirmed && !prevConfirmed {
		if err := d.Store.InsertAlertDedup(ctx, "critical", canaryCriticalMsg, 30*time.Minute); err != nil {
			log.Printf("[canary] alert: %v", err)
		}
	} else if prevConfirmed && !red {
		if err := d.Store.InsertAlertDedup(ctx, "info", canaryRecoverMsg, 30*time.Minute); err != nil {
			log.Printf("[canary] alert: %v", err)
		}
	}
	egressRed.Store(confirmed)
	return red, confirmed
}
