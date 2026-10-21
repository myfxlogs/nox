// okx_rate_gate.go —— okx 签名 GET 节流闸（429 修，account-mon okx:earn 连败根修）。
// 病灶实证（SG 10-21）：sfp/dcd/order-history 文档档=1 req/s（User ID 规则），
// kick 合并窗内连发（同 drain 批内多 okx 键串行 pollVenueKeys）即撞 50011——
// 13min×22 次 first-poll error，cadence 本身远低于档位，败因=同秒连发非高频。
// 单闸注入点=okxSubaccountSignedGet（全部子账户签名 GET 同经此径）。
// 语义：同 (凭据,path) 请求强制 ≥minInterval 间距（锁内预约时隙+ctx 觉睡）；
// 429 撞单→penalty 自 base 倍增至 max（成功减半衰减），penalty 期超 minInterval
// 的等待量→不等直返错（如实跳过留痕非吞——调方 error+log 照走）；非 429 错
// 只占时隙不加罚。参数三键 app_config（boot overlay 注入，缺省仅代码兜底）。
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ErrOkxThrottled 429 退避期跳过标——调方凭此类可识别「未发出」非 venue 响应。
var ErrOkxThrottled = errors.New("okx GET throttled (429 backoff)")

var okxGate = struct {
	sync.Mutex
	min, backoff, max time.Duration
	paths             map[string]*okxPathGate
}{
	min: 1100 * time.Millisecond, backoff: 2 * time.Second, max: 60 * time.Second,
	paths: map[string]*okxPathGate{},
}

// okxPathGate 单端点态：next=下一可发时点；penalty=429 罚量（0=无罚）。
type okxPathGate struct {
	next    time.Time
	penalty time.Duration
}

// ConfigureOkxRateGate boot overlay 注入参数（main.go 一行——无热更新，
// 与 app_config overlay 全体同约束：组件使用前一次性生效）。
func ConfigureOkxRateGate(min, backoff, backoffMax time.Duration) {
	okxGate.Lock()
	defer okxGate.Unlock()
	if min > 0 {
		okxGate.min = min
	}
	if backoff > 0 {
		okxGate.backoff = backoff
	}
	if backoffMax > 0 {
		okxGate.max = backoffMax
	}
}

// okxGateWait 预约时隙并等待。退避期（罚量使等待 >min）→ 直返 ErrOkxThrottled
// 不发不占（429 须如实报错留痕——skip 语义见文件头）。
func okxGateWait(ctx context.Context, key string) error {
	okxGate.Lock()
	g := okxGate.paths[key]
	if g == nil {
		g = &okxPathGate{}
		okxGate.paths[key] = g
	}
	now := time.Now()
	rem := g.next.Sub(now)
	if rem > okxGate.min {
		okxGate.Unlock()
		return fmt.Errorf("%w (%.1fs left)", ErrOkxThrottled, rem.Seconds())
	}
	// 预约时隙=上个时隙续排（非 now——否则排队者挤同一时隙；基距+罚量）。
	slot := g.next
	if slot.Before(now) {
		slot = now
	}
	g.next = slot.Add(okxGate.min + g.penalty)
	okxGate.Unlock()
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

// okxGateResult 回执同步：429→罚量倍增（base 起，封顶 max）；成功→罚量减半衰减；
// 其它错不动罚（时隙已占，venue 故障不叠加限频语义）。
func okxGateResult(key string, err error) {
	okxGate.Lock()
	defer okxGate.Unlock()
	g := okxGate.paths[key]
	if g == nil {
		return
	}
	switch {
	case err == nil:
		g.penalty /= 2
	case isOkx429(err):
		p := g.penalty * 2
		if p < okxGate.backoff {
			p = okxGate.backoff
		}
		if p > okxGate.max {
			p = okxGate.max
		}
		g.penalty = p
		// 罚量立即生效：已预约的 next 若近在眼前，顺延到含罚窗口
		if soon := time.Now().Add(okxGate.min + p); g.next.Before(soon) {
			g.next = soon
		}
	}
}

// isOkx429 q.get 非 200 报错体含「http 429」子串判定（venue 50011 体随 HTTP 码同返）。
func isOkx429(err error) bool {
	return err != nil && strings.Contains(err.Error(), "http 429")
}
