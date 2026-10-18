// ban_gate.go —— binance REST 面 418 感知全局退避闸（venue 级封禁状态件）。
// 病灶（SG 10-19 实证）：venue 418 报文含「IP banned until <epoch-ms>」明示期限，
// 但生产 REST 消费件（capitalview/premiumIndex/dualSide/account-mon/reconcile/
// listenKey 七族）封禁期每拍照发=撞击可能续杯=封禁永不自然解禁（20min×223 撞实拍）。
// 单闸注入点=httpx 共享 transport 层（全 bn REST 消费径同经此径——failover 只拦
// 传输层错，418=响应层 venue 报文，failoverRT 不拦）。
// 语义：418/-1003 → 解析 banned until T → 闸记至 T+margin；闸窗内 *.binance.com
// 请求一律短路 ErrBinanceBanned（如实跳过——调方 error+log 照旧留痕禁吞）；
// 429 轻退避同机制（30s 固定窗——指数窗无 venue 实据不造）；T 到期自然放行，
// 再撞再记（体张性）。WS 面零动（票面禁线）。
package httpx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// ErrBinanceBanned 闸窗内短路标——如实跳过义（调方凭此类可识别非 venue 响应）。
var ErrBinanceBanned = errors.New("binance REST banned (gate short-circuit)")

var (
	// banUntilUnixMs 共享出口 IP 级封禁戳（epoch-ms——binance 报文口径，
	// 全 *.binance.com 域同闸：封禁是 IP 维非域维）。0=未封。
	banUntilUnixMs atomic.Int64
	// banLogged 防闸窗内每轮刷「入闸」log——状态变迁才记（入闸一次/放行一次）。
	banLogged atomic.Bool
	banRe     = regexp.MustCompile(`banned until (\d+)`)
	banMargin = 5 * time.Second  // 解禁 margin（venue 边界 jitter 容差）
	ban429Win = 30 * time.Second // 429 轻退避窗（venue Retry-After 未携=固定短窗）
)

// bannedNow 闸窗判定——返回剩余封禁期（>0=在闸）。
func bannedNow() time.Duration {
	u := banUntilUnixMs.Load()
	if u == 0 {
		return 0
	}
	return time.Until(time.UnixMilli(u))
}

// recordBan 记封禁至 T（保留更长者——两件报期不一取严）。
func recordBan(until time.Time) {
	for {
		cur := banUntilUnixMs.Load()
		if cur >= until.UnixMilli() || banUntilUnixMs.CompareAndSwap(cur, until.UnixMilli()) {
			break
		}
	}
	if banLogged.CompareAndSwap(false, true) {
		log.Printf("[httpx-ban] binance REST 面入闸至 %s（418/-1003 报文期限+margin）", until.UTC().Format("15:04:05Z"))
	}
}

type banGateRT struct{ base http.RoundTripper }

func (g banGateRT) RoundTrip(r *http.Request) (*http.Response, error) {
	isBn := strings.HasSuffix(r.URL.Host, "binance.com")
	if isBn {
		if rem := bannedNow(); rem > 0 {
			return nil, fmt.Errorf("%w (%.0fs left)", ErrBinanceBanned, rem.Seconds())
		}
		// 出闸瞬（自然放行）——状态变迁一记
		if banLogged.CompareAndSwap(true, false) {
			log.Printf("[httpx-ban] binance REST 面解禁放行（封禁期限到点）")
		}
	}
	resp, err := g.base.RoundTrip(r)
	if err != nil || !isBn || resp == nil {
		return resp, err
	}
	if resp.StatusCode == 418 {
		// 报文「IP(... ) banned until <ms>」解析（读体后回包——调方 raw 照常可解）
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(body))
		if m := banRe.FindSubmatch(body); len(m) == 2 {
			if ms, e := strconv.ParseInt(string(m[1]), 10, 64); e == nil {
				recordBan(time.UnixMilli(ms).Add(banMargin))
			}
		} else {
			// 期限未携=保守短窗（venue -1003 无语义报文——60s 实证粒度缺省）
			recordBan(time.Now().Add(60 * time.Second))
		}
		return resp, nil
	}
	if resp.StatusCode == 429 {
		recordBan(time.Now().Add(ban429Win)) // 轻退避同机制
	}
	return resp, nil
}

// newBanGateRT 装配闸——嵌 failoverRT 外（ban 语义在响应层，先于 transport 尝试）。
func newBanGateRT(base http.RoundTripper) http.RoundTripper {
	return banGateRT{base: base}
}
