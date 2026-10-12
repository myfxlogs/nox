// okx_ws_funding.go —— okx funding-rate 频道 fact 发射（自 okx_ws.go 迁出，
// D-092 行数纪律，纯移动零语义变更）。
package exchange

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/fact"
)

// handleFundingRate D-509 S4：funding-rate 频道帧→funding 三 fact。
// 字段（v5 public）：fundingRate（string）、fundingTime/nextFundingTime（ms string）——
// periodsPerDay=fundingPeriodsPerDay(next−funding)（venue 自家字段算间隔，D-079 口径）。
// 缺字段=缺省不造（venue 禁借用律）。
func (o *OKXWebSocket) handleFundingRate(ctx context.Context, msg wsMsg) {
	if o.emitFacts == nil || len(msg.Data) == 0 {
		return
	}
	instID, _ := msg.Arg.InstID.(string)
	if instID == "" {
		return
	}
	item := msg.Data[0]
	rateS, _ := item["fundingRate"].(string)
	ftS, _ := item["fundingTime"].(string)
	nftS, _ := item["nextFundingTime"].(string)
	if rateS == "" {
		return
	}
	var rate, ftMs, nftMs float64
	fmt.Sscanf(rateS, "%f", &rate)
	fmt.Sscanf(ftS, "%f", &ftMs)
	fmt.Sscanf(nftS, "%f", &nftMs)
	sym := strings.ReplaceAll(strings.TrimSuffix(instID, "-SWAP"), "-", "")
	intervalMs := nftMs - ftMs
	if intervalMs <= 0 {
		log.Printf("[okx-ws] %s funding interval missing (ft=%v nft=%v), fallback 3/day", sym, ftMs, nftMs)
	}
	now := time.Now()
	fs := []fact.Fact{
		{Kind: fact.KindFunding, Venue: VenueOKX, Symbol: sym,
			Value: annualizeFunding(rate, fundingPeriodsPerDay(intervalMs)),
			Unit:  fact.UnitPctAnnualized, Ts: now, Src: "okx_ws_v5"},
	}
	if ftMs > 0 {
		// D-092：NextSettle 发射 fundingTime（当前期结算时刻=真·下次结算），非
		// nextFundingTime（再下一期）——旧值致前端倒计时恒多一期（业主实见 13:52:38，
		// 真值 ~05:52）。两 venue 字段语义对照表（10-06 okx 官方端点实拍 ft=16:00Z、
		// nft=次日 00:00Z，防后人按字段名「统一」回去）：binance markprice 流
		// nextFundingTime=下次结算；okx funding-rate 流 fundingTime=下次结算、
		// nextFundingTime=再下一期。
		// D-033 自盯：venue 值不篡改，异常陈旧（ft≤now）或越界（ft−now>interval）
		// 只采样 warn——未来 venue 字段再变语义时可见。
		ahead := time.Duration(int64(ftMs)-time.Now().UnixMilli()) * time.Millisecond
		if ahead <= 0 || (intervalMs > 0 && ahead > time.Duration(int64(intervalMs))*time.Millisecond) {
			log.Printf("[okx-ws] %s fundingTime 越界（ahead=%s interval=%s）——venue 字段语义或时钟异常，观察", sym, ahead, time.Duration(intervalMs)*time.Millisecond)
		}
		fs = append(fs, fact.Fact{Kind: fact.KindFundingNextSettle, Venue: VenueOKX,
			Symbol: sym, Value: ftMs / 1000, Unit: fact.UnitUnixSec, Ts: now, Src: "okx_ws_v5"})
	}
	if intervalMs > 0 {
		fs = append(fs, fact.Fact{Kind: fact.KindFundingInterval, Venue: VenueOKX,
			Symbol: sym, Value: intervalMs / 1000, Unit: fact.UnitSeconds, Ts: now, Src: "okx_ws_v5"})
	}
	if err := o.emitFacts(ctx, fs); err != nil {
		log.Printf("[okx-ws] funding emit %s: %v", instID, err)
	}
}
