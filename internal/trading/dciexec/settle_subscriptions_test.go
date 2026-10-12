package dciexec

// settle_subscriptions_test.go —— M3-④ 交割观察死端锚：持仓行随交割移出
// positions（生产 #7/#8 实证 row==nil 永跳）→申购史端点终态确认翻 settled。

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

// 持仓行缺+申购史终态行→翻态+入账（死端修复锚）。
func TestSettleViaSubscriptions(t *testing.T) {
	st := newFake()
	st.filledOrders = []pgstore.FilledDCIOrder{d207Order()}
	st.cfgID = 42
	ex := &Executor{
		Store: st,
		Now:   time.Now,
		PositionsFn: func(context.Context) ([]DCIPosition, error) {
			return nil, nil // venue 已移出持仓行（死端原景）
		},
		SubscriptionsFn: func(context.Context) ([]DCIPosition, error) {
			return []DCIPosition{{
				ID: "PUTP", OptionType: "PUT", Status: "SETTLED",
				ExercisedCoin: "BTC", ExercisedAmt: `0.0045`, InterestAmt: `1.2`,
			}}, nil
		},
		PriceFn: func(context.Context, string) (float64, error) { return 80000, nil },
	}
	n, err := ex.SettleExpiredOrders(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("settle n=%d err=%v", n, err)
	}
	if len(st.settled) != 1 || st.settled[0].ccy != "BTC" {
		t.Fatalf("翻态行对拍败: %+v", st.settled)
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

// 申购史行 HOLDING（未到期）→不翻；双源俱缺→不翻仍跳（fail-closed 留重试）。
func TestSettleSubscriptionsGuards(t *testing.T) {
	st := newFake()
	st.filledOrders = []pgstore.FilledDCIOrder{d207Order()}
	ex := &Executor{
		Store:       st,
		Now:         time.Now,
		PositionsFn: func(context.Context) ([]DCIPosition, error) { return nil, nil },
		SubscriptionsFn: func(context.Context) ([]DCIPosition, error) {
			return []DCIPosition{{ID: "PUTP", OptionType: "PUT", Status: "HOLDING"}}, nil
		},
	}
	if n, _ := ex.SettleExpiredOrders(context.Background()); n != 0 || len(st.settled) != 0 {
		t.Fatalf("申购史 HOLDING 不应翻: n=%d", n)
	}
	ex.SubscriptionsFn = func(context.Context) ([]DCIPosition, error) { return nil, nil }
	if n, _ := ex.SettleExpiredOrders(context.Background()); n != 0 || len(st.settled) != 0 {
		t.Fatalf("双源俱缺不应翻: n=%d", n)
	}
	// 申购史读败→不翻且整拍不重试爆（其他单下拍再确认）。
	ex.SubscriptionsFn = func(context.Context) ([]DCIPosition, error) { return nil, fmt.Errorf("venue 500") }
	if n, err := ex.SettleExpiredOrders(context.Background()); err != nil || n != 0 || len(st.settled) != 0 {
		t.Fatalf("申购史读败不应翻: n=%d err=%v", n, err)
	}
}
