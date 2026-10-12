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
	ID             string `json:"id"`
	InstrumentName string `json:"instrumentName"`
	ProductRef     string `json:"productRef"`
	OptionType     string `json:"optionType"`
	Status         string `json:"status"`
	ExercisedCoin  string `json:"exercisedCoin"`
	ExerciseCoin   string `json:"exerciseCoin"`
	ExercisedAmt   string `json:"exercisedAmount"`
	ExerciseAmt    string `json:"exerciseAmount"`
	InterestAmt    string `json:"interestAmount"`
	EarnAmt        string `json:"earnAmount"`
}

// settleCcy 行权方向=exercisedCoin（多键名容错；未给=空如实）。
func (p *DCIPosition) settleCcy() string {
	if p.ExercisedCoin != "" {
		return p.ExercisedCoin
	}
	return p.ExerciseCoin
}

// settleQty 行权接货量（多键名容错；未给/解析败=0 如实不编）。
func (p *DCIPosition) settleQty() float64 {
	for _, v := range []string{p.ExercisedAmt, p.ExerciseAmt} {
		if f, err := parseNum(v); err == nil && f > 0 {
			return f
		}
	}
	return 0
}

// interestQty 利息额（多键名容错同上）。
func (p *DCIPosition) interestQty() float64 {
	for _, v := range []string{p.InterestAmt, p.EarnAmt} {
		if f, err := parseNum(v); err == nil && f > 0 {
			return f
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
	for _, o := range orders {
		if st, ok := settleDateOf(o.Response); ok && !st.After(now) {
			due = append(due, o)
		}
	}
	if len(due) == 0 {
		return 0, nil
	}
	pos, err := e.dciPositions(ctx)
	if err != nil {
		return 0, fmt.Errorf("venue dci positions: %w", err) // 读败全跳——单错不翻态
	}
	settled := 0
	var subs []DCIPosition
	subsFetched := false // 申购史备份源——仅首个持仓行缺时惰性取（常态零加读）。
	for _, o := range due {
		var row *DCIPosition
		for i := range pos {
			if pos[i].match(o.ProductRef, o.OptType) {
				row = &pos[i]
				break
			}
		}
		if row == nil {
			if !subsFetched {
				s, serr := e.dciSubscriptions(ctx)
				if serr != nil {
					log.Printf("[dci-settle] subscriptions 读败（持仓行缺 %s）: %v——下拍重试", o.ProductRef, serr)
					continue
				}
				subs, subsFetched = s, true
			}
			for i := range subs {
				if subs[i].match(o.ProductRef, o.OptType) {
					row = &subs[i]
					break
				}
			}
		}
		if row == nil {
			log.Printf("[dci-settle] order %d %s 持仓/申购史行均未查得——跳过下拍重试", o.ID, o.ProductRef)
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
	return settled, nil
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
	// income_events：利息与行权接货分 kind 如实记（exchange_configs 缺=id 空，
	// NULL 下唯一键不生效会重灌——缺配置则跳账并在推送中文备注）。
	cfgID, cerr := e.Store.ExchangeConfigID(ctx, "binance", "spot")
	if cerr == nil && cfgID > 0 {
		var evs []pgstore.IncomeEvent
		if interest > 0 {
			evs = append(evs, pgstore.IncomeEvent{
				Venue: "binance", VenueType: "option", ConfigID: cfgID,
				Symbol:     o.Underlying,
				IncomeType: "dci_interest",
				AmountUSD:  e.usdOf(ctx, o.InvestCcy, interest),
				TranID:     o.ID*10 + 1, Ts: e.Now()})
		}
		if qty > 0 && ccy != "" {
			evs = append(evs, pgstore.IncomeEvent{
				Venue: "binance", VenueType: "option", ConfigID: cfgID,
				Symbol:     o.Underlying,
				IncomeType: "dci_exercise",
				AmountUSD:  e.usdOf(ctx, ccy, qty),
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
	return nil
}

// usdOf 原币→USD 换算（USD 族 1:1；其余走现货 ticker，读败=0 如实不入账）。
func (e *Executor) usdOf(ctx context.Context, ccy string, qty float64) float64 {
	switch strings.ToUpper(ccy) {
	case "USD", "USDT", "USDC", "BUSD", "FDUSD":
		return qty
	}
	px, err := e.spotPrice(ctx, strings.ToUpper(ccy)+"USDT")
	if err != nil || px <= 0 {
		return 0
	}
	return qty * px
}
