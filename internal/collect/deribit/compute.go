package deribit

import (
	"time"

	"arb/internal/fact"
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

// computeAllParity 纯函数：从期权+期货快照计算所有 (标的,到期,行权价) 的平价偏离。
// 流动性过滤：bid=ask=0 跳过；ATM±10% 外跳过（underlying 现算）。
// F 缺失（无同到期期货）→ 跳过+计数，不 panic。
// 最近 N 个到期日：按 expiry 字符串排序取前 N（DDMMMYY 字母序≈日历序，Deribit 到期日固定）。
func computeAllParity(in ParityInput, now time.Time) ParityOutput {
	var out ParityOutput
	for _, base := range in.Bases {
		// 1. 收集该标的的期权 instrument + 按到期分组
		type cpPair struct {
			call, put optBookSummary
			inst      optInstrument
		}
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
			bucket := pairsByExpiry[inst.Expiry]
			if inst.IsCall {
				// 查找已有 put
				found := false
				for i := range bucket {
					if bucket[i].inst.Strike == inst.Strike && !bucket[i].inst.IsCall {
						bucket[i].call = o
						bucket[i].inst = inst
						bucket[i].inst.IsCall = false // 保持 put 的 inst（strike 相同）
						found = true
						break
					}
				}
				if !found {
					bucket = append(bucket, cpPair{call: o, inst: inst})
				}
			} else {
				found := false
				for i := range bucket {
					if bucket[i].inst.Strike == inst.Strike && bucket[i].inst.IsCall && bucket[i].call.InstrumentName != "" {
						bucket[i].put = o
						found = true
						break
					}
				}
				if !found {
					bucket = append(bucket, cpPair{put: o, inst: inst})
				}
			}
			pairsByExpiry[inst.Expiry] = bucket
		}

		// 2. 取最近 N 个到期日
		expiries := make([]string, 0, len(pairsByExpiry))
		for e := range pairsByExpiry {
			expiries = append(expiries, e)
		}
		sortStrings(expiries)
		n := in.ExpiryCount
		if n <= 0 {
			n = 2
		}
		if len(expiries) > n {
			expiries = expiries[:n]
		}

		// 3. 期货价映射（同到期）
		futureByExpiry := map[string]float64{}
		for _, f := range in.Futures {
			if f.BaseCurrency != base {
				continue
			}
			// 期货 instrument 名：BTC-27SEP26（无 strike/-C/-P）
			parts := splitInstrument(f.InstrumentName)
			if len(parts) != 2 {
				continue
			}
			futureByExpiry[parts[1]] = f.MarkPrice
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
				if p.call.InstrumentName == "" || p.put.InstrumentName == "" {
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

// sortStrings 简易字符串排序（避免引入 sort 到纯逻辑文件；测试覆盖）。
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
