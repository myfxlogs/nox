// okx_settle.go —— okx DCI 行权/到期对账：filled 单 per-order 轮
// `GET /sfp/dcd/order-status?ordId=` 状态迁移（LIVE→PENDING_SETTLE→SETTLED），
// settled 回执落账（settleSz/settleCcy=交割总额+币、yieldSz/yieldCcy=利息、
// settlePx=交割价——venue 实拍键名，行权结果只认回执不推算）。
// 接货处置复用 D-517 预案：跨币到账→funding 6→18 回划→现货 market 卖回→
// 净损益 income_events；卖败留仓标 pending_disposal 不臆造。
package dciexec

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"nox/internal/store/pgstore"
)

var okxOrdIDRe = regexp.MustCompile(`"ordId"\s*:\s*"([^"]+)"`)

// okxDeliveryReached product_ref 内嵌交割日（ETH-USDT-261021-2280-P 第三段
// YYMMDD）→ 交割日 UTC 零点起算到期可轮。形外 ref 如实轮（venue order-status
// 才是终态真相，ref 畸形不该饿死真单）。
func okxDeliveryReached(productRef string, now time.Time) bool {
	parts := strings.Split(productRef, "-")
	if len(parts) < 3 || len(parts[2]) != 6 {
		return true
	}
	d, err := time.Parse("060102", parts[2])
	if err != nil {
		return true
	}
	return !now.Before(d)
}

// settleOKXOrder okx 单到期对账：order-status 轮询 state——非 settled 如实
// 跳过（未到期零误翻）；settled→落账+入账+处置。返 true=本拍翻态。
func (e *Executor) settleOKXOrder(ctx context.Context, o pgstore.FilledDCIOrder) (bool, error) {
	cred, ok := e.EarnCreds["okx"]
	if !ok || cred.Key == "" {
		return false, fmt.Errorf("okx 凭据缺——对账读径无凭")
	}
	m := okxOrdIDRe.FindStringSubmatch(o.Response)
	if m == nil {
		return false, fmt.Errorf("response 缺 ordId（订单#%d 回执形外）——不臆对", o.ID)
	}
	rail := &okxEarnRail{e: e, c: cred}
	data, err := rail.req(ctx, http.MethodGet,
		"/api/v5/finance/sfp/dcd/order-status?ordId="+m[1], "")
	if err != nil {
		return false, err // 读败下拍重试——单错不翻态
	}
	var rows []okxSettleRow
	if err := json.Unmarshal(data, &rows); err != nil {
		return false, fmt.Errorf("order-status 解析: %w", err)
	}
	if len(rows) == 0 {
		return false, fmt.Errorf("order-status 零行（ordId=%s）", m[1])
	}
	st := &rows[0]
	if !strings.EqualFold(st.State, "settled") {
		return false, nil // LIVE/PENDING_SETTLE 未终态——下拍再轮
	}
	return true, e.bookSettlementOKX(ctx, o, st)
}

type okxSettleRow struct {
	State     string `json:"state"`
	OrdID     string `json:"ordId"`
	ProductID string `json:"productId"`
	SettleCcy string `json:"settleCcy"`
	SettleSz  string `json:"settleSz"`
	SettlePx  string `json:"settlePx"`
	YieldCcy  string `json:"yieldCcy"`
	YieldSz   string `json:"yieldSz"`
}

// bookSettlementOKX 同 bookSettlement 形（CAS→income→表象收口→warn→处置钩），
// venue='okx'：settleCcy/settleSz 到账面、yieldSz 利息、exercised=settleCcy≠
// investCcy。configID 按 EarnCreds 优先序兜取（unified>perp>spot）。
func (e *Executor) bookSettlementOKX(ctx context.Context, o pgstore.FilledDCIOrder, st *okxSettleRow) error {
	ccy := st.SettleCcy
	qty, _ := strconv.ParseFloat(st.SettleSz, 64)
	interest, _ := strconv.ParseFloat(st.YieldSz, 64)
	ok, err := e.Store.SettleDCIOrder(ctx, o.ID, ccy, qty, interest)
	if err != nil {
		return err
	}
	if !ok {
		return nil // 并发已收口——幂等跳
	}
	if o.ProposalID != nil && o.TenorDays != nil && *o.TenorDays > 0 &&
		o.Qty > 0 && strings.EqualFold(ccy, o.InvestCcy) && interest > 0 {
		apr := interest / o.Qty * 365 / float64(*o.TenorDays) * 100
		if uerr := e.Store.UpdateOppEvalActual(ctx, *o.ProposalID, apr); uerr != nil {
			log.Printf("[dci-settle] okx order %d eval actual apr: %v", o.ID, uerr)
		}
	}
	if cfgID, cerr := e.okxConfigID(ctx); cerr == nil && cfgID > 0 {
		var evs []pgstore.IncomeEvent
		if interest > 0 {
			evs = append(evs, pgstore.IncomeEvent{
				Venue: "okx", VenueType: "option", ConfigID: cfgID,
				Symbol: o.Underlying, IncomeType: "dci_interest",
				AmountUSD: e.usdOf(ctx, st.YieldCcy, interest),
				TranID:    o.ID*10 + 1, Ts: e.Now()})
		}
		if qty > 0 && ccy != "" {
			evs = append(evs, pgstore.IncomeEvent{
				Venue: "okx", VenueType: "option", ConfigID: cfgID,
				Symbol: o.Underlying, IncomeType: "dci_exercise",
				AmountUSD: e.usdOf(ctx, ccy, qty),
				TranID:    o.ID*10 + 2, Ts: e.Now()})
		}
		if n, err := e.Store.InsertIncomeEvents(ctx, evs); err != nil {
			log.Printf("[dci-settle] okx order %d income events: %v", o.ID, err)
		} else if n > 0 {
			log.Printf("[dci-settle] okx order %d income events %d 行入账", o.ID, n)
		}
	}
	marker := "单号 " + o.RequestID
	suffix := fmt.Sprintf("已交割（%s → %s）", o.InvestCcy, ccy)
	if ccy == "" {
		suffix = "已交割（方向未给——venue 回执缺 settleCcy）"
	}
	if n, err := e.Store.ClosePersonalPositionsByMarker(ctx, marker, suffix); err != nil {
		log.Printf("[dci-settle] okx order %d position close: %v", o.ID, err)
	} else if n == 0 {
		log.Printf("[dci-settle] okx order %d 表象行未寻获（marker=%q）", o.ID, marker)
	}
	dir := ccy
	if dir == "" {
		dir = "方向未给"
	}
	if err := e.Store.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
		"DCI 订单#%d 已交割：okx %s %.6f %s（本息 %.8f %s，单号 %s）",
		o.ID, o.ProductRef, qty, dir, interest, o.InvestCcy, o.RequestID)); err != nil {
		return err
	}
	e.disposeExerciseOKX(ctx, o, ccy, qty)
	return nil
}

// okxConfigID exchange_configs okx 行 id（凭据优先序 unified>perp>spot>”）。
func (e *Executor) okxConfigID(ctx context.Context) (int64, error) {
	for _, vt := range []string{"unified", "perp", "spot", ""} {
		if id, err := e.Store.ExchangeConfigID(ctx, "okx", vt); err == nil && id > 0 {
			return id, nil
		}
	}
	return 0, nil
}

// disposeExerciseOKX D-517 预案 okx 形：跨币接货（settleCcy≠investCcy 且非
// USD 族）→6→18 回划→market sell 得 USDT→净损益入账；败=留仓标。
func (e *Executor) disposeExerciseOKX(ctx context.Context, o pgstore.FilledDCIOrder, ccy string, qty float64) {
	if qty <= 0 || ccy == "" || strings.EqualFold(ccy, o.InvestCcy) {
		return
	}
	switch strings.ToUpper(ccy) {
	case "USD", "USDT", "USDC", "BUSD", "FDUSD":
		return
	}
	cred, ok := e.EarnCreds["okx"]
	if !ok || cred.Key == "" {
		e.markPendingDisposal(ctx, o, ccy, qty, "okx 凭据缺——现货卖回无径")
		return
	}
	rail := &okxEarnRail{e: e, c: cred}
	var lastErr error
	for i := 0; i < disposalMaxRetry; i++ {
		proceeds, err := okxMarketSell(ctx, rail, ccy, qty)
		if err == nil {
			pnl := proceeds - o.Qty
			if cfgID, cerr := e.okxConfigID(ctx); cerr == nil && cfgID > 0 {
				if n, ierr := e.Store.InsertIncomeEvents(ctx, []pgstore.IncomeEvent{{
					Venue: "okx", VenueType: "spot", ConfigID: cfgID,
					Symbol: o.Underlying, IncomeType: "dci_exercise_disposal",
					AmountUSD: pnl, TranID: o.ID*10 + 3, Ts: e.Now()}}); ierr != nil {
					log.Printf("[dci-dispose] okx order %d disposal income: %v", o.ID, ierr)
				} else if n > 0 {
					log.Printf("[dci-dispose] okx order %d disposal pnl %.4f 入账", o.ID, pnl)
				}
			}
			if aerr := e.Store.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
				"DCI 订单#%d 接货 %.8f %s 已市价卖回：okx 得 %.2f USDT，净损益 %+.2f（D-517）",
				o.ID, qty, ccy, proceeds, pnl)); aerr != nil {
				log.Printf("[dci-dispose] okx order %d alert: %v", o.ID, aerr)
			}
			return
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	e.markPendingDisposal(ctx, o, ccy, qty,
		fmt.Sprintf("okx 市价卖回 %d 次尽败：%v", disposalMaxRetry, lastErr))
}

// okxMarketSell 接货市价卖回：6→18 回划（sfp 到账在 funding 户）→
// `POST /trade/order {instId=ccy-USDT,tdMode:cash,side:sell,ordType:market,sz}`
// →`GET /trade/order` 回执 accFillSz×avgPx=得额（venue 实拍键）。
func okxMarketSell(ctx context.Context, rail *okxEarnRail, ccy string, qty float64) (float64, error) {
	// 10-15 同型核查（binance -1013 LOT_SIZE 实证）：okx sz 亦受 lotSz/minSz 约束，
	// 先 floor 再回划（划转量与报单量一致，尾零损耗留 funding 户）。
	if fq, ferr := okxLotFloor(ctx, rail, strings.ToUpper(ccy)+"-USDT", qty); ferr != nil {
		return 0, ferr
	} else {
		qty = fq
	}
	tb := fmt.Sprintf(`{"ccy":"%s","amt":"%s","from":"6","to":"18","type":"0"}`,
		ccy, strconv.FormatFloat(qty, 'f', 8, 64))
	if _, err := rail.req(ctx, http.MethodPost, "/api/v5/asset/transfer", tb); err != nil {
		return 0, fmt.Errorf("okx 6→18 回划: %w", err)
	}
	inst := strings.ToUpper(ccy) + "-USDT"
	body := fmt.Sprintf(`{"instId":"%s","tdMode":"cash","side":"sell","ordType":"market","sz":"%s"}`,
		inst, strconv.FormatFloat(qty, 'f', 8, 64))
	sraw, err := rail.req(ctx, http.MethodPost, "/api/v5/trade/order", body)
	if err != nil {
		return 0, fmt.Errorf("okx market sell: %w", err)
	}
	var srows []struct {
		OrdID string `json:"ordId"`
	}
	if json.Unmarshal(sraw, &srows) != nil || len(srows) == 0 || srows[0].OrdID == "" {
		return 0, fmt.Errorf("okx sell 回执 ordId 缺: %s", string(sraw))
	}
	oraw, err := rail.req(ctx, http.MethodGet,
		"/api/v5/trade/order?instId="+inst+"&ordId="+srows[0].OrdID, "")
	if err != nil {
		return 0, fmt.Errorf("okx order detail: %w", err)
	}
	var orows []struct {
		AccFillSz string `json:"accFillSz"`
		AvgPx     string `json:"avgPx"`
	}
	if json.Unmarshal(oraw, &orows) != nil || len(orows) == 0 {
		return 0, fmt.Errorf("okx order detail 解析: %s", string(oraw))
	}
	sz, _ := strconv.ParseFloat(orows[0].AccFillSz, 64)
	px, _ := strconv.ParseFloat(orows[0].AvgPx, 64)
	if sz <= 0 || px <= 0 {
		return 0, fmt.Errorf("okx sell 成交额回执缺（accFillSz=%s avgPx=%s）",
			orows[0].AccFillSz, orows[0].AvgPx)
	}
	return sz * px, nil
}

// okxLotCache instruments lotSz/minSz 进程内缓存（公共参考数据——D-004 举证：
// OKX WS 无 instruments 频道，public/instruments REST 低频拉取合规）。
var okxLotCache sync.Map // instId → [2]float64{lotSz, minSz}

// okxLotFloor sz 对齐 lotSz（floor；<minSz 或读败→fail-closed 留仓径）。
func okxLotFloor(ctx context.Context, rail *okxEarnRail, inst string, qty float64) (float64, error) {
	if v, ok := okxLotCache.Load(inst); ok {
		f := v.([2]float64)
		return okxFloorApply(inst, f[0], f[1], qty)
	}
	data, err := rail.req(ctx, http.MethodGet,
		"/api/v5/public/instruments?instType=SPOT&instId="+inst, "")
	if err != nil {
		return 0, fmt.Errorf("okx instruments %s: %w", inst, err)
	}
	var rows []struct {
		LotSz string `json:"lotSz"`
		MinSz string `json:"minSz"`
	}
	if json.Unmarshal(data, &rows) != nil || len(rows) == 0 {
		return 0, fmt.Errorf("okx instruments %s 解析/零行: %s", inst, string(data))
	}
	lot, _ := strconv.ParseFloat(rows[0].LotSz, 64)
	mn, _ := strconv.ParseFloat(rows[0].MinSz, 64)
	if lot <= 0 {
		return 0, fmt.Errorf("okx instruments %s lotSz 缺（%q）", inst, rows[0].LotSz)
	}
	okxLotCache.Store(inst, [2]float64{lot, mn})
	return okxFloorApply(inst, lot, mn, qty)
}

func okxFloorApply(inst string, lot, mn, qty float64) (float64, error) {
	f := math.Floor(qty/lot+1e-12) * lot
	if f < mn || f <= 0 {
		return 0, fmt.Errorf("okx sell %s 步进后 %.8f 越 minSz %.8f（留仓径）", inst, f, mn)
	}
	return f, nil
}
