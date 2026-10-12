package dashboard

import (
	"context"
	"fmt"
	"log"
	"time"

	"nox/internal/data/adapter"
	"nox/internal/data/fact"
	"nox/internal/data/keydomain"
	"nox/internal/data/marketstate"
	"nox/internal/ledger/reconcile"
	"nox/internal/platform/safego"
	"nox/internal/store/pgstore"
	"nox/internal/trading/exec"

	"google.golang.org/grpc/metadata"
	mt5pb "nox/internal/data/adapter/gen/mtapi/mt5"
)

// startAdapter 平台分发：构造适配器 → 连接 → 订阅 → 注册执行器/querier。
func (m *BrokerManager) startAdapter(ctx context.Context, a *pgstore.BrokerAccount) error {
	m.setCommission(a.Venue, a.CommissionBps)
	if a.Platform != 1 {
		return fmt.Errorf("MT4 已退役：platform=%d 不再支持（仅 MT5）", a.Platform) // D-470
	}
	return m.startMT5(ctx, a)
}

// SetMarginReservePct D-415：注入动态 MT5 executor 资金预检安全垫（cfg.MarginReservePct）。
func (m *BrokerManager) SetMarginReservePct(pct float64) { m.marginReservePct = pct }

// startMT5 启动 MT5 适配器。
func (m *BrokerManager) startMT5(ctx context.Context, a *pgstore.BrokerAccount) error {
	// D-164 R5：attach-pending 置位——Connect 内帧源先达触发的 no-match kick 据此重排；
	// defer 清位覆盖 attach 完成与所有错误返回径（不残留致永久重排）。
	kickKey := keydomain.Join(a.Venue, "mt5")
	m.setAttaching(kickKey)
	defer m.clearAttaching(kickKey)
	ad := adapter.NewMT5Adapter(a.Venue, a.Host, a.Server, int32(a.Port), a.User, a.Password)
	// D-104 P2 U1：SetGateway 须在 Connect 前调（Connect dial 按序试 gateway+fallbacks）。
	ad.SetGateway(m.MT5Gateway, m.MT5GatewayFallbacks)
	// D-099：MarginSink 须在 Connect 前注入（Connect 末尾 go runMarginSource 读此字段）。
	ad.MarginSink = m.MarginSink
	// D-107 P1：OrderEventSink 须在 Connect 前注入（Connect 末尾 go runOrderEventSource 读此字段）。
	ad.OrderEventSink = m.OrderEventSink
	// D-581：三 sink 拷 dispatch* bound-method 免时序——目标晚接自生效（原按值拷贝
	// 晚于 StartAll 的 Set* 永远错过，adapter 拷到 nil 即永久丢帧）。
	ad.AccountSink = m.dispatchAccount
	ad.PositionsSink = m.dispatchPositions
	ad.StreamStateSink = m.dispatchStreamState
	// D-104 P2 U2：AlertSink 须在 Connect 前注入（quoteReconnectBackoff 封顶档读此字段）。
	ad.AlertSink = m.AlertSink
	// D-004 P1#6：dispatch 晚绑定（同 D-581 三 sink）——SetReconKick 晚接自生效。
	ad.OnReconnect = m.dispatchReconKick
	// D-170：zeroframe venue 级闭市闸（session 表判，表缺席→不拦照报）。
	ad.ClosedCheck = func() bool {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		closed, known := marketstate.VenueAllClosed(cctx, m.store, a.Venue, time.Now())
		return known && closed
	}
	if err := ad.Connect(ctx); err != nil {
		return fmt.Errorf("mt5 connect: %w", err)
	}
	fetchSymbols := func(ctx context.Context) ([]string, error) {
		resp, err := ad.MT5Client().SymbolList(tokenCtx(ctx, ad.Token()), &mt5pb.SymbolListRequest{Id: ad.Token()})
		if err != nil {
			return nil, err
		}
		if e := resp.GetError(); e != nil {
			return nil, fmt.Errorf("mt5 symbols: %s", e.GetMessage())
		}
		return resp.GetResult(), nil
	}
	// 最小手数（D-061 量规格预检）：SymbolParams 单符号取 SymGroup.MinLots。
	fetchMinLots := func(ctx context.Context, symbol string) (float64, bool, error) {
		resp, err := ad.MT5Client().SymbolParams(tokenCtx(ctx, ad.Token()), &mt5pb.SymbolParamsRequest{Id: ad.Token(), Symbol: symbol})
		if err != nil {
			return 0, false, err
		}
		if e := resp.GetError(); e != nil {
			return 0, false, fmt.Errorf("mt5 symbolParams %s: %s", symbol, e.GetMessage())
		}
		sp := resp.GetResult()
		if sp == nil || sp.GetSymbolGroup() == nil {
			return 0, false, nil
		}
		min := sp.GetSymbolGroup().GetMinLots()
		return min, min > 0, nil
	}
	// volume step（D-070 qty 精度取整）：SymbolParams SymGroup.LotsStep（double，直接=手数步长）。
	// D-097 R1 取证修复：原用 VolumeStep(int64 f20, 微手单位 1手=1000000) ÷1e6 → 1.0 手，
	// 实际三家 broker（277/529/591 AUDJPY）LotsStep=0.01 / VolumeStep=1000000。
	// cmd/probe 三方对拍证据：LotsStep(double f3)=0.01 = MT5 终端步长真值；
	// VolumeStep(int64 f20)=1000000 是底层微手单位，÷1e6=1.0 手=错。
	fetchVolumeStep := func(ctx context.Context, symbol string) (float64, bool, error) {
		resp, err := ad.MT5Client().SymbolParams(tokenCtx(ctx, ad.Token()), &mt5pb.SymbolParamsRequest{Id: ad.Token(), Symbol: symbol})
		if err != nil {
			return 0, false, err
		}
		if e := resp.GetError(); e != nil {
			return 0, false, fmt.Errorf("mt5 symbolParams %s: %s", symbol, e.GetMessage())
		}
		sp := resp.GetResult()
		if sp == nil || sp.GetSymbolGroup() == nil {
			return 0, false, nil
		}
		step := sp.GetSymbolGroup().GetLotsStep() // D-097: LotsStep(double f3)=手数步长真值
		if step <= 0 {
			return 0, false, nil
		}
		return step, true, nil
	}
	// 符号表 + 分组：SymbolParamsMany 批量取 SymGroup.GroupName，取反斜杠路径顶层段为分组
	// （Forex\Major\EURUSD→Forex）。Limit 不设——服务端默认 cap，设小 limit 反而在符号超限时抛异常
	// （归档 ListingsMany 经验，D-040）。
	fetchSymbolGroups := func(ctx context.Context) ([]SymbolGroup, error) {
		names, err := fetchSymbols(ctx)
		if err != nil {
			return nil, err
		}
		if len(names) == 0 {
			return nil, nil
		}
		resp, err := ad.MT5Client().SymbolParamsMany(tokenCtx(ctx, ad.Token()), &mt5pb.SymbolParamsManyRequest{Id: ad.Token(), Symbol: names})
		if err != nil {
			return nil, err
		}
		if e := resp.GetError(); e != nil {
			return nil, fmt.Errorf("mt5 symbolParamsMany: %s", e.GetMessage())
		}
		out := make([]SymbolGroup, 0, len(resp.GetResult()))
		for _, sp := range resp.GetResult() {
			if sp == nil {
				continue
			}
			// ContractSize 同批返回（D-042 合约规格校验数据源），0 = 未知；
			// TradeMode 同批透传（D-060 F7 seed 判据：禁交易符号不映射）。
			out = append(out, SymbolGroup{
				Symbol:       sp.GetSymbol(),
				Group:        topGroup(sp.GetSymbolGroup().GetGroupName()),
				ContractSize: sp.GetSymbolInfo().GetContractSize(),
				TradeMode:    int32(sp.GetSymbolGroup().GetTradeMode()),
			})
		}
		return out, nil
	}
	// 单符号权威合约规格（SymbolParams，零信任：保存映射时后端自取校验，不信前端传值）。
	// known=false = 合约未知（查询失败/无结果），调用方跳过规格校验。D-042。
	fetchContract := func(ctx context.Context, symbol string) (float64, bool, error) {
		resp, err := ad.MT5Client().SymbolParams(tokenCtx(ctx, ad.Token()), &mt5pb.SymbolParamsRequest{Id: ad.Token(), Symbol: symbol})
		if err != nil {
			return 0, false, err
		}
		if e := resp.GetError(); e != nil {
			return 0, false, fmt.Errorf("mt5 symbolParams %s: %s", symbol, e.GetMessage())
		}
		sp := resp.GetResult()
		if sp == nil || sp.GetSymbolInfo() == nil {
			return 0, false, nil
		}
		cs := sp.GetSymbolInfo().GetContractSize()
		return cs, cs > 0, nil
	}
	symMap, symbols, blocked, scales, sensitive, err := m.symbolPlan(ctx, a.Venue, fetchSymbolGroups)
	if err != nil || len(symMap) == 0 {
		log.Printf("[broker] %s symbol plan: %v", a.Venue, err)
		symMap, symbols = identityMap(defaultSymbols)
		blocked, scales, sensitive = nil, nil, nil
	}
	ad.SetSymbolMap(symMap)             // 全量（含 blocked 行）——native/variant fact 仍需（D-383 A2.3）
	ad.SetCanonicalEmitBlocked(blocked) // D-383 A2.3：canonical 标签发射抑制集
	ad.SetSymbolScales(scales)          // D-594：单位标定随 map 同源注入（scale 供发射口乘价）
	ad.SetUnitSensitive(sensitive)      // D-594：单位敏感 canonical 集（未标定行 canonical 抑制）
	// 历史映射（D-042 前）无合约 → 启动时回填，恢复规格画像（零信任校验基线）。
	if gs, gerr := fetchSymbolGroups(ctx); gerr == nil {
		m.backfillContracts(ctx, a.Venue, gs)
	}

	ch, cancel := ad.Subscribe(symbols)
	safego.GoLoop("broker_manager_adapters:mt5-quote-pump", func() {
		for q := range ch {
			m.qbus.Publish(q)
		}
	})
	mt5Exe := exec.NewMT5Executor(a.Venue, ad.Token(), ad.TradingClient(), ad.MT5Client())
	mt5Exe.SetMarginReservePct(m.marginReservePct)
	m.reg.Register(mt5Exe)
	q := reconcile.NewMT5Querier(a.Venue, ad.Token(), ad.MT5Client())
	// D-162R4：DB 驱动径同款复合报价钟（symMap=broker→canonical 已算出复用）——
	// 帧径（ad.SetPriceAnchor）+REST 径（q.SetPriceAnchor）同注，重启+休市锚持久末帧。
	anchor := adapter.CompositeTickAnchor(ad.LastTickAnchor,
		func(ctx context.Context, sym string) (float64, float64, time.Time, bool) {
			if m.store == nil {
				return 0, 0, time.Time{}, false
			}
			t, ok, err := m.store.LatestTick(ctx, a.Venue, "mt5", sym)
			if err != nil || !ok {
				return 0, 0, time.Time{}, false
			}
			return t.Bid, t.Ask, t.Ts, true
		}, symMap)
	ad.SetPriceAnchor(anchor)
	q.SetPriceAnchor(anchor)

	r := &running{
		stop:           func() { cancel(); ad.Stop() },
		querier:        q,
		histQuerier:    reconcile.NewMT5OrderHistoryQuerier(a.Venue, ad.Token(), ad.MT5Client()), // D-416
		symbols:        fetchSymbols,
		symbolGroups:   fetchSymbolGroups,
		symbolParams:   fetchContract,
		volumeSpec:     fetchMinLots,
		volumeStep:     fetchVolumeStep,
		setSymMap:      ad.SetSymbolMap,
		setEmitBlocked: ad.SetCanonicalEmitBlocked,
		setUnitScales: func(sc map[string]adapter.SymbolScale, sens map[string]bool) {
			ad.SetSymbolScales(sc)
			ad.SetUnitSensitive(sens)
		},
		resubscribe: ad.Resubscribe,
		swap:        func(ctx context.Context) ([]fact.Fact, error) { return ad.SwapFacts(ctx, m.qbus) },
		marginSpec: func(ctx context.Context, symbol string) (float64, float64, error) {
			// D-036 仓位级保证金估算输入：SymGroup.InitialMargin（per-lot 定额，
			// >0 直乘 lots）+ SymbolInfo.ContractSize（率径分母），同次 SymbolParams。
			resp, err := ad.MT5Client().SymbolParams(tokenCtx(ctx, ad.Token()), &mt5pb.SymbolParamsRequest{Id: ad.Token(), Symbol: symbol})
			if err != nil {
				return 0, 0, err
			}
			if e := resp.GetError(); e != nil {
				return 0, 0, fmt.Errorf("mt5 symbolParams %s: %s", symbol, e.GetMessage())
			}
			sp := resp.GetResult()
			if sp == nil {
				return 0, 0, nil
			}
			return sp.GetSymbolGroup().GetInitialMargin(), sp.GetSymbolInfo().GetContractSize(), nil
		},
		requiredMargin: func(ctx context.Context, symbol string, lots float64, side string) (float64, error) {
			// D-068 仓位级保证金原生值：RequiredMargin RPC——D-415 同形态（price 缺省
			// 0=网关侧现价），type=仓向。G4 供应商 2026-10-05 实装（0.01 lot XAU=$2.0811
			// 实拍线性）；结果≤0 视为失败（fail-closed，调用方降级估算）。
			dealType := mt5pb.DealType_DealType_DealBuy
			if side == "SELL" {
				dealType = mt5pb.DealType_DealType_DealSell
			}
			resp, err := ad.MT5Client().RequiredMargin(tokenCtx(ctx, ad.Token()), &mt5pb.RequiredMarginRequest{Id: ad.Token(), Symbol: symbol, Lots: lots, Type: &dealType})
			if err != nil {
				return 0, err
			}
			if e := resp.GetError(); e != nil {
				return 0, fmt.Errorf("mt5 requiredMargin %s: %s", symbol, e.GetMessage())
			}
			if v := resp.GetResult(); v > 0 {
				return v, nil
			}
			return 0, fmt.Errorf("mt5 requiredMargin %s: result %v", symbol, resp.GetResult())
		},
		sessions:         ad.SessionsFacts, // D-029 R3：周表采集源（running 面——SessionsFacts 聚合含 running+static）
		meta:             func(ctx context.Context) ([]fact.Fact, error) { return ad.MetaFacts(ctx) },
		orderHistoryScan: mt5OrderHistoryScan(ad.MT5Client(), ad.Token()), // D-181 S1
		// D-563 S4b：判定时刻实时资金（marginFloorEval 用，禁快照消费）。
		acctSummary: func(ctx context.Context) (float64, float64, string, error) {
			resp, err := ad.MT5Client().AccountSummary(tokenCtx(ctx, ad.Token()), &mt5pb.AccountSummaryRequest{Id: ad.Token()})
			if err != nil {
				return 0, 0, "", err
			}
			if e2 := resp.GetError(); e2 != nil {
				return 0, 0, "", fmt.Errorf("mt5 accountSummary: %s", e2.GetMessage())
			}
			res := resp.GetResult()
			if res == nil {
				return 0, 0, "", fmt.Errorf("mt5 accountSummary: result nil")
			}
			return res.GetEquity(), res.GetFreeMargin(), res.GetCurrency(), nil
		},
	}
	m.attach(a.Venue, r)
	log.Printf("[broker] %s (MT5) ready", a.Venue)
	m.kickAccountReady(kickKey) // D-071 R1：margin 首采 kick 可早于 ready（见 sinks.go）
	return nil
}

// tokenCtx 给 mtapi 调用附加会话 id header（网关按 id 路由）。
func tokenCtx(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "id", token)
}
