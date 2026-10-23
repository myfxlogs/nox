// okx_ws_restpoll.go —— D-665：okx 宇宙面收窄 REST 拍。
// tickers ws 全宇宙订阅 ~150KB/s=单 conn 最肥面之一；改 exec-face instId ws
// （armed∪毕业∪在仓 1s 鲜度全保）+REST /api/v5/market/tickers 15min 拍
// 宇宙全集（发现面消费时标≥15min 对版——REST 拍行数=原 ws instId 集
// 覆盖率不降）。
package exchange

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/fact"
	"nox/internal/platform/rejects"
)

// okxUniversePollEvery D-665：REST 宇宙拍周期（paper_mtm_tick_max_age=5min 闸对齐——拍后 3min 窗内可估；15min 立票错判按纸闸代码为准）。
const okxUniversePollEvery = 3 * time.Minute

// runOKXTickerPoll okx 宇宙 tickers REST 轮询（SWAP+SPOT 两 instType 全谱——
// 发现面覆盖率不降锚）。publish 语义=ws tickers 同径（instId→canonical→qbus）。
func (o *OKXWebSocket) runOKXTickerPoll(ctx context.Context, publish func(bus.Quote)) {
	client := o.cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	base := strings.TrimRight(o.cfg.OKXBaseURL, "/")
	if base == "" {
		base = "https://www.okx.com"
	}
	for {
		for _, it := range []struct{ instType, vt string }{{"SWAP", "perp"}, {"SPOT", "spot"}} {
			var out struct {
				Code string `json:"code"`
				Data []struct {
					InstID string `json:"instId"`
					BidPx  string `json:"bidPx"`
					AskPx  string `json:"askPx"`
					Ts     string `json:"ts"`
				} `json:"data"`
			}
			url := base + "/api/v5/market/tickers?instType=" + it.instType
			if err := getJSONInto(ctx, client, url, &out); err != nil {
				log.Printf("[okx-ws] universe ticker %s: %v", it.instType, err)
				continue
			}
			if out.Code != "0" {
				log.Printf("[okx-ws] universe ticker %s code=%s", it.instType, out.Code)
				continue
			}
			for _, d := range out.Data {
				venueSymbol := d.InstID
				sym := strings.TrimSuffix(venueSymbol, "-SWAP")
				canonical := strings.ReplaceAll(sym, "-", "")
				var bid, ask float64
				if _, err := fmt.Sscanf(d.BidPx, "%f", &bid); err != nil || bid == 0 {
					continue
				}
				if _, err := fmt.Sscanf(d.AskPx, "%f", &ask); err != nil || ask == 0 {
					continue
				}
				ts := time.Now()
				if t, err := parseOKXTS(d.Ts); err == nil {
					ts = t
				}
				publish(bus.Quote{Symbol: canonical, Venue: VenueOKX, VenueSymbol: venueSymbol,
					Bid: bid, Ask: ask, Time: ts, VenueType: it.vt})
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(okxUniversePollEvery):
		}
	}
}

// execSyms D-665：ws 订阅集收窄至执行面（armed∪毕业∪在仓 okx 腿——
// ExecFaceVenueFn 返回 canonical→vt，跨 60s 差集生效；nil/err=空集
// 零订非静默——宇宙面由 runOKXTickerPoll REST 拍接管覆盖率不降）。
func (o *OKXWebSocket) execSyms(ctx context.Context) []string {
	out, last := execFaceDesired(ctx, o.cfg, "okx", o.execFaceLastGood)
	o.execFaceLastGood = last
	return out
}

// syms D-369 S2：okx WS 订阅符号集（env ∪ provider 动态集，连接期可增量）。
func (o *OKXWebSocket) syms() []string { return o.cfg.effectiveSymbols("okx") }

// SetFactEmit 注入 fact 发射口（wiring 期；先于 Run）。
func (o *OKXWebSocket) SetFactEmit(fn func(ctx context.Context, fs []fact.Fact) error) {
	o.emitFacts = fn
}

func init() {
	rejects.Register("okx_indiv", "execface_err", "budget_err", "budget_cap", "monitorface_err")
	rejects.Register("gate_indiv", "execface_err", "budget_err", "budget_cap", "monitorface_err")
}

// execFaceDesired D-665 返修②③：venue exec 集取径——ExecFaceVenueFn err=
// 保 lastGood 非静默空拆（D-663 残③同族病灶跨 venue 泛化丢件修）；
// IndivBudgetFn cap 闸同过（复用 NOX_INDIV_MAX_CONNS——宇宙集病态返
// 604KB/s 事故同构防）。caller goroutine 持 lastGood 快照（venue 内
// 单 loop 串行调用面；gate 两 loop 各持自快照无共享态）。
func execFaceDesired(ctx context.Context, cfg Config, venue string, lastGood []string) ([]string, []string) {
	if cfg.ExecFaceVenueFn == nil {
		return nil, lastGood
	}
	face, err := cfg.ExecFaceVenueFn(ctx, venue)
	if err != nil {
		rejects.Inc(rejects.Key{Face: venue + "_indiv", Gate: "execface_err"})
		return lastGood, lastGood
	}
	// 占额序=exec 先、monitor 填余（armed/在仓腿不被观测集挤掉——返修①修）。
	exec := make([]string, 0, len(face))
	for sym := range face {
		exec = append(exec, sym)
	}
	sort.Strings(exec)
	var mon []string
	if cfg.MonitorFaceFn != nil {
		mset, merr := cfg.MonitorFaceFn(ctx)
		if merr != nil {
			rejects.Inc(rejects.Key{Face: venue + "_indiv", Gate: "monitorface_err"}) // 返修②残随修：读败如实计非静默缩 desired
		} else {
			for sym, v := range mset {
				if v == venue { // 监控观测集按 venue 分流（carry 腿=perp 侧盯）
					if _, dup := face[sym]; !dup {
						mon = append(mon, sym)
					}
				}
			}
			sort.Strings(mon)
		}
	}
	out := append(exec, mon...)
	lastGood = out
	if cfg.IndivBudgetFn == nil {
		return out, lastGood
	}
	if cap, ok, berr := cfg.IndivBudgetFn(ctx); berr != nil {
		rejects.Inc(rejects.Key{Face: venue + "_indiv", Gate: "budget_err"})
		return out, lastGood
	} else if !ok {
		// 键缺=执行面订阅显式关（D-663 同语义——宇宙 REST 拍仍接管发现面）。
		return nil, lastGood
	} else if cap < 0 {
		cap = 0
	} else if len(out) > int(cap) {
		out = out[:cap] // 序截断保 exec 先占（monitor 集尾部优先让额）
		rejects.Inc(rejects.Key{Face: venue + "_indiv", Gate: "budget_cap"})
		if cfg.AlertFn != nil {
			cfg.AlertFn("warn", fmt.Sprintf("[%s-ws] desired %d 超预算 cap=%d——监控集让额于 exec 先占", venue, len(exec)+len(mon), cap))
		}
	}
	return out, lastGood
}

// fundingSubArgs D-665 返修④：funding-rate 订阅集与执行面解耦——tickers
// 收窄执行面后，funding 仍须消费面全谱（FundingRegimeOK 候选闸对非执行面
// 品种结构性瞎眼实证：BTCUSDT stale 7h+；帧稀疏廉价不违 ≤200KB/s 锚）。
// 集源=fundingSyms（o.syms() 原动态消费面——env∪provider）；
// subscribed 键域 "funding-rate:" 前缀与 tickers 键域隔离。
func (o *OKXWebSocket) fundingSubArgs(fundingSyms []string, subscribed map[string]bool) []map[string]string {
	var args []map[string]string
	for _, sym := range fundingSyms {
		_, perpID := okxSubCandidates(sym)
		if perpID == "" {
			continue
		}
		_, perpID = o.catalog.pick("", perpID)
		if perpID == "" {
			continue
		}
		key := "funding-rate:" + perpID
		if subscribed[key] {
			continue
		}
		subscribed[key] = true
		args = append(args, map[string]string{"channel": "funding-rate", "instId": perpID})
	}
	return args
}

func parseOKXTS(s string) (time.Time, error) {
	var ms int64
	fmt.Sscanf(s, "%d", &ms)
	if ms == 0 {
		return time.Time{}, fmt.Errorf("bad ts")
	}
	return time.UnixMilli(ms), nil
}
