// settle.go —— D-207①②④：DCI 交割观察径。
// 由既有 cexscan 扫描拍驱动（refreshTopic "cex_scan" 事件，零新定时器）：
// filled 订单 response.settleDate 过期→签名 GET /v1/dci/product/positions
// 查持仓终态——行仍 HOLDING=未到期跳过；行非 HOLDING=venue 确认结算→
// CAS 翻 settled 记 exercisedCoin 行权方向+本息额、income_events 分 kind
// 入账（dci_interest 利息/dci_exercise 行权接货）、DCI 表象行同帧收口、
// warn 推送（D-199 例）。venue 读败/行未查得=fail-closed 跳过下拍重试。
package dciexec

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"nox/internal/store/pgstore"
)

// DCIPosition venue 持仓行（settle 观察消费集——键名多形以 venue 实拍为准）。
type DCIPosition struct {
	ID                 string `json:"id"`
	InstrumentName     string `json:"instrumentName"`
	ProductRef         string `json:"productRef"`
	OptionType         string `json:"optionType"`
	Status             string `json:"status"`
	ExercisedCoin      string `json:"exercisedCoin"`
	ExerciseCoin       string `json:"exerciseCoin"`
	ExercisedAmt       string `json:"exercisedAmount"`
	ExerciseAmt        string `json:"exerciseAmount"`
	InterestAmt        string `json:"interestAmount"`
	EarnAmt            string `json:"earnAmount"`
	IsExercised        *bool  `json:"isExercised"`        // Binance 实拍键：行权旗（nil=venue 未给）
	SettleAsset        string `json:"settleAsset"`        // Binance 实拍键：交割资产（未行权=投币）
	SettleAmount       string `json:"settleAmount"`       // Binance 实拍键：交割到账总额（本息和）
	SubscriptionAmount string `json:"subscriptionAmount"` // Binance 实拍键：申购本金
}

// settleCcy 交割资产=settleAsset（Binance 实拍键）→exercisedCoin 兜底
// （多键名容错；未给=空如实）。#7 实证 exercisedCoin 是标的币非到账币，
// 未行权 PUT 直接先取会错记 BTC——settleAsset 优先是必修正。
func (p *DCIPosition) settleCcy() string {
	if p.SettleAsset != "" {
		return p.SettleAsset
	}
	if p.ExercisedCoin != "" {
		return p.ExercisedCoin
	}
	return p.ExerciseCoin
}

// settleQty 行权接货量：Binance isExercised=true→settleAmount（接货额即
// 本息折标的到账量）；未行权/venue 缺旗→exercisedAmount/exerciseAmount
// 多键名容错；未给/解析败=0 如实不编。
func (p *DCIPosition) settleQty() float64 {
	if p.IsExercised != nil && *p.IsExercised {
		if f, err := parseNum(p.SettleAmount); err == nil && f > 0 {
			return f
		}
	}
	for _, v := range []string{p.ExercisedAmt, p.ExerciseAmt} {
		if f, err := parseNum(v); err == nil && f > 0 {
			return f
		}
	}
	return 0
}

// interestQty 利息额：interestAmount/earnAmount 多键名容错；Binance 未行权
// 实证=settleAmount−subscriptionAmount 同币差（#7：542.4066−541=1.4066 USDT）；
// 行权面跨币差不可算→0 如实（接货量已含本息折标）。
func (p *DCIPosition) interestQty() float64 {
	for _, v := range []string{p.InterestAmt, p.EarnAmt} {
		if f, err := parseNum(v); err == nil && f > 0 {
			return f
		}
	}
	if p.IsExercised != nil && !*p.IsExercised {
		settle, err1 := parseNum(p.SettleAmount)
		sub, err2 := parseNum(p.SubscriptionAmount)
		if err1 == nil && err2 == nil && settle > sub {
			return settle - sub
		}
	}
	return 0
}

func parseNum(v string) (float64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, fmt.Errorf("empty")
	}
	var f float64
	if err := json.Unmarshal([]byte(v), &f); err == nil {
		return f, nil
	}
	var s string
	if err := json.Unmarshal([]byte(v), &s); err == nil {
		return parseNum(s)
	}
	return 0, fmt.Errorf("bad num %q", v)
}

// match 同 venueDCIHolding 判据（三键匹配+opt_type 复验）。
func (p *DCIPosition) match(productRef, optType string) bool {
	if p.OptionType != "" && p.OptionType != optType {
		return false
	}
	return p.ID == productRef || p.InstrumentName == productRef || p.ProductRef == productRef
}

// SettleExpiredOrders 交割观察单拍——过期 filled 订单逐单 venue 终态确认翻
// settled+入账+推送。返回本轮翻态数；venue 读败整体跳（下拍重试零风暴）。
// 持仓行缺（positions 随交割移出行）→申购史端点确认终态（死端切源 M3-④）。
func (e *Executor) SettleExpiredOrders(ctx context.Context) (int, error) {
	orders, err := e.Store.ListFilledDCIOrders(ctx)
	if err != nil {
		return 0, err
	}
	now := e.Now()
	var due []pgstore.FilledDCIOrder
	var okxOrders []pgstore.FilledDCIOrder
	for _, o := range orders {
		if o.Venue == "okx" {
			// earn 轨申购行也落本表 venue=okx（request_id=earn|…、回执无 ordId）
			// ——非 DCI 产品单，不喂 order-status 轮询否则每拍报错。
			// 未到期单同样不轮（product_ref 内嵌交割日 YYMMDD——scan 事件密集
			// 时轮远月单会顶 venue 限流 50011，SG 实证）。
			if strings.HasPrefix(o.RequestID, "dci|okx|") && okxDeliveryReached(o.ProductRef, now) {
				okxOrders = append(okxOrders, o)
			}
			continue
		}
		if o.Venue != "binance" {
			continue // 未知 venue 如实跳（不混径）
		}
		if st, ok := settleDateOf(o.Response); ok && !st.After(now) {
			due = append(due, o)
		}
	}
	okxSettled := 0
	for i := range okxOrders {
		done, err := e.settleOKXOrder(ctx, okxOrders[i])
		if err != nil {
			log.Printf("[dci-settle] okx order %d: %v——单错跳单不影响余行", okxOrders[i].ID, err)
			continue
		}
		if done {
			okxSettled++
		}
	}
	if len(due) == 0 {
		return okxSettled, nil
	}
	pos, err := e.dciPositions(ctx)
	if err != nil {
		return 0, fmt.Errorf("venue dci positions: %w", err) // 读败全跳——单错不翻态
	}
	settled := 0
	var done []DCIPosition
	doneFetched := false // 终态行备份源——仅首个持仓行缺时惰性取（常态零加读）。
	for _, o := range due {
		var row *DCIPosition
		for i := range pos {
			if pos[i].match(o.ProductRef, o.OptType) {
				row = &pos[i]
				break
			}
		}
		if row == nil {
			if !doneFetched {
				s, serr := e.dciSettledPositions(ctx)
				if serr != nil {
					log.Printf("[dci-settle] 终态持仓读败（持仓行缺 %s）: %v——下拍重试", o.ProductRef, serr)
					continue
				}
				done, doneFetched = s, true
			}
			if pid := positionIDOf(o.Response); pid != "" {
				for i := range done {
					if done[i].ID == pid {
						row = &done[i]
						break
					}
				}
			}
			if row == nil {
				for i := range done {
					if done[i].match(o.ProductRef, o.OptType) {
						row = &done[i]
						break
					}
				}
			}
		}
		if row == nil {
			log.Printf("[dci-settle] order %d %s 持仓/终态行均未查得——跳过下拍重试", o.ID, o.ProductRef)
			continue
		}
		if row.Status == "HOLDING" {
			continue // 未到期（settleDate 估算偏差/展期实证）——不翻
		}
		if err := e.bookSettlement(ctx, o, row); err != nil {
			log.Printf("[dci-settle] order %d book: %v", o.ID, err)
			continue
		}
		settled++
	}
	return settled + okxSettled, nil
}

// bookSettlement 确认行入账单步：CAS 翻态→income_events 分 kind→表象行收口→warn。
func (e *Executor) bookSettlement(ctx context.Context, o pgstore.FilledDCIOrder, row *DCIPosition) error {
	ccy, qty, interest := row.settleCcy(), row.settleQty(), row.interestQty()
	ok, err := e.Store.SettleDCIOrder(ctx, o.ID, ccy, qty, interest)
	if err != nil {
		return err
	}
	if !ok {
		return nil // 并发已收口——幂等跳
	}
	// D-258 M4.1④：评估卡实际 APR 回写——未行权本息同币才换算
	// （interest/invest×365/tenor）；行权接货 qty 异币不换算跳写。
	if o.ProposalID != nil && o.TenorDays != nil && *o.TenorDays > 0 &&
		o.Qty > 0 && strings.EqualFold(ccy, o.InvestCcy) && interest > 0 {
		apr := interest / o.Qty * 365 / float64(*o.TenorDays) * 100
		if uerr := e.Store.UpdateOppEvalActual(ctx, *o.ProposalID, apr); uerr != nil {
			log.Printf("[dci-settle] order %d eval actual apr: %v", o.ID, uerr)
		}
	}
	// income_events：利息与行权接货分 kind 如实记（exchange_configs 缺=id 空，
	// NULL 下唯一键不生效会重灌——缺配置则跳账并在推送中文备注）。
	cfgID, cerr := e.Store.ExchangeConfigID(ctx, "binance", "spot")
	if cerr != nil || cfgID == 0 { // 读败/行缺静默跳账面补钉（#7/#8 收益欠账实证）
		log.Printf("[dci-settle] order %d exchange_config 跳收益账（fail-open）cerr=%v cfgID=%d", o.ID, cerr, cfgID)
	}
	if cerr == nil && cfgID > 0 {
		var evs []pgstore.IncomeEvent
		if interest > 0 {
			evs = append(evs, pgstore.IncomeEvent{
				Venue: "binance", VenueType: "option", ConfigID: cfgID,
				Symbol:     o.Underlying,
				IncomeType: "dci_interest",
				AmountUSD:  e.usdOf(ctx, "binance", o.InvestCcy, interest),
				TranID:     o.ID*10 + 1, Ts: e.Now()})
		}
		if qty > 0 && ccy != "" {
			evs = append(evs, pgstore.IncomeEvent{
				Venue: "binance", VenueType: "option", ConfigID: cfgID,
				Symbol:     o.Underlying,
				IncomeType: "dci_exercise",
				AmountUSD:  e.usdOf(ctx, "binance", ccy, qty),
				TranID:     o.ID*10 + 2, Ts: e.Now()})
		}
		if n, err := e.Store.InsertIncomeEvents(ctx, evs); err != nil {
			log.Printf("[dci-settle] order %d income events: %v", o.ID, err)
		} else if n > 0 {
			log.Printf("[dci-settle] order %d income events %d 行入账", o.ID, n)
		}
	}
	// 表象行同帧收口（note 含 reqID 标记的 OPEN 行→CLOSED+尾注）。
	marker := "单号 " + o.RequestID
	suffix := fmt.Sprintf("已交割（%s → %s）", o.InvestCcy, ccy)
	if ccy == "" {
		suffix = "已交割（方向未给——venue 回执缺 exercisedCoin）"
	}
	if n, err := e.Store.ClosePersonalPositionsByMarker(ctx, marker, suffix); err != nil {
		log.Printf("[dci-settle] order %d position close: %v", o.ID, err)
	} else if n == 0 {
		log.Printf("[dci-settle] order %d 表象行未寻获（marker=%q）", o.ID, marker)
	}
	dir := ccy
	if dir == "" {
		dir = "方向未给"
	}
	// 例：终局回执 warn 级（info 被滤噪闸拦截，结算必达）。
	if err := e.Store.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
		"DCI 订单#%d 已交割：%s %.6f %s（本息 %.8f %s，单号 %s）",
		o.ID, o.ProductRef, qty, dir, interest, o.InvestCcy, o.RequestID)); err != nil {
		return err
	}
	// D-517 接货处置钩子：行权落标的币→市价卖回（CAS 幂等闸在 bookSettlement
	// 头——dispose 只跑一次；USD 族/同币到账内部直返零副作用）。
	e.disposeExercise(ctx, o, ccy, qty)
	return nil
}

// usdOf 原币→USD 换算（USD 族 1:1；其余走 venue 现货 ticker，读败=0 如实不入账）。
func (e *Executor) usdOf(ctx context.Context, venue, ccy string, qty float64) float64 {
	switch strings.ToUpper(ccy) {
	case "USD", "USDT", "USDC", "BUSD", "FDUSD":
		return qty
	}
	px, err := e.spotPriceVenue(ctx, venue, strings.ToUpper(ccy)+"USDT")
	if err != nil || px <= 0 {
		return 0
	}
	return qty * px
}
