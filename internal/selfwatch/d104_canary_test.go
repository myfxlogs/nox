// d104_canary_test.go —— D-104 锚（复审返修版）：出口 canary 拍逻辑——首拍全红零告警
// （只更新内态）、连续 2 拍全红才单条 critical（30min 去重形态）+聚合闸置位、确认红窗
// 恢复→单条 info、疑似单拍红后恢复=静默收场、每拍 fact 落库（值=通过锚数）、
// 锚点 env 覆盖解析。
package selfwatch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"arb/internal/fact"
)

func d104Deps(st *fakeStore) Deps {
	return Deps{Store: st, Now: func() time.Time { return time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC) }}
}

// setDial 注入拨测替身，返回复原函数（并复位红窗态防跨测试泄漏）。
func setDial(f func(addr string) error) func() {
	prev := dialAnchor
	dialAnchor = f
	SetEgressRed(false)
	return func() { dialAnchor = prev; SetEgressRed(false) }
}

var d104Anchors = []string{"a:443", "b:443", "c:443"}

// TestD104_CanaryTwoRoundConfirm 复审返修①②主锚：首拍全红零告警（fact 照落+内态不置
// 闸）；第二拍仍全红→单条 critical（30min 窗）+闸置位；持续红零重发；恢复→fact 值 3+
// 单条 info+翻绿清闸。
func TestD104_CanaryTwoRoundConfirm(t *testing.T) {
	defer setDial(func(string) error { return errors.New("dial tcp: timeout") })()

	st := &fakeStore{}
	d := d104Deps(st)

	// 首拍全红：零告警、零闸位，fact 值 0 照落（内态留给下拍）。
	red, confirmed := d.canaryRound(context.Background(), d104Anchors, false, false)
	if !red || confirmed || EgressRed() {
		t.Fatalf("首拍全红应只更新内态: red=%v confirmed=%v gate=%v", red, confirmed, EgressRed())
	}
	if len(st.dedupCalls) != 0 {
		t.Fatalf("单拍全红零告警: %v", st.dedupCalls)
	}
	if len(st.facts) != 1 || st.facts[0].Value != 0 || st.facts[0].Kind != fact.KindEgressCanary {
		t.Fatalf("首拍 fact 应 egress_canary 值 0: %+v", st.facts)
	}
	if st.facts[0].Venue != "local:egress" || st.facts[0].Unit != fact.UnitCount || st.facts[0].Src != "egress_canary" {
		t.Fatalf("fact 键域/口径不符: %+v", st.facts[0])
	}

	// 第二拍仍全红：红窗确认→单条 critical（30min 去重形态）+闸置位。
	red, confirmed = d.canaryRound(context.Background(), d104Anchors, true, false)
	if !red || !confirmed || !EgressRed() {
		t.Fatalf("连续 2 拍全红应确认红窗: red=%v confirmed=%v gate=%v", red, confirmed, EgressRed())
	}
	if len(st.dedupCalls) != 1 || !strings.HasPrefix(st.dedupCalls[0], "critical|[canary] 本机出口故障") {
		t.Fatalf("确认拍应单条 critical: %v", st.dedupCalls)
	}
	if !strings.Contains(st.dedupCalls[0], "30m0s") {
		t.Fatalf("critical 应带 30min 去重窗: %v", st.dedupCalls[0])
	}

	// 持续红：零新告警，fact 照落。
	if _, confirmed = d.canaryRound(context.Background(), d104Anchors, true, true); !confirmed {
		t.Fatal("持续失败应保持红窗")
	}
	if len(st.dedupCalls) != 1 {
		t.Fatalf("持续红 30min 窗内不应重发: %v", st.dedupCalls)
	}
	if len(st.facts) != 3 {
		t.Fatalf("每拍都应落 fact: %d", len(st.facts))
	}

	// 恢复：fact 值 3 + 单条 info + 翻绿清闸。
	restore := setDial(func(string) error { return nil })
	defer restore()
	red, confirmed = d.canaryRound(context.Background(), d104Anchors, true, true)
	if red || confirmed || EgressRed() {
		t.Fatalf("恢复后应翻绿清闸: red=%v confirmed=%v gate=%v", red, confirmed, EgressRed())
	}
	if len(st.facts) != 4 || st.facts[3].Value != 3 {
		t.Fatalf("恢复拍 fact 应值 3: %+v", st.facts)
	}
	if len(st.dedupCalls) != 2 || !strings.HasPrefix(st.dedupCalls[1], "info|[canary] 本机出口恢复") {
		t.Fatalf("恢复应单条 info: %v", st.dedupCalls)
	}
	if !strings.Contains(st.dedupCalls[1], "30m0s") {
		t.Fatalf("info 应带 30min 去重窗: %v", st.dedupCalls[1])
	}
}

// TestD104_CanarySuspectSilentClose 疑似单拍红后恢复=静默收场——critical 未发过，恢复
// 也不发 info（15s 级瞬时抖动全程零声，降噪本义）；随后再连续 2 拍全红才重新确认。
func TestD104_CanarySuspectSilentClose(t *testing.T) {
	defer setDial(func(string) error { return errors.New("refused") })()
	st := &fakeStore{}
	d := d104Deps(st)

	// 首拍红（疑似）→次拍恢复：零告警零 info。
	if red, confirmed := d.canaryRound(context.Background(), d104Anchors, false, false); !red || confirmed {
		t.Fatalf("首拍应疑似红: red=%v confirmed=%v", red, confirmed)
	}
	restore := setDial(func(string) error { return nil })
	defer restore()
	if red, confirmed := d.canaryRound(context.Background(), d104Anchors, true, false); red || confirmed {
		t.Fatalf("疑似后恢复应翻绿: red=%v confirmed=%v", red, confirmed)
	}
	if len(st.dedupCalls) != 0 || EgressRed() {
		t.Fatalf("疑似窗收场应全程静默: %v gate=%v", st.dedupCalls, EgressRed())
	}

	// 重新连续 2 拍全红：第二拍才 critical（计数不被前窗污染）。
	restore2 := setDial(func(string) error { return errors.New("timeout") })
	defer restore2()
	if red, confirmed := d.canaryRound(context.Background(), d104Anchors, false, false); !red || confirmed {
		t.Fatalf("再疑似应零告警: red=%v confirmed=%v", red, confirmed)
	}
	if len(st.dedupCalls) != 0 {
		t.Fatalf("再疑似首拍零告警: %v", st.dedupCalls)
	}
	if _, confirmed := d.canaryRound(context.Background(), d104Anchors, true, false); !confirmed {
		t.Fatal("再连续 2 拍全红应确认")
	}
	if len(st.dedupCalls) != 1 || !strings.HasPrefix(st.dedupCalls[0], "critical|") {
		t.Fatalf("再确认应单条 critical: %v", st.dedupCalls)
	}
}

// TestD104_CanaryPartialOK 部分锚通过=非红窗（值>0 零告警）——mtapi 单面故障不误报出口挂。
func TestD104_CanaryPartialOK(t *testing.T) {
	defer setDial(func(addr string) error {
		if addr == "b:443" {
			return errors.New("refused")
		}
		return nil
	})()
	st := &fakeStore{}
	if red, confirmed := d104Deps(st).canaryRound(context.Background(), d104Anchors, false, false); red || confirmed {
		t.Fatalf("单锚失败不应进红窗: red=%v confirmed=%v", red, confirmed)
	}
	if len(st.facts) != 1 || st.facts[0].Value != 2 {
		t.Fatalf("fact 应值 2（两锚通过）: %+v", st.facts)
	}
	if len(st.dedupCalls) != 0 {
		t.Fatalf("非红窗零告警: %v", st.dedupCalls)
	}
}

// TestD104_EgressAnchors（D-145 改锚：app_config 行覆盖解析）：行缺→三锚缺省 /
// 逗号分隔 trim / 全分隔符=空集停用形态。
func TestD104_EgressAnchors(t *testing.T) {
	ctx := context.Background()
	d := d104Deps(&fakeStore{})
	if len(egressAnchors(ctx, d)) != 3 {
		t.Fatalf("缺省应三锚: %v", egressAnchors(ctx, d))
	}
	st := &fakeStore{cfg: map[string]string{cfgEgressAnchors: " x:443 , y:443,,"}}
	got := egressAnchors(ctx, d104Deps(st))
	if len(got) != 2 || got[0] != "x:443" || got[1] != "y:443" {
		t.Fatalf("行覆盖应 trim 空段: %v", got)
	}
	st.cfg[cfgEgressAnchors] = " , ,"
	if len(egressAnchors(ctx, d104Deps(st))) != 0 {
		t.Fatalf("全分隔符应空集: %v", egressAnchors(ctx, d104Deps(st)))
	}
}
