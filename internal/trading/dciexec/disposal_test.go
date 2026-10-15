package dciexec

// disposal_test.go —— D-517 接货自动卖回锚：行权 BTC→市价卖回+净损益落账；
// 卖败有界→pending_disposal 留仓标+warn；USD 族/同币到账不处置。

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

func TestD517_ExerciseSellback(t *testing.T) {
	st := newFake()
	st.cfgID = 42
	var sellSym string
	var sellQty float64
	ex := &Executor{SigmaFn: testSigma,
		Store: st, BnKey: "k", Now: time.Now,
		PositionsFn: func(context.Context) ([]DCIPosition, error) {
			return []DCIPosition{{
				ID: "PUTP", OptionType: "PUT", Status: "SETTLED",
				ExercisedCoin: "BTC", ExercisedAmt: `0.0045`, InterestAmt: `1.2`,
			}}, nil
		},
		PriceFn: func(context.Context, string) (float64, error) { return 80000, nil },
		SpotFiltersFn: func(context.Context, string) (float64, float64, int, error) {
			return 0.00001, 0.00001, 5, nil
		},
		SpotSellFn: func(_ context.Context, sym string, qty float64) (float64, error) {
			sellSym, sellQty = sym, qty
			return 350.0, nil // 卖回得 350 USDT（付额 541→亏 191 如实记负）
		},
	}
	st.filledOrders = []pgstore.FilledDCIOrder{d207Order()}
	if n, err := ex.SettleExpiredOrders(context.Background()); err != nil || n != 1 {
		t.Fatalf("settle n=%d err=%v", n, err)
	}
	if sellSym != "BTCUSDT" || sellQty != 0.0045 {
		t.Fatalf("卖回对拍败: %s %.8f", sellSym, sellQty)
	}
	var disp bool
	for _, ev := range st.incomeEvs {
		if ev.IncomeType == "dci_exercise_disposal" {
			disp = true
			if ev.AmountUSD != 350.0-541.0 {
				t.Fatalf("处置净损益对拍败: %v（want 350-541=-191）", ev.AmountUSD)
			}
			if ev.VenueType != "spot" {
				t.Fatalf("处置 venue_type 对拍败: %q", ev.VenueType)
			}
		}
	}
	if !disp {
		t.Fatal("dci_exercise_disposal 损益行未落")
	}
	var warn string
	for _, a := range st.alerts {
		if strings.HasPrefix(a, "warn:") && strings.Contains(a, "已市价卖回") {
			warn = a
		}
	}
	if warn == "" {
		t.Fatalf("卖回 warn 推送未落: %v", st.alerts)
	}
	if st.posRows != 0 {
		t.Fatalf("卖回成功却落 pending_disposal 行: %d", st.posRows)
	}
}

func TestD517_SellFail_PendingDisposal(t *testing.T) {
	st := newFake()
	st.cfgID = 42
	calls := 0
	ex := &Executor{SigmaFn: testSigma,
		Store: st, BnKey: "k", Now: time.Now,
		PositionsFn: func(context.Context) ([]DCIPosition, error) {
			return []DCIPosition{{
				ID: "PUTP", OptionType: "PUT", Status: "SETTLED",
				ExercisedCoin: "BTC", ExercisedAmt: `0.0045`,
			}}, nil
		},
		PriceFn: func(context.Context, string) (float64, error) { return 80000, nil },
		SpotFiltersFn: func(context.Context, string) (float64, float64, int, error) {
			return 0.00001, 0.00001, 5, nil
		},
		SpotSellFn: func(context.Context, string, float64) (float64, error) {
			calls++
			return 0, fmt.Errorf("venue 权限缺")
		},
	}
	st.filledOrders = []pgstore.FilledDCIOrder{d207Order()}
	if n, err := ex.SettleExpiredOrders(context.Background()); err != nil || n != 1 {
		t.Fatalf("settle n=%d err=%v", n, err)
	}
	if calls != disposalMaxRetry {
		t.Fatalf("有界重试对拍败: %d want %d", calls, disposalMaxRetry)
	}
	if st.posRows != 1 {
		t.Fatalf("pending_disposal 留仓标未落: %d", st.posRows)
	}
	var pend bool
	for _, a := range st.alerts {
		if strings.Contains(a, "pending_disposal") {
			pend = true
		}
	}
	if !pend {
		t.Fatalf("留仓告警未落: %v", st.alerts)
	}
	for _, ev := range st.incomeEvs {
		if ev.IncomeType == "dci_exercise_disposal" {
			t.Fatal("卖败仍落处置损益行——亏得编不得")
		}
	}
}

func TestD517_NoDisposal_SameCcy(t *testing.T) {
	st := newFake()
	st.cfgID = 42
	ex := &Executor{SigmaFn: testSigma,
		Store: st, BnKey: "k", Now: time.Now,
		PositionsFn: func(context.Context) ([]DCIPosition, error) {
			return []DCIPosition{{
				ID: "PUTP", OptionType: "PUT", Status: "SETTLED",
				SettleAsset: "USDT", SettleAmount: `542.4`, SubscriptionAmount: `541`,
			}}, nil
		},
		PriceFn: func(context.Context, string) (float64, error) { return 80000, nil },
		SpotSellFn: func(context.Context, string, float64) (float64, error) {
			t.Fatal("USD 族到账仍触发卖回——disposeExercise 域外")
			return 0, nil
		},
	}
	st.filledOrders = []pgstore.FilledDCIOrder{d207Order()}
	if n, err := ex.SettleExpiredOrders(context.Background()); err != nil || n != 1 {
		t.Fatalf("settle n=%d err=%v", n, err)
	}
	if st.posRows != 0 {
		t.Fatal("未行权仍落 pending_disposal")
	}
}

// TestD542_LotSizeFloor 10-15 事故锚：0.00433438 BTC 越 stepSize=0.00001 精度
// → -1013 必败；修后报量 floor=0.00433 成功。
func TestD542_LotSizeFloor(t *testing.T) {
	st := newFake()
	st.cfgID = 42
	var sellQty float64
	ex := &Executor{SigmaFn: testSigma,
		Store: st, BnKey: "k", Now: time.Now,
		SpotFiltersFn: func(context.Context, string) (float64, float64, int, error) {
			return 0.00001, 0.00001, 5, nil
		},
		PositionsFn: func(context.Context) ([]DCIPosition, error) {
			return []DCIPosition{{
				ID: "PUTP", OptionType: "PUT", Status: "SETTLED",
				ExercisedCoin: "BTC", ExercisedAmt: `0.00433438`, InterestAmt: `0`,
			}}, nil
		},
		PriceFn: func(context.Context, string) (float64, error) { return 80000, nil },
		SpotSellFn: func(_ context.Context, _ string, qty float64) (float64, error) {
			sellQty = qty
			return 346.64, nil
		},
	}
	st.filledOrders = []pgstore.FilledDCIOrder{d207Order()}
	if n, err := ex.SettleExpiredOrders(context.Background()); err != nil || n != 1 {
		t.Fatalf("settle n=%d err=%v", n, err)
	}
	if sellQty != 0.00433 {
		t.Fatalf("floor 报量对拍败: %.8f（want 0.00433）", sellQty)
	}
}

// TestD542_SweepPendingDisposal 清扫锚：pending_disposal OPEN 行→重试卖回成功
// →损益入账+关行+warn；卖败留行不刷告警（dedup 去重面交给 store）。
func TestD542_SweepPendingDisposal(t *testing.T) {
	st := newFake()
	st.cfgID = 42
	qty := 0.00433
	st.positions = []pgstore.PersonalPosition{{
		ID: 9, Asset: "BTC", Kind: "other", Status: "OPEN", Qty: &qty,
		Note: "pending_disposal D-517：DCI#11 行权接货待卖回（LOT_SIZE 尽败）",
	}}
	st.orders = map[string]*pgstore.DCIOrder{"r11": {ID: 11, Qty: 351.0, Underlying: "BTCUSDT"}}
	ex := &Executor{Store: st, BnKey: "k", Now: time.Now,
		SpotFiltersFn: func(context.Context, string) (float64, float64, int, error) {
			return 0.00001, 0.00001, 5, nil
		},
		SpotSellFn: func(context.Context, string, float64) (float64, error) { return 346.4, nil },
	}
	n, err := ex.SweepPendingDisposals(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("sweep n=%d err=%v", n, err)
	}
	var disp bool
	for _, ev := range st.incomeEvs {
		if ev.IncomeType == "dci_exercise_disposal" && ev.AmountUSD > -4.61 && ev.AmountUSD < -4.59 {
			disp = true
		}
	}
	if !disp {
		t.Fatalf("清扫损益行未落或对拍败: %+v", st.incomeEvs)
	}
	var closed bool
	for _, a := range st.alerts {
		if strings.Contains(a, "已市价卖回得") {
			closed = true
		}
	}
	if !closed {
		t.Fatalf("清扫 warn 未发: %+v", st.alerts)
	}
}

// TestD542_SweepUnanchored 锚缺面：note 有 DCI#id 但台账查无/读败——卖回照做
// （真账）但 income 行不入账（得额≠损益，10-15 实证 invest=0 虚增 +352 净利）。
// 关行+warn 仍落，文案明示待人工补记。
func TestD542_SweepUnanchored(t *testing.T) {
	st := newFake()
	st.cfgID = 42
	qty := 0.00433
	st.positions = []pgstore.PersonalPosition{{
		ID: 9, Asset: "BTC", Kind: "other", Status: "OPEN", Qty: &qty,
		Note: "pending_disposal D-517：DCI#99 行权接货待卖回",
	}}
	// orders 表空——id=99 查无
	ex := &Executor{Store: st, BnKey: "k", Now: time.Now,
		SpotFiltersFn: func(context.Context, string) (float64, float64, int, error) {
			return 0.00001, 0.00001, 5, nil
		},
		SpotSellFn: func(context.Context, string, float64) (float64, error) { return 346.4, nil },
	}
	n, err := ex.SweepPendingDisposals(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("sweep n=%d err=%v", n, err)
	}
	for _, ev := range st.incomeEvs {
		if ev.IncomeType == "dci_exercise_disposal" {
			t.Fatalf("锚缺仍记损益行（得额冒充净利）: %+v", ev)
		}
	}
	var warned bool
	for _, a := range st.alerts {
		if strings.Contains(a, "锚缺") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("锚缺告警未明示: %+v", st.alerts)
	}
}
