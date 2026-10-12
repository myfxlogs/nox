package deribit

import (
	"sort"
	"strings"
	"time"

	"nox/internal/data/fact"
)

// ParityInput computeAllParity 的入参（可测性，无 IO）。
type ParityInput struct {
	Options []optBookSummary // 全期权链快照
	Futures []optBookSummary // 全期货链快照
	// Bases 采集标的（BTC/ETH）；ExpiryCount 取最近 N 个到期日。
	Bases       []string
	ExpiryCount int
}

// ParityOutput computeAllParity 的输出。
type ParityOutput struct {
	Facts    []fact.Fact // 落库用（opt_parity_dev）
	Results  []ParityResult
	Skipped  int // F 缺失跳过计数
	NoMkt    int // bid=ask=0 跳过计数
	OutOfATM int // ATM±10% 外跳过计数
}

// cpPair 一对 call/put（同 base/expiry/strike）。
// inst 只挂 put 侧（call/put strike 相同，Canonical 无 C/P 后缀，任一即可）。
type cpPair struct {
	call, put optBookSummary
	inst      optInstrument
	hasCall   bool
	hasPut    bool
}

// computeAllParity 纯函数：从期权+期货快照计算所有 (标的,到期,行权价) 的平价偏离。
// 流动性过滤：bid=ask=0 跳过；ATM±10% 外跳过（underlying 现算）。
// F 缺失（无同到期期货）→ 跳过+计数，不 panic。
// 最近 N 个到期日：按 YYYYMMDD 排序取前 N（字典序=日历序；DDMMMYY 字母序不成立——
// 日份无前导零 + 月份字母序错乱，见 TestExpirySortCalendar）。
func computeAllParity(in ParityInput, now time.Time) ParityOutput {
	var out ParityOutput
	for _, base := range in.Bases {
		// 1. 收集该标的的期权 instrument + 按到期(YYYYMMDD)分组
		//    分组键用 ExpiryYMD（YYYYMMDD）而非 Expiry（DDMMMYY）——字典序=日历序。
		pairsByExpiry := map[string][]cpPair{}
		for _, o := range in.Options {
			if o.BaseCurrency != base {
				continue
			}
			inst, ok := parseInstrument(o.InstrumentName)
			if !ok {
				continue
			}
			// 流动性过滤：bid=ask=0 跳过
			if o.BidPrice == 0 && o.AskPrice == 0 {
				out.NoMkt++
				continue
			}
			bucket := pairsByExpiry[inst.ExpiryYMD]
			// 查找同 strike 的对（call/put strike 相同 → 配对）
			found := false
			for i := range bucket {
				if bucket[i].inst.Strike == inst.Strike {
					if inst.IsCall && !bucket[i].hasCall {
						bucket[i].call = o
						bucket[i].hasCall = true
						found = true
						break
					}
					if !inst.IsCall && !bucket[i].hasPut {
						bucket[i].put = o
						bucket[i].hasPut = true
						// inst 只在首次挂腿时设置；strike 相同，Canonical 无 C/P 后缀，无需覆盖。
						if !bucket[i].hasCall {
							bucket[i].inst = inst
						}
						found = true
						break
					}
				}
			}
			if !found {
				p := cpPair{inst: inst}
				if inst.IsCall {
					p.call = o
					p.hasCall = true
				} else {
					p.put = o
					p.hasPut = true
				}
				bucket = append(bucket, p)
			}
			pairsByExpiry[inst.ExpiryYMD] = bucket
		}

		// 2. 取最近 N 个到期日（YYYYMMDD 字典序=日历序）
		expiries := make([]string, 0, len(pairsByExpiry))
		for e := range pairsByExpiry {
			expiries = append(expiries, e)
		}
		sort.Strings(expiries)
		n := in.ExpiryCount
		if n <= 0 {
			n = 2
		}
		if len(expiries) > n {
			expiries = expiries[:n]
		}

		// 3. 期货价映射（同到期，键=YYYYMMDD）
		futureByExpiry := map[string]float64{}
		for _, f := range in.Futures {
			if f.BaseCurrency != base {
				continue
			}
			// 期货 instrument 名：BTC-27SEP26（无 strike/-C/-P）
			parts := strings.Split(f.InstrumentName, "-")
			if len(parts) != 2 {
				continue
			}
			ymd := parseExpiry(parts[1])
			if ymd == "" {
				continue
			}
			futureByExpiry[ymd] = f.MarkPrice
		}

		// 4. 计算每对 call/put
		for _, expiry := range expiries {
			pairs := pairsByExpiry[expiry]
			futurePrice, hasFuture := futureByExpiry[expiry]
			if !hasFuture {
				out.Skipped += len(pairs)
				continue
			}
			// ATM 基准：用期货价作为 underlying 近似（期权 underlying_price per-contract 略有差异，
			// 但 ATM±10% 过滤用期货价即可——精度足够判定是否在带内）。
			atm := futurePrice
			for _, p := range pairs {
				if !p.hasCall || !p.hasPut {
					continue // 只有 call 或只有 put，无法配对
				}
				strike := p.inst.Strike
				// ATM±10% 过滤
				if atm > 0 {
					lo := atm * 0.9
					hi := atm * 1.1
					if strike < lo || strike > hi {
						out.OutOfATM++
						continue
					}
				}
				underlying := p.call.UnderlyingPrice
				if underlying <= 0 {
					underlying = p.put.UnderlyingPrice
				}
				r := computeParity(p.call, p.put, futurePrice, strike, underlying)
				r.Instrument = p.inst
				out.Results = append(out.Results, r)
				out.Facts = append(out.Facts, fact.Fact{
					Kind:   fact.KindOptParityDev,
					Venue:  Venue,
					Symbol: r.Instrument.Canonical,
					Value:  r.DevBps,
					Unit:   fact.UnitBps,
					Ts:     now,
					Src:    "deribit_public",
				})
			}
		}
	}
	return out
}
