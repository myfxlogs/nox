// Package okxrate —— okx REST 调用面共额退避闸（429 收敛票——balance/earn/dci
// 多面共额病灶根修）。
//
// 病灶实证：旧闸只罩 reconcile 子账户签名 GET 一面且按 (凭据,path) 分键——
// venue 限频档=User ID/IP 级共享预算，balance/earn/dci 三面各自连发时单面
// 档位不超而总频爆额=429 风暴（earn 读径连败实证）。本件收口两面：
//
//  1. 每端点最小时隙（per-path next 预约，钉 sfp/dcd order-history 1req/s 档）；
//  2. 全局共额罚窗——任一路径撞 429→全局 penalty 倍增并置 globalUntil，
//     窗内全路径调用直返 ErrThrottled 不发不占（多面共额语义）；成功调用
//     penalty 减半衰减，窗到期自然放行。
//
// skip 语义=如实报错留痕（调方 error+log 照走，不吞 429）；参数三键
// app_config（NOX_OKX_GET_MIN_INTERVAL/429_BACKOFF/429_BACKOFF_MAX，boot
// overlay 经 Configure 一次性注入，缺省仅代码兜底）。
package okxrate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ErrThrottled 退避期跳过标——调方凭此类可识别「未发出」非 venue 响应。
var ErrThrottled = errors.New("okx GET throttled (429 backoff)")

var gate = struct {
	sync.Mutex
	min, backoff, max time.Duration
	paths             map[string]*pathGate
	globalUntil       time.Time     // 共额罚窗：任一面 429→全面退避至此时
	penalty           time.Duration // 全局罚量（成功减半/429 倍增）
}{
	min: 1100 * time.Millisecond, backoff: 2 * time.Second, max: 60 * time.Second,
	paths: map[string]*pathGate{},
}

// pathGate 单端点态：next=下一可发时点。
type pathGate struct {
	next time.Time
}

// Configure boot overlay 注入参数（无热更新——组件使用前一次性生效同 app_config
// overlay 全体同约束）。
func Configure(min, backoff, backoffMax time.Duration) {
	gate.Lock()
	defer gate.Unlock()
	if min > 0 {
		gate.min = min
	}
	if backoff > 0 {
		gate.backoff = backoff
	}
	if backoffMax > 0 {
		gate.max = backoffMax
	}
}

// Wait 预约时隙并等待。共额罚窗内/端点罚距超限→直返 ErrThrottled 不发不占
// （如实跳过留痕非吞——调方 error+log 照走）。
func Wait(ctx context.Context, key string) error {
	gate.Lock()
	g := gate.paths[key]
	if g == nil {
		g = &pathGate{}
		gate.paths[key] = g
	}
	now := time.Now()
	if rem := gate.globalUntil.Sub(now); rem > 0 {
		gate.Unlock()
		return fmt.Errorf("%w (shared %.1fs left)", ErrThrottled, rem.Seconds())
	}
	rem := g.next.Sub(now)
	if rem > gate.min {
		gate.Unlock()
		return fmt.Errorf("%w (%.1fs left)", ErrThrottled, rem.Seconds())
	}
	// 预约时隙=上个时隙续排（非 now——否则排队者挤同一时隙）。
	slot := g.next
	if slot.Before(now) {
		slot = now
	}
	g.next = slot.Add(gate.min)
	gate.Unlock()
	if rem <= 0 {
		return nil
	}
	t := time.NewTimer(rem)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Result 回执同步：429→全局罚量倍增+共额罚窗置位（封顶 max）；成功→罚量减半
// 衰减；其它错不动罚（时隙已占，venue 故障不叠加限频语义）。
func Result(err error) {
	gate.Lock()
	defer gate.Unlock()
	now := time.Now()
	switch {
	case err == nil:
		gate.penalty /= 2
	case Is429(err):
		p := gate.penalty * 2
		if p < gate.backoff {
			p = gate.backoff
		}
		if p > gate.max {
			p = gate.max
		}
		gate.penalty = p
		gate.globalUntil = now.Add(p)
	}
}

// Is429 429 判形：报文体含「http 429」或「HTTP 429」（两族调用径报文壳
// 大小写不一——reconcile q.get 小写形/cexscan okxGet 大写形）或 venue
// 50011 码（HTTP 200 携 50011 业务码同频限语义）。
func Is429(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "http 429") || strings.Contains(s, "HTTP 429") ||
		strings.Contains(s, `"50011"`) || strings.Contains(s, "code=50011") || strings.Contains(s, "code\": \"50011\"")
}
