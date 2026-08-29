package adapter

// MT5 符号元数据采集（D-140 S2 + D-142 ③）。
// 发 mt_symbol_meta Fact（symbol=broker 原生，value=ContractSize，unit=contract_size）。
// D-142 ③：同时发 mt_symbol_margin Fact（value=品种级保证金率，unit=ratio）——
//   SymGroup.InitMarginRate[0]（long order 口径）。网关实拍不返回该字段（经验库
//   mt5-symgroup-margin-fields-not-served，2026-08-29 二次验证仍 len=0）→ 当前产出
//   零 margin facts，消费方④回落账户级 1/Leverage。网关升级后自动产出。
// CS 的自动来源：采集到的 CS 经 main 层 sink upsert 进 broker_symbol_map（仅填 NULL 行）。
// 复用 SwapFacts 已建立的 SymbolParamsMany 批量拉取路径，独立轮询不耦合 swap 标定。

import (
	"context"
	"log"
	"sync"
	"time"

	"arb/internal/fact"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
)

// metaPerCallTimeout 单符号元数据拉取超时（与 swap 同量级）。
const metaPerCallTimeout = 10 * time.Second

// MetaFacts 采集 MT5 符号元数据 Fact（D-140 S2）。
// 遍历 symMap 全部 broker symbol，SymbolParamsMany 批量拉取 ContractSize + Description。
// 返回 mt_symbol_meta Fact 列表（symbol=broker 原生，value=CS）。
// best-effort：单符号失败跳过不阻塞整体。
func (a *MT5Adapter) MetaFacts(ctx context.Context) ([]fact.Fact, error) {
	sm := a.symMap.Load()
	if sm == nil || len(*sm) == 0 {
		return nil, nil
	}
	tok := a.getToken()
	names := make([]string, 0, len(*sm))
	for brokerSym := range *sm {
		names = append(names, brokerSym)
	}
	// 批量拉取（SymbolParamsMany 一次取全部）
	pctx, cancel := context.WithTimeout(a.withSessionMD(ctx), 60*time.Second)
	defer cancel()
	resp, err := a.mt5.SymbolParamsMany(pctx, &mt5pb.SymbolParamsManyRequest{Id: tok, Symbol: names})
	if err != nil {
		return nil, err
	}
	if e := resp.GetError(); e != nil {
		return nil, errFromMT5(e)
	}
	results := resp.GetResult()
	var facts []fact.Fact
	now := time.Now().UTC()
	marginCount := 0
	for _, r := range results {
		si := r.GetSymbolInfo()
		sym := r.GetSymbol()
		if sym == "" || si == nil {
			continue
		}
		cs := si.GetContractSize()
		if cs > 0 {
			facts = append(facts, fact.Fact{
				Kind:   fact.KindMTSymbolMeta,
				Venue:  a.venue,
				Symbol: sym, // broker 原生符号
				Value:  cs,
				Unit:   fact.UnitContractSize,
				Ts:     now,
				Src:    "mt5",
			})
		}
		// D-142 ③：品种级保证金率——SymGroup.InitMarginRate[0]（long order 口径）。
		// 网关实拍不返回（经验库 mt5-symgroup-margin-fields-not-served），len=0 跳过。
		sg := r.GetSymbolGroup()
		if sg != nil {
			imr := sg.GetInitMarginRate()
			if len(imr) > 0 && imr[0] > 0 {
				facts = append(facts, fact.Fact{
					Kind:   fact.KindMTSymbolMargin,
					Venue:  a.venue,
					Symbol: sym,
					Value:  imr[0],
					Unit:   fact.UnitRatio,
					Ts:     now,
					Src:    "mt5",
				})
				marginCount++
			}
		}
	}
	log.Printf("[mt5-meta] %s: %d meta facts (CS) + %d margin facts", a.venue, len(facts)-marginCount, marginCount)
	return facts, nil
}

// errFromMT5 从 MT5 Error 构造 Go error（复用 mt_swap 同模式）。
func errFromMT5(e *mt5pb.Error) error {
	if e == nil {
		return nil
	}
	return &mt5Error{msg: e.GetMessage()}
}

type mt5Error struct{ msg string }

func (e *mt5Error) Error() string { return e.msg }

// _ 保留 sync 引用（MetaFacts 未来可能并发拉取时复用）
var _ = sync.WaitGroup{}
