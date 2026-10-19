package main

import (
	"context"
	"log"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/collect"
	"nox/internal/data/collect/cmc"
	"nox/internal/platform/config"
	"nox/internal/platform/safego"
	"nox/internal/store/pgstore"
)

// buildCmcCollectors 组装 CMC Markets 采集器（arb-archive wire_cmc.go 借形移植；
// STATE「CMC 采集器移植」票）。凭据走 secRes（CMC_EMAIL/CMC_PASSWORD/NOX_CMC_CREATE_TPL，
// app_secrets 三键已注册且 SG 在值）；缺值=优雅降级跳过+请求箱落凭据缺口（禁凭据件误立）。
// accountID/WS 开关走 app_config（D-162：装配点 DB 驱动禁 env）——迁移 1047 种子键。
// 返回 WsClient 供 main 装配（nil=REST 回退态或未装配）。
func buildCmcCollectors(ctx context.Context, st *pgstore.Store, cfg *config.Config, secRes *config.SecretResolver, qbus *bus.QuoteBus) ([]collect.Named, *cmc.WsClient) {
	cmc.SetCreateTplHex(secRes.Get("NOX_CMC_CREATE_TPL")) // 重登模板 secrets 注入（arb D-145 同源键 NOX 化）
	email := secRes.Get("CMC_EMAIL")
	pass := secRes.Get("CMC_PASSWORD")
	if email == "" || pass == "" {
		log.Println("[wiring] CMC_EMAIL/CMC_PASSWORD 未设置，跳过 CMC 采集器")
		openCredGap(ctx, st)
		return nil, nil
	}
	accountID, ok := cmcAccountID(ctx, st)
	if !ok {
		log.Println("[wiring] app_config cmc_account_id 缺/非法，跳过 CMC 采集器")
		return nil, nil
	}
	auth := cmc.NewAuthManager(email, pass, accountID, cmc.DefaultPlatformBase)
	// 初始登录（免首轮无 token 废请求；失败不阻断，Poll 路径重试）
	if err := auth.Login(ctx); err != nil {
		log.Printf("[wiring] CMC 初始登录失败（不阻断，交由 Poll 路径重试）: %v", err)
	}
	safego.GoLoop("wire_cmc:authRefresh", func() { auth.StartRefresh(context.Background()) })
	prices := cmc.NewPricesCollector(auth, "X-AFQUL", st, cmc.DefaultRestBase)
	// 业主令 10-19③：报价条候选接 CMC——REST 价（instrument 价唯一通道，WS 无价频）
	// 写库后同步 publish qbus；venue_type=cfd、VenueSymbol=instrument_id。
	// AllLatest 面增一源：cmc 非执行 venue（broker_symbol_map/combo 无 cmc 行）→eval/下单径零新配对。
	prices.SetQuoteHook(func(bid, ask float64, ts time.Time) {
		qbus.Publish(bus.Quote{
			Symbol: "TRXUSD", Venue: "cmc", VenueType: "cfd",
			Bid: bid, Ask: ask, Time: ts, VenueSymbol: "X-AFQUL",
		})
	})
	safego.GoLoop("wire_cmc:pricesWatchdog", func() { prices.StartWatchdog(context.Background()) })
	account := cmc.NewAccountCollector(auth, accountID, st, cmc.DefaultRestBase)
	// WS 禁用开关（app_config cmc_ws_disabled）→ 回退全 REST（arb D-225 v2.1 同语义）
	if cmcWsDisabled(ctx, st) {
		log.Println("[wiring] CMC WS 禁用(app_config), WS 事件驱动关闭，REST 全开")
		return []collect.Named{
			{Name: "cmc_prices", Interval: 60 * time.Second, Collector: prices},
			{Name: "cmc_account", Interval: 5 * time.Minute, Collector: account},
			{Name: "cmc_history", Interval: 24 * time.Hour, Collector: cmc.NewHistoryCollector(auth, accountID, st, cmc.DefaultRestBase)},
		}, nil
	}
	// WS 事件驱动：健康短路接线（健康期 REST 降频/短路）
	ws := cmc.NewWsClient(auth, accountID, st, cmc.DefaultWsBase, cmc.DefaultRestBase)
	account.SetWSHealthy(ws.IsHealthy) // 账户面 WS type5/6/7 真覆盖=降频闸保留；prices 摘闸——D-212 裁 REST=instrument 价主源按 Interval=60s 全速（休市 closed 门控保留）
	// marketStatus 注入（WS 价格行 market_status 列）
	ws.SetMarketStatus(func() string {
		status, _, err := st.LastCMCMarketStatus(context.Background())
		if err != nil {
			return ""
		}
		return status
	})
	safego.Go("wire_cmc:ws", func() { ws.Run(context.Background()) })
	// 禁线「WS 断=看门狗告警非轮询兜底」：WS 模式下 REST prices/account 不进调度
	// （WS 覆盖报价+账户+持仓三面，ws_persist 全落；断流=ws_guard 传输告警+prices
	// watchdog 陈旧告警+wire_health 表暗死三重，不静默轮询遮断）。
	// history 无 WS 通道（归档 API 端点清单：WS 只 prices/heartbeat/account type6）——
	// D-004 例外低频 reconcile（24h 增量流水，from=游标+1s）。
	// D-212 修正：instrument bid/mid 价面 WS 无通道（订阅仅 type5/6/7 账户域——
	// 协议面取证实证：无 instrument 价频订阅能力，禁凭猜写帧）——cmc_prices REST
	// 是该字段主源非断流兜底（禁线语义不误伤：兜底禁的是遮断 WS 已有面）。
	// collector 自带 wsHealthy+15min 降频闸限频。
	return []collect.Named{
		{Name: "cmc_prices", Interval: 60 * time.Second, Collector: prices},
		{Name: "cmc_history", Interval: 24 * time.Hour, Collector: cmc.NewHistoryCollector(auth, accountID, st, cmc.DefaultRestBase)},
	}, ws
}

// cmcAccountID app_config cmc_account_id 读（迁移 1047 种子；未配置=ok=false 跳过）。
func cmcAccountID(ctx context.Context, st *pgstore.Store) (int64, bool) {
	v, ok, err := st.AppConfigInt64(ctx, "cmc_account_id")
	if err != nil || !ok || v <= 0 {
		return 0, false
	}
	return v, true
}

// cmcWsDisabled app_config cmc_ws_disabled 读（"true"=禁）。
func cmcWsDisabled(ctx context.Context, st *pgstore.Store) bool {
	v, ok, err := st.AppConfigInt64(ctx, "cmc_ws_disabled")
	if err != nil || !ok {
		return false
	}
	return v != 0
}

// openCredGap 凭据缺口落请求箱（kind=credential；幂等键 template_id|kind|subject）。
// 禁凭据件误立——缺值不编不猜，业主喂键后 OpenConditionGap 行自然消（resolve 径）。
func openCredGap(ctx context.Context, st *pgstore.Store) {
	if st == nil {
		return
	}
	id, err := st.OpenConditionGap(ctx, &pgstore.ConditionGap{
		TemplateID:  "cmc_collector",
		Kind:        "credential",
		Subject:     "CMC_EMAIL/CMC_PASSWORD",
		Qualifier:   "CMC Markets 采集凭据（app_secrets 三键：CMC_EMAIL/CMC_PASSWORD/NOX_CMC_CREATE_TPL）",
		RequestText: "CMC 采集器凭据缺口：请在 app_secrets 配置 CMC_EMAIL 与 CMC_PASSWORD（及重登模板 NOX_CMC_CREATE_TPL）；配齐后本缺口可核销，采集器下轮 boot 自挂。",
	})
	if err != nil {
		log.Printf("[wiring] cmc cred gap open: %v", err)
		return
	}
	if err := st.RequestConditionGap(ctx, id); err != nil {
		log.Printf("[wiring] cmc cred gap request: %v", err)
	}
}
