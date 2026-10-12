package dciexec

// settle_subscriptions_test.go —— M3-④ 交割观察死端锚：持仓行随交割移出
// positions（生产 #7/#8 实证 row==nil 永跳）→status=SETTLED 终态端点确认翻
// settled；匹配键=response.positionId（产品号≠仓位号，切流实拍 #7 实证）。

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

// 持仓行缺+SETTLED 终态行→翻态+入账（死端修复锚）。终态行 id=仓位号与
// product_ref 异号段（生产 #7 实证 id=18537557 vs product_ref=2698318），
// 匹配靠订单 response.positionId。
func TestSettleViaSubscriptions(t *testing.T) {
	st := newFake()
	o := d207Order()
	o.Response = `{"positionId":"18537557","settleDate":` + fmt.Sprint(time.Now().Add(-time.Hour).UnixMilli()) + `}`
	st.filledOrders = []pgstore.FilledDCIOrder{o}
	st.cfgID = 42
	ex := &Executor{
		Store: st,
		Now:   time.Now,
		PositionsFn: func(context.Context) ([]DCIPosition, error) {
			return nil, nil // venue 已移出持仓行（死端原景）
		},
		SettledPositionsFn: func(context.Context) ([]DCIPosition, error) {
			unex := false
			return []DCIPosition{{
				ID: "18537557", OptionType: "PUT", Status: "SETTLED",
				IsExercised: &unex, SettleAsset: "USDT",
				SettleAmount: "542.4066", SubscriptionAmount: "541",
			}}, nil
		},
		PriceFn: func(context.Context, string) (float64, error) { return 80000, nil },
	}
	n, err := ex.SettleExpiredOrders(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("settle n=%d err=%v", n, err)
	}
	if len(st.settled) != 1 || st.settled[0].ccy != "USDT" {
		t.Fatalf("翻态行对拍败: %+v", st.settled)
	}
	if got := st.settled[0].income; got < 1.4 || got > 1.42 {
		t.Fatalf("利息=settleAmount−subscriptionAmount 对拍败: %v", got)
	}
	var warn string
	for _, a := range st.alerts {
		if strings.HasPrefix(a, "warn:") {
			warn = a
		}
	}
	if !strings.Contains(warn, "已交割") {
		t.Fatalf("warn 推送对拍败: %q", warn)
	}
}

// 终态行 HOLDING（未到期）→不翻；双源俱缺→不翻仍跳（fail-closed 留重试）。
func TestSettleSubscriptionsGuards(t *testing.T) {
	st := newFake()
	st.filledOrders = []pgstore.FilledDCIOrder{d207Order()}
	ex := &Executor{
		Store:       st,
		Now:         time.Now,
		PositionsFn: func(context.Context) ([]DCIPosition, error) { return nil, nil },
		SettledPositionsFn: func(context.Context) ([]DCIPosition, error) {
			return []DCIPosition{{ID: "PUTP", OptionType: "PUT", Status: "HOLDING"}}, nil
		},
	}
	if n, _ := ex.SettleExpiredOrders(context.Background()); n != 0 || len(st.settled) != 0 {
		t.Fatalf("终态行 HOLDING 不应翻: n=%d", n)
	}
	ex.SettledPositionsFn = func(context.Context) ([]DCIPosition, error) { return nil, nil }
	if n, _ := ex.SettleExpiredOrders(context.Background()); n != 0 || len(st.settled) != 0 {
		t.Fatalf("双源俱缺不应翻: n=%d", n)
	}
	// 终态源读败→不翻且整拍不重试爆（其他单下拍再确认）。
	ex.SettledPositionsFn = func(context.Context) ([]DCIPosition, error) { return nil, fmt.Errorf("venue 500") }
	if n, err := ex.SettleExpiredOrders(context.Background()); err != nil || n != 0 || len(st.settled) != 0 {
		t.Fatalf("终态源读败不应翻: n=%d err=%v", n, err)
	}
}
