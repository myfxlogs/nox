// d430_tickkey_test.go —— D-430 T1-T3：legTickKey 纯函数键解析矩阵。
package dashboard

import "testing"

// TestD430_LegTickKey T1-T3：tick 域键解析矩阵（venue_type 原样直通 + symbol 分域归一）。
func TestD430_LegTickKey(t *testing.T) {
	mtCanon := func(venue, sym string) (string, bool) {
		if venue == "3001157" && sym == "XAUUSDm" {
			return "XAUUSD", true
		}
		return "", false
	}
	// T1 MT 腿：venue_symbol→canonical，venue_type=mt5 原样
	if tv, tt, ts, ok := legTickKey("3001157:mt5", "XAUUSDm", mtCanon); !ok ||
		tv != "3001157" || tt != "mt5" || ts != "XAUUSD" {
		t.Fatalf("T1 = (%s,%s,%s,%v), want (3001157,mt5,XAUUSD,true)", tv, tt, ts, ok)
	}
	// T2 okx perp 腿：venue_type 保 perp 不归一 unified（反向锚），-SWAP 归一
	if tv, tt, ts, ok := legTickKey("okx:perp", "XAU-USDT-SWAP", mtCanon); !ok ||
		tv != "okx" || tt != "perp" || ts != "XAUUSDT" {
		t.Fatalf("T2 = (%s,%s,%s,%v), want (okx,perp,XAUUSDT,true)", tv, tt, ts, ok)
	}
	if _, tt, _, _ := legTickKey("okx:perp", "XAU-USDT-SWAP", mtCanon); tt == "unified" {
		t.Fatal("T2 反向锚：venue_type 不得归一为 unified")
	}
	// T3 gate perp 腿：下划线归一
	if tv, tt, ts, ok := legTickKey("gate:perp", "CL_USDT", mtCanon); !ok ||
		tv != "gate" || tt != "perp" || ts != "CLUSDT" {
		t.Fatalf("T3 = (%s,%s,%s,%v), want (gate,perp,CLUSDT,true)", tv, tt, ts, ok)
	}
	// MT 未映射 / 未知 suffix / 无冒号 → ok=false
	if _, _, _, ok := legTickKey("xxx:mt5", "FOOm", mtCanon); ok {
		t.Fatal("MT 未映射应 ok=false")
	}
	if _, _, _, ok := legTickKey("binance:futures", "BTCUSDT", mtCanon); ok {
		t.Fatal("未知 suffix 应 ok=false")
	}
	if _, _, _, ok := legTickKey("nocolon", "X", mtCanon); ok {
		t.Fatal("无冒号 venueKey 应 ok=false")
	}
}
