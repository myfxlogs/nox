// d434r1_ws_buffer_test.go —— D-434 R1-T1（主锚）：okxAccountPushRow buffer 三层回落
// 与 REST accountOKX 同一共享函数（reconcile.OKXBufferMultiple）。WS 推送 data[0] 与
// REST data[0] schema 同构（details 字段集一致）；pos502 实采形态=顶层 mgnRatio 空 +
// details[USDT] 供 mmr/mgnRatio。红=旧码只读顶层 mgnRatio → ①② BufferMultiple 落 nil。
package exchange

import (
	"testing"
	"time"
)

func d434r1Buffer(t *testing.T, data map[string]any) *float64 {
	t.Helper()
	row, ok := okxAccountPushRow(data, time.Now())
	if !ok {
		t.Fatal("合法推送应解析成功")
	}
	return row.BufferMultiple
}

// TestD434R1_WSPushRowBuffer R1-T1 四态矩阵（复用 T6 四 case 数据形态）：
// ①顶层空+details[USDT] mmr/mgnRatio 双供 → eq/Σmmr（eq≈1515.9/mmr≈17.45→≈86.87）
// ②mmr 缺 → detail mgnRatio/100 ③details 空+顶层供 → /100 ④全空 → nil。
func TestD434R1_WSPushRowBuffer(t *testing.T) {
	// ① 顶层空 + details[USDT] mmr/mgnRatio 双供 → eq/mmr 首选（非 mgnRatio/100）
	got := d434r1Buffer(t, map[string]any{
		"mgnRatio": "",
		"details": []map[string]any{
			{"ccy": "USDT", "eqUsd": "1515.9", "cashBal": "600", "mmr": "17.45", "mgnRatio": "77.2"},
		},
	})
	if got == nil || *got < 86.8 || *got > 87.0 {
		t.Fatalf("① buffer=%v want≈86.87（eq/Σmmr）", got)
	}
	// ② mmr 缺 + detail mgnRatio=77.2 → 0.772
	got = d434r1Buffer(t, map[string]any{
		"mgnRatio": "",
		"details": []map[string]any{
			{"ccy": "USDT", "eqUsd": "1515.9", "cashBal": "600", "mgnRatio": "77.2"},
		},
	})
	if got == nil || *got < 0.77 || *got > 0.78 {
		t.Fatalf("② buffer=%v want≈0.772（detail mgnRatio/100）", got)
	}
	// ③ details 无 USDT margin 字段 + 顶层 mgnRatio=50 → 0.5（原行为保留）
	got = d434r1Buffer(t, map[string]any{
		"mgnRatio": "50",
		"details":  []map[string]any{{"ccy": "USDT", "eqUsd": "100", "cashBal": "100"}},
	})
	if got == nil || *got < 0.49 || *got > 0.51 {
		t.Fatalf("③ buffer=%v want≈0.5（顶层回落）", got)
	}
	// ④ 全空 → nil（诚实 data_stale；<=0=nil 语义保留）
	if got = d434r1Buffer(t, map[string]any{
		"mgnRatio": "",
		"details":  []map[string]any{{"ccy": "USDT", "eqUsd": "100", "cashBal": "100"}},
	}); got != nil {
		t.Fatalf("④ buffer=%v want nil（未采诚实）", *got)
	}
}
