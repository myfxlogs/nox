package adapter

// D-560 T1/T2 锚：emitBlocked 变体降格原生键不占 canonical 槽；primary/未映射原样。
import (
	"testing"
)

func TestD560_PublishSymbol(t *testing.T) {
	blocked := map[string]bool{"XAUUSD247m": true}
	if got := publishSymbol("XAUUSD247m", "XAUUSD", blocked); got != "XAUUSD247m" {
		t.Errorf("blocked 命中应降格原生键, got %s", got)
	}
	if got := publishSymbol("XAUUSDm", "XAUUSD", blocked); got != "XAUUSD" {
		t.Errorf("primary 应 canonical 原样, got %s", got)
	}
	if got := publishSymbol("EURUSDm", "EURUSDm", blocked); got != "EURUSDm" {
		t.Errorf("未映射行应原样, got %s", got)
	}
	if got := publishSymbol("XAUUSD247m", "XAUUSD", nil); got != "XAUUSD" {
		t.Errorf("blocked=nil 应 canonical 原样, got %s", got)
	}
}

// T2 适配器级：SetCanonicalEmitBlocked+SetSymbolMap 装配后变体帧降格、primary 帧占槽。
func TestD560_AdapterEmitBlockedQuote(t *testing.T) {
	a := &MT5Adapter{}
	a.SetSymbolMap(map[string]string{"XAUUSDm": "XAUUSD", "XAUUSD247m": "XAUUSD"})
	a.SetCanonicalEmitBlocked(map[string]bool{"XAUUSD247m": true})
	got := publishSymbol("XAUUSD247m", a.canonical("XAUUSD247m"), a.emitBlockedSet())
	if got != "XAUUSD247m" {
		t.Errorf("变体帧 Symbol 应为原生键 XAUUSD247m（不占 canonical 槽）, got %s", got)
	}
	if got := publishSymbol("XAUUSDm", a.canonical("XAUUSDm"), a.emitBlockedSet()); got != "XAUUSD" {
		t.Errorf("primary 帧 Symbol 应占 canonical 槽, got %s", got)
	}
}
