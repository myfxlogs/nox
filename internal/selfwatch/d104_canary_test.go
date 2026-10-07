// d104_canary_test.go —— D-104 锚：出口 canary 拍逻辑——全锚失败=红窗（绿→红单条
// critical、30min 去重形态）、持续红不重发、恢复翻绿零告警、每拍 fact 落库（值=通过
// 锚数）、红窗态进程内原子刷新；锚点 env 覆盖解析。
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

// TestD104_CanaryRedTransition 红窗变迁锚：全锚失败→fact 值 0+绿→红单条 critical
// （30min 去重形态）+红窗态置位；持续红→零新告警；恢复→fact 值 3+翻绿零告警。
func TestD104_CanaryRedTransition(t *testing.T) {
	defer setDial(func(string) error { return errors.New("dial tcp: timeout") })()

	st := &fakeStore{}
	d := d104Deps(st)

	// 首拍全败：fact 值 0 + 单条 critical + 红窗置位。
	if red := d.canaryRound(context.Background(), d104Anchors, false); !red {
		t.Fatal("全锚失败应进红窗")
	}
	if !EgressRed() {
		t.Fatal("红窗态应置位")
	}
	if len(st.facts) != 1 || st.facts[0].Value != 0 || st.facts[0].Kind != fact.KindEgressCanary {
		t.Fatalf("首拍 fact 应 egress_canary 值 0: %+v", st.facts)
	}
	if st.facts[0].Venue != "local:egress" || st.facts[0].Unit != fact.UnitCount || st.facts[0].Src != "egress_canary" {
		t.Fatalf("fact 键域/口径不符: %+v", st.facts[0])
	}
	if len(st.dedupCalls) != 1 || !strings.HasPrefix(st.dedupCalls[0], "critical|[canary] 本机出口故障") {
		t.Fatalf("绿→红应单条 critical: %v", st.dedupCalls)
	}
	if !strings.Contains(st.dedupCalls[0], "30m0s") {
		t.Fatalf("critical 应带 30min 去重窗: %v", st.dedupCalls[0])
	}

	// 持续红：零新告警，fact 照落。
	if red := d.canaryRound(context.Background(), d104Anchors, true); !red {
		t.Fatal("持续失败应保持红窗")
	}
	if len(st.dedupCalls) != 1 {
		t.Fatalf("持续红不应重发: %v", st.dedupCalls)
	}
	if len(st.facts) != 2 {
		t.Fatalf("每拍都应落 fact: %d", len(st.facts))
	}

	// 恢复：fact 值 3 + 翻绿 + 零告警。
	restore := setDial(func(string) error { return nil })
	defer restore()
	if red := d.canaryRound(context.Background(), d104Anchors, true); red {
		t.Fatal("恢复后应翻绿")
	}
	if EgressRed() {
		t.Fatal("恢复后红窗态应清除")
	}
	if len(st.facts) != 3 || st.facts[2].Value != 3 {
		t.Fatalf("恢复拍 fact 应值 3: %+v", st.facts)
	}
	if len(st.dedupCalls) != 1 {
		t.Fatalf("恢复不应告警: %v", st.dedupCalls)
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
	if red := d104Deps(st).canaryRound(context.Background(), d104Anchors, false); red {
		t.Fatal("单锚失败不应进红窗")
	}
	if len(st.facts) != 1 || st.facts[0].Value != 2 {
		t.Fatalf("fact 应值 2（两锚通过）: %+v", st.facts)
	}
	if len(st.dedupCalls) != 0 {
		t.Fatalf("非红窗零告警: %v", st.dedupCalls)
	}
}

// TestD104_EgressAnchors env 覆盖解析：缺省三锚 / env 逗号分隔 trim / 全分隔符=空集停用形态。
func TestD104_EgressAnchors(t *testing.T) {
	t.Setenv(cfgEgressAnchors, "")
	if len(egressAnchors()) != 3 {
		t.Fatalf("缺省应三锚: %v", egressAnchors())
	}
	t.Setenv(cfgEgressAnchors, " x:443 , y:443,,")
	got := egressAnchors()
	if len(got) != 2 || got[0] != "x:443" || got[1] != "y:443" {
		t.Fatalf("env 覆盖应 trim 空段: %v", got)
	}
	t.Setenv(cfgEgressAnchors, " , ,")
	if len(egressAnchors()) != 0 {
		t.Fatalf("全分隔符应空集: %v", egressAnchors())
	}
}
