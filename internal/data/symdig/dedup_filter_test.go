// d530_test.go D-530：显式 MaxTokens + stop_reason=max_tokens 分批二轮 + 仍截断 error 升级。
package symdig

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nox/internal/platform/analyst"
	"nox/internal/store/pgstore"
)

func d530Deps(t *testing.T, st *fakeStore, run analystRunner) Deps {
	t.Helper()
	return Deps{Store: st, Client: &analyst.Client{}, Run: run, InboxPath: filepath.Join(t.TempDir(), "inbox.md")}
}

func d530Sum(syms ...string) *Summary {
	sum := &Summary{}
	for _, s := range syms {
		sum.Rows = append(sum.Rows, pgstore.SymdigFinding{Venue: "v1", Symbol: s, Klass: KlassUnmappedUnknown})
	}
	return sum
}

func propDelta(sym string, closed bool) string {
	out := "```symdig-proposals\n[{\"symbol\":\"" + sym + "\",\"venue\":\"v1\",\"proposed_canonical\":\"C" + sym + "\",\"evidence\":\"e\",\"confidence\":0.9}]"
	if closed {
		out += "\n```"
	}
	return out
}

// A3：正常闭合输出 → 单轮，无告警，行为不变。
func TestD530ClosedSingleRound(t *testing.T) {
	st := &fakeStore{}
	var calls atomic.Int32
	run := func(_ context.Context, _ *analyst.Client, _ analyst.ToolExecutor, _ []analyst.ToolSpec,
		_ string, msgs []analyst.Message, cb analyst.LoopCallbacks) (analyst.LoopResult, error) {
		calls.Add(1)
		cb.OnDelta(propDelta("GOLD", true))
		return analyst.LoopResult{StopReason: "end_turn"}, nil
	}
	if err := llmDig(context.Background(), d530Deps(t, st, run), d530Sum("GOLD"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("闭合输出应单轮，calls=%d", calls.Load())
	}
	for _, a := range st.alerts {
		if strings.HasPrefix(a, "critical:") || strings.HasPrefix(a, "warn:") {
			t.Fatalf("闭合输出不应告警: %v", st.alerts)
		}
	}
}

// A1：stop_reason=max_tokens 且未闭合 → 剩余子集二轮，两轮提案合并入箱。
func TestD530TruncatedSecondRound(t *testing.T) {
	st := &fakeStore{}
	var calls atomic.Int32
	run := func(_ context.Context, _ *analyst.Client, _ analyst.ToolExecutor, _ []analyst.ToolSpec,
		_ string, msgs []analyst.Message, cb analyst.LoopCallbacks) (analyst.LoopResult, error) {
		if calls.Add(1) == 1 {
			cb.OnDelta(propDelta("GOLD", false)) // 截断：未闭合，仅覆盖 GOLD
			return analyst.LoopResult{StopReason: "max_tokens"}, nil
		}
		if !strings.Contains(string(msgs[0].Content), "SILVER") || strings.Contains(string(msgs[0].Content), "GOLD") {
			t.Fatalf("第二轮载荷应只含剩余 SILVER: %s", msgs[0].Content)
		}
		cb.OnDelta(propDelta("SILVER", true))
		return analyst.LoopResult{StopReason: "end_turn"}, nil
	}
	inbox := filepath.Join(t.TempDir(), "inbox.md")
	deps := Deps{Store: st, Client: &analyst.Client{}, Run: run, InboxPath: inbox}
	if err := llmDig(context.Background(), deps, d530Sum("GOLD", "SILVER"), time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("2 品种单批 calls=%d", calls.Load())
	}
	data, _ := os.ReadFile(inbox)
	if !strings.Contains(string(data), "GOLD→CGOLD") {
		t.Fatalf("EOF 容忍解析 GOLD 行应入箱:\n%s", data)
	}
	var errs int
	for _, a := range st.alerts {
		if strings.HasPrefix(a, "critical:") {
			errs++
		}
	}
	if errs != 1 {
		t.Fatalf("批内截断应 critical 告警一条: %v", st.alerts)
	}
}

// A2：批内截断 → error 级告警（分批径 max_tokens+malformed 即达）。
func TestD530DoubleTruncatedError(t *testing.T) {
	st := &fakeStore{}
	var calls atomic.Int32
	run := func(_ context.Context, _ *analyst.Client, _ analyst.ToolExecutor, _ []analyst.ToolSpec,
		_ string, _ []analyst.Message, cb analyst.LoopCallbacks) (analyst.LoopResult, error) {
		calls.Add(1)
		cb.OnDelta(propDelta("GOLD", false))
		return analyst.LoopResult{StopReason: "max_tokens"}, nil
	}
	if err := llmDig(context.Background(), d530Deps(t, st, run), d530Sum("GOLD", "SILVER"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("2 品种单批 calls=%d", calls.Load())
	}
	found := false
	for _, a := range st.alerts {
		if strings.HasPrefix(a, "critical:") {
			found = true
		}
	}
	if !found {
		t.Fatalf("批内截断应 critical 告警: %v", st.alerts)
	}
}

// MaxTokens 缺省/装配注入（D-145：env 直读退役——cfg 字段注入，<=0 缺省 8192）。
func TestD530MaxTokensDefault(t *testing.T) {
	d := &Deps{}
	d.fill()
	if d.MaxTokens != 8192 {
		t.Fatalf("缺省 8192, got %d", d.MaxTokens)
	}
	d2 := &Deps{MaxTokens: 4096}
	d2.fill()
	if d2.MaxTokens != 4096 {
		t.Fatalf("注入 4096, got %d", d2.MaxTokens)
	}
}

// 坑2 B1：malformed-非截断（end_turn 未闭合）→ 批记异常+warn 一条（分批径不再续提救回，EOF 容忍解析 GOLD 行仍入箱）。
func TestD589MalformedNonTruncatedRetries(t *testing.T) {
	st := &fakeStore{}
	var calls atomic.Int32
	run := func(_ context.Context, _ *analyst.Client, _ analyst.ToolExecutor, _ []analyst.ToolSpec,
		_ string, msgs []analyst.Message, cb analyst.LoopCallbacks) (analyst.LoopResult, error) {
		if calls.Add(1) == 1 {
			cb.OnDelta(propDelta("GOLD", false)) // 未闭合但非截断
			return analyst.LoopResult{StopReason: "end_turn"}, nil
		}
		cb.OnDelta(propDelta("SILVER", true))
		return analyst.LoopResult{StopReason: "end_turn"}, nil
	}
	inbox := filepath.Join(t.TempDir(), "inbox.md")
	deps := Deps{Store: st, Client: &analyst.Client{}, Run: run, InboxPath: inbox}
	if err := llmDig(context.Background(), deps, d530Sum("GOLD", "SILVER"), time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("2 品种单批 calls=%d", calls.Load())
	}
	data, _ := os.ReadFile(inbox)
	if !strings.Contains(string(data), "GOLD→CGOLD") {
		t.Fatalf("EOF 容忍解析 GOLD 行应入箱:\n%s", data)
	}
	var warns int
	for _, a := range st.alerts {
		if strings.HasPrefix(a, "warn:") {
			warns++
		}
	}
	if warns != 1 {
		t.Fatalf("非截断 malformed 应 warn 一条: %v", st.alerts)
	}
}

// 坑2 B2：批 malformed（非截断）→ warn 恰好一条且文案=格式异常原文。
func TestD589DoubleMalformedWarnsOnce(t *testing.T) {
	st := &fakeStore{}
	var calls atomic.Int32
	run := func(_ context.Context, _ *analyst.Client, _ analyst.ToolExecutor, _ []analyst.ToolSpec,
		_ string, _ []analyst.Message, cb analyst.LoopCallbacks) (analyst.LoopResult, error) {
		calls.Add(1)
		cb.OnDelta(propDelta("GOLD", false))
		return analyst.LoopResult{StopReason: "end_turn"}, nil
	}
	if err := llmDig(context.Background(), d530Deps(t, st, run), d530Sum("GOLD", "SILVER"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("2 品种单批 calls=%d", calls.Load())
	}
	var warns int
	for _, a := range st.alerts {
		if strings.HasPrefix(a, "warn:") && strings.Contains(a, "提案 AI 输出格式异常（已按截断内容容忍解析，提案集可能不完整）") {
			warns++
		}
		if strings.HasPrefix(a, "critical:") {
			t.Fatalf("非截断 malformed 不应 critical: %v", st.alerts)
		}
	}
	if warns != 1 {
		t.Fatalf("两轮耗尽应 warn 恰好一条, got %d: %v", warns, st.alerts)
	}
}

// 返修① 多批对抗锚：12 品种→2 批（10+2），批 1 max_tokens+malformed（仅
// GOLD 行容忍解析）+批 2 干净续跑——断言 calls=2+两批提案合并入箱+
// error 恰一条（混合序不降级=anyTruncMalformed 累计语义钉死）。
func TestSymdigBatchAdversarial(t *testing.T) {
	st := &fakeStore{}
	var calls atomic.Int32
	syms := []string{"GOLD", "SILVER", "A", "B", "C", "D", "E", "F", "G", "H", "I", "J"}
	run := func(_ context.Context, _ *analyst.Client, _ analyst.ToolExecutor, _ []analyst.ToolSpec,
		_ string, msgs []analyst.Message, cb analyst.LoopCallbacks) (analyst.LoopResult, error) {
		n := calls.Add(1)
		if n == 1 {
			cb.OnDelta(propDelta("GOLD", false)) // 批1：max_tokens+malformed，GOLD 行容忍解析
			return analyst.LoopResult{StopReason: "max_tokens"}, nil
		}
		cb.OnDelta(propDelta("SILVER", true)) // 批2：干净
		return analyst.LoopResult{StopReason: "end_turn"}, nil
	}
	inbox := filepath.Join(t.TempDir(), "inbox.md")
	deps := Deps{Store: st, Client: &analyst.Client{}, Run: run, InboxPath: inbox}
	if err := llmDig(context.Background(), deps, d530Sum(syms...), time.Now()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("12 品种应两批 calls=%d", calls.Load())
	}
	data, _ := os.ReadFile(inbox)
	for _, want := range []string{"GOLD→CGOLD", "SILVER→CSILVER"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("两批提案应合并且含 %s:\n%s", want, data)
		}
	}
	var errs, warns int
	for _, a := range st.alerts {
		if strings.HasPrefix(a, "critical:") {
			errs++
		}
		if strings.HasPrefix(a, "warn:") {
			warns++
		}
	}
	if errs != 1 || warns != 0 {
		t.Fatalf("混合序应 critical 恰一条零 warn: %v", st.alerts)
	}
}
