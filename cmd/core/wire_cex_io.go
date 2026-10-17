// wire_cex_io.go —— D-365 S3：CEX 进出账采集装配（binance+okx real 配置，15min 周期；
// 首拉 90d 回填+水位增量；幂等落库 fin_venue_io）。
package main

import (
	"context"
	"fmt"
	"log"
	"nox/internal/data/adapter"
	"strings"
	"time"

	"nox/internal/api/dashboard"
	"nox/internal/data/collect/exchange"
	"nox/internal/platform/safego"
	"nox/internal/store/pgstore"
)

// MTIODeltaGraceHoursKey 退回㉖伤①：差分成熟度宽限（小时）——app_config 运维键
// （缺省 72h=实测 ~48h venue 记录滞后+裕量；禁裸字面量故落键）。
const MTIODeltaGraceHoursKey = "mt_io_delta_grace_hours"

// startCexIOCollector 装配并启动进出账采集（初始跑一轮+15min ticker——进出账低频，
// 不与高频采集抢）。配置逐轮重读（增删自动跟随，同 income 惯例）。
// MT5 io 同拍挂车（零新定时器）：connected mt5 账号 OrderHistory Balance 行→fin_venue_io。
func startCexIOCollector(ctx context.Context, st *pgstore.Store, kickCh chan struct{}, bm *dashboard.BrokerManager) {
	col := exchange.NewCexIOCollector(st, func() []exchange.CexIOAccount {
		cfgs, err := st.ListExchangeConfigs(ctx)
		if err != nil {
			log.Printf("[wiring] cex-io ListExchangeConfigs: %v (round skipped)", err)
			return nil
		}
		return exchange.FilterIOAccounts(cfgs)
	})
	safego.GoLoop("main:cex-io", func() {
		// D-469a S4/S5：binance DEPOSIT/WITHDRAW kick 主路；ticker 降 30min 断流兜底。
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for {
			if n, err := col.RunOnce(ctx); err != nil {
				log.Printf("[wiring] cex-io RunOnce: %v", err)
			} else if n > 0 {
				log.Printf("[wiring] cex-io round: %d rows inserted", n)
			}
			if bm != nil {
				if n := mtIORound(ctx, st, bm); n > 0 {
					log.Printf("[wiring] mt-io order scan: %d rows", n)
				}
				// D-602：双流皆空（OrderHistory 无 Balance 行+OnOrderUpdate 不推
				// Balance 事件）主径=balance 差分落账（只补账不喂判定）
				if n := mtIODeltaRound(ctx, st, bm); n > 0 {
					log.Printf("[wiring] mt-io delta: %d rows", n)
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-kickCh:
				// 出入账到账延迟小，kick 后 30s 再拉（同 incomeKickSettleDelay 口径）。
				if _, done := waitOrKick(ctx, 30*time.Second, nil); done {
					return
				}
			}
		}
	})
	log.Printf("[wiring] cex-io collector started (kick-driven, 30min watermark fallback; mt-io 同拍)")
}

// mtIORound MT5 充提采集轮（水位增量——每方向独立游标，overlap 1h 防边界漏；
// 首拉 90d 回填）。幂等键=mt5:ticket:{ticket}（(venue,config_id,direction,external_id)
// 唯一冲突 DO NOTHING）。ccy=账户 AccountSummary 原生币种，缺→fail-closed 该账户跳
// （不编 USD）。
func mtIORound(ctx context.Context, st *pgstore.Store, bm *dashboard.BrokerManager) int {
	accs, err := st.MTIOAccountIDs(ctx)
	if err != nil {
		log.Printf("[wiring] mt-io accounts: %v", err)
		return 0
	}
	total := 0
	for _, a := range accs {
		scan := bm.IOScan(a.Venue)
		if scan == nil {
			continue // 未接线（connecting/stopped）——下轮重试如实记
		}
		ccy, ok := bm.MTAccountCurrency(ctx, a.Venue)
		if !ok {
			log.Printf("[wiring] mt-io %s 账户币种不可得（fail-closed 跳）", a.Venue)
			continue
		}
		now := time.Now().UTC()
		for _, dir := range []string{"deposit", "withdraw"} {
			from := now.Add(-90 * 24 * time.Hour)
			if ts, ok, _ := st.LatestVenueIOTs(ctx, a.ID, dir); ok {
				from = ts.Add(-time.Hour) // 水位 overlap
			}
			evs, err := scan(ctx, from, now)
			if err != nil {
				log.Printf("[wiring] mt-io %s %s scan: %v", a.Venue, dir, err)
				continue
			}
			var rows []pgstore.VenueIO
			for _, e := range evs {
				// D-607 双向查重：deal 事件行先落（txid=order:{ticket}）→跳过（异键双记病灶——
				// external_id 幂等键只防同键，防不了 mt5:deal:* vs mt5:ticket:* 异键双写）
				if ok, err := st.VenueIOTxIDExists(ctx, a.ID, fmt.Sprintf("order:%d", e.Ticket)); err == nil && ok {
					continue
				}
				d := "deposit"
				amt := e.Amount
				if amt < 0 {
					d, amt = "withdraw", -amt
				}
				if d != dir {
					continue
				}
				rows = append(rows, pgstore.VenueIO{
					Venue: a.Venue, ConfigID: a.ID, Direction: d, Ccy: ccy,
					Amount: amt, ExternalID: fmt.Sprintf("mt5:ticket:%d", e.Ticket), Ts: e.CloseTime,
				})
			}
			if n, err := st.InsertVenueIO(ctx, rows); err != nil {
				log.Printf("[wiring] mt-io %s insert: %v", a.Venue, err)
			} else {
				total += int(n)
			}
		}
	}
	return total
}

// mtIODeltaRound D-602：MT5 balance 差分落账器——双流皆空（OrderHistory 无
// Balance 行+OnOrderUpdate 不推 Balance 事件+mtapi 网关无 deal 历史 RPC，
// 2026-10-17 取证三通道）时主径：相邻事件帧快照 balance_raw 的未解释残差
// （Δbalance−窗内已关仓 PnL−窗内已落 io 行）=外部资金流（D-539 解释账范式：
// 差分是第二解释源，必须扣全部已落解释项防双计——退回㉓伤①）。
// 行 status='inferred'（推断行与实录行可区分，伤②）+warn 随批落（伤④）；
// 币种=MTAccountCurrency 实拉 fail-closed 跳户（伤⑤，禁硬编域值）；
// PnL 查败/落账败逐窗 fail-closed log（伤③，同文件范式）。
// 输入行限 src IN (mt_frame,mt_event)（D-604：rest 系周期摄取快照禁入判定/
// 落账输入——事件落地行才是流的持久化形态）。小项⑴注记：不引水位状态——
// 幂等键 mt5:delta:{snap_id} 兜底重跑零重复，窗计算成本=帧数量级可忍；
// 小项⑷注记：open-position 逐日 swap 扣 balance 而 settlements 关仓才落
// →armed 出仓后每窗小 inferred 行=D-602 ③杂项径如实落+warn 可容（perf 自消：
// NAV 与 flow 同额对消），口径禁静默在此注记。
func mtIODeltaRound(ctx context.Context, st *pgstore.Store, bm *dashboard.BrokerManager) int {
	accs, err := st.MTIOAccountIDs(ctx)
	if err != nil {
		log.Printf("[wiring] mt-io delta accounts: %v", err)
		return 0
	}
	total := 0
	for _, a := range accs {
		ccy, ok := bm.MTAccountCurrency(ctx, a.Venue)
		if !ok {
			log.Printf("[wiring] mt-io delta %s 账户币种不可得（fail-closed 跳户）", a.Venue)
			continue
		}
		snaps, err := st.BalanceDeltaWindows(ctx, a.Venue, time.Now().UTC().Add(-90*24*time.Hour))
		if err != nil {
			log.Printf("[wiring] mt-io delta %s windows: %v", a.Venue, err)
			continue
		}
		if len(snaps) < 2 {
			continue
		}
		ioSum := func(from, to time.Time) (float64, error) {
			return st.SumVenueIOExternal(ctx, a.Venue, ccy, from, to)
		}
		pnl := func(from, to time.Time) (float64, error) {
			return st.ClosedPositionPnLBetween(ctx, a.Venue, from, to)
		}
		// 退回㉖伤①：grace 落 app_config 键（禁裸字面量——运维可调），缺省 72h
		// 覆盖实测 ~48h venue 记录滞后+裕量。
		grace := 72 * time.Hour
		if gh, ok, gerr := st.AppConfigInt64(ctx, MTIODeltaGraceHoursKey); gerr == nil && ok && gh > 0 {
			grace = time.Duration(gh) * time.Hour
		} else if gerr != nil {
			log.Printf("[wiring] mt-io delta %s grace read: %v（缺省 72h）", a.Venue, gerr)
		}
		rows, alerts, err := deltaBookRows(snaps, time.Now().UTC(), grace, pnl, ioSum, ccy, a.ID, a.Venue)
		if err != nil {
			log.Printf("[wiring] mt-io delta %s: %v（fail-closed 该户跳）", a.Venue, err)
			continue
		}
		if len(rows) == 0 {
			continue
		}
		n, err := st.InsertVenueIO(ctx, rows)
		if err != nil {
			log.Printf("[wiring] mt-io delta %s insert: %v（fail-closed 本批不落）", a.Venue, err)
			continue
		}
		total += int(n)
		if n > 0 {
			// 伤④：D-602② warn 随批落（InsertAlertDedup 24h 防刷屏——聚合窗叙事一条列户）。
			if aerr := st.InsertAlertDedup(ctx, "warn", fmt.Sprintf(
				"MT %s 差分落账 %d 行 status=inferred（balance 残差口径——推断账行已落请对账单复核：%s）",
				a.Venue, n, strings.Join(alerts, "；")), 24*time.Hour); aerr != nil {
				log.Printf("[wiring] mt-io delta %s alert: %v", a.Venue, aerr)
			}
		}
	}
	return total
}

// mtDealIOSink D-607：OnOrderUpdate Deal 事件→fin_venue_io 落账（主径——
// 原 tap 弃件复得；非交易族 2/3/4/5/6/12 才落，Buy/Sell=仓位流归 settlement）。
// 查重联动：先查 mt5:ticket:{OrderTicket} 在库→跳过（扫描先到留）；
// 反之 mt5:deal:{Ticket} 落行后 ticket 扫描扫到同 deal 的 order ticket 不重复
// （external_id 幂等键天然防同 deal 双记）。
var mtDealNonTradeTypes = map[int32]bool{2: true, 3: true, 4: true, 5: true, 6: true, 12: true}

// dealRow D-607：DealEvent→fin_venue_io 行（纯函数——幂等键/方向/查重键
// 组装逻辑钉测试锚；查重键=mt5:ticket:{OrderTicket}）。
func dealRow(venue string, configID int64, ccy string, d adapter.DealEvent) (row pgstore.VenueIO, dedupKey string, ok bool) {
	if !mtDealNonTradeTypes[d.Type] || d.Profit == 0 {
		return pgstore.VenueIO{}, "", false
	}
	dir, amt := "deposit", d.Profit
	if amt < 0 {
		dir, amt = "withdraw", -amt
	}
	return pgstore.VenueIO{
			Venue: venue, ConfigID: configID, Direction: dir, Ccy: ccy,
			Amount: amt, ExternalID: fmt.Sprintf("mt5:deal:%d", d.Ticket),
			TxID: fmt.Sprintf("order:%d", d.OrderTicket), // 双向查重键：扫描面按 txid 认（先到留）
			Ts:   d.Ts,
		},
		fmt.Sprintf("mt5:ticket:%d", d.OrderTicket), true
}

func mtDealIOSink(st *pgstore.Store, bm *dashboard.BrokerManager) adapter.DealSink {
	return func(venueKey string, d adapter.DealEvent) {
		if !mtDealNonTradeTypes[d.Type] {
			return // 交易族 Buy/Sell=仓位流归 settlement 径不落 io
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		accs, err := st.MTIOAccountIDs(ctx)
		if err != nil {
			log.Printf("[mt-io-deal] %s accts: %v（跳过留痕）", venueKey, err)
			return
		}
		var accID int64
		var accVenue string
		for _, a := range accs {
			if a.Venue+":mt5" == venueKey || a.Venue == venueKey {
				accID, accVenue = a.ID, a.Venue
				break
			}
		}
		if accID == 0 {
			return // 未接线户如实跳
		}
		ccy, ok := bm.MTAccountCurrency(ctx, venueKey)
		if !ok {
			log.Printf("[mt-io-deal] %s 币种不可得（fail-closed 跳）", venueKey)
			return
		}
		row, dedupKey, ok := dealRow(accVenue, accID, ccy, d)
		if !ok {
			return
		}
		// 查重联动：mt5:ticket:{OrderTicket} 已在=扫描先到留（双向查重）
		if ok, err := st.VenueIOExternalExists(ctx, accID, dedupKey); err == nil && ok {
			return
		}
		if _, err := st.InsertVenueIO(ctx, []pgstore.VenueIO{row}); err != nil {
			log.Printf("[mt-io-deal] %s insert: %v", venueKey, err)
		}
	}
}
