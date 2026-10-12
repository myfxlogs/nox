package exec

import (
	"fmt"
	"math"
	"strconv"
	"time"
)

// === Gate（USDT 本位合约 perp，D-098 G1）===

// gateFinishFill 按 finish_as 判定终态订单成交（D-098 返修包① M2）：
//   - finish_as=="filled" → 全成交 Fill（qty=|size-left|，filled 时 left=0 即 |size|）；
//   - finish_as!="filled"（ioc/cancelled/stp/liquidated/reduce_only/position_closed/...）：
//     qty=|size-left|>0 → 如实部分成交 Fill；qty==0 → error 含 finish_as（未成交，防 0-qty 假成交）。
//
// okx 教训：IOC 零成交（finished+finish_as=ioc+left=size）旧逻辑落入 fallthrough 返
// Fill{Qty:0,Price:0},nil → executeLeg 记 FILLED 0-qty 腿。本函数守卫 qty==0 → error。
func gateFinishFill(o gateOrderResponse) (Fill, error) {
	qty, price, err := gateFilled(o)
	if err != nil {
		return Fill{}, err
	}
	id := strconv.FormatInt(o.ID, 10)
	if o.FinishAs == "filled" {
		return Fill{OrderID: id, Price: price, Qty: qty, Ts: time.Now()}, nil
	}
	if qty > 0 {
		return Fill{OrderID: id, Price: price, Qty: qty, Ts: time.Now()}, nil
	}
	// qty==0：未成交（IOC 零成交 / cancelled / stp / liquidated / ...）→ 如实报错含 finish_as
	return Fill{}, fmt.Errorf("gate order %d: finish_as=%s 未成交（size=%d left=%d）",
		o.ID, o.FinishAs, o.Size, o.Left)
}

// gateFilled 从订单对象计算已成交 qty（|size-left|）与均价（fill_price）。
// fill_price 解析失败=脏成交数据，error 上抛不静默归零（下单路径）。
func gateFilled(o gateOrderResponse) (float64, float64, error) {
	filled := o.Size - o.Left // 带符号，绝对值即成交合约数
	qty := math.Abs(float64(filled))
	price, err := f64z(o.FillPrice)
	if err != nil {
		return 0, 0, fmt.Errorf("gate order %d fill_price=%q: %w", o.ID, o.FillPrice, err)
	}
	return qty, price, nil
}
