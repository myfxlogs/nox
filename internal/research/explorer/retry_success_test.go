// d143_test.go D-143 锚：parse fail → 同 prompt 补救一轮（上限两轮，Rate.Allow 每轮过闸）；
// 分型告警 no-fence/unmarshal/max_tokens（截断 error 级其余 warn）。
package explorer

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"arb/internal/platform/analyst"
)

// seqRun 按序回放 LLM 输出（补救轮用）；calls 计数断言轮数。
func seqRun(outs []string, stops []string, calls *int) Runner {
	return func(_ context.Context, _ *analyst.Client, _ analyst.ToolExecutor, _ []analyst.ToolSpec,
		_ string, _ []analyst.Message, cb analyst.LoopCallbacks) (analyst.LoopResult, error) {
		i := *calls
		(*calls)++
		cb.OnDelta(outs[i])
		stop := ""
		if i < len(stops) {
			stop = stops[i]
		}
		return analyst.LoopResult{Grounded: true, StopReason: stop}, nil
	}
}

func mkD143Deps(fs *fakeStore, run Runner, rate *analyst.RateLimiter) Deps {
	dir := mustTempDir()
	d := Deps{Store: fs, Client: analyst.NewClient("http://x", "k", "m"), Run: run,
		Now:       func() time.Time { return time.Date(2026, 10, 9, 6, 47, 0, 0, BJTime) },
		InboxPath: dir + "/inbox.md", ResearchDir: dir + "/res"}
	if rate != nil {
		d.Rate = rate
	}
	return d
}

func mustTempDir() string {
	dir, err := os.MkdirTemp("", "d143")
	if err != nil {
		panic(err)
	}
	return dir
}

const badJSONBatch = "```explorer-proposals\n{not json}\n```\n" // fenced 有、JSON 坏→unmarshal

// TestD143_RetrySuccess 首轮回坏 JSON→次轮回合法批→正常落库（补救轮生效）。
func TestD143_RetrySuccess(t *testing.T) {
	fs := newFakeStore()
	calls := 0
	deps := mkD143Deps(fs, seqRun([]string{badJSONBatch, goodBatch}, nil, &calls), nil)
	res, err := RunDaily(context.Background(), deps)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if calls != 2 {
		t.Fatalf("want 2 rounds（首轮坏→补救轮）, got %d", calls)
	}
	if res.Proposals != 1 || len(fs.proposals) != 1 {
		t.Fatalf("补救轮成功后应正常落库 1 行, proposals=%d rows=%d", res.Proposals, len(fs.proposals))
	}
	for _, a := range fs.alertRows {
		if strings.Contains(a, "解析失败") {
			t.Fatalf("补救成功不应落解析失败告警: %v", a)
		}
	}
}

// TestD143_TwoRoundsFailTyped 两轮全坏→分型告警（unmarshal warn 级）+零落库。
func TestD143_TwoRoundsFailTyped(t *testing.T) {
	fs := newFakeStore()
	calls := 0
	deps := mkD143Deps(fs, seqRun([]string{badJSONBatch, badJSONBatch}, nil, &calls), nil)
	res, err := RunDaily(context.Background(), deps)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if calls != 2 || res.Proposals != 0 || len(fs.proposals) != 0 {
		t.Fatalf("calls=%d proposals=%d rows=%d", calls, res.Proposals, len(fs.proposals))
	}
	if !hasWarn(fs.alertRows, "unmarshal") || !hasWarn(fs.alertRows, "解析失败") {
		t.Fatalf("unmarshal 分型告警缺: %v", fs.alertRows)
	}
}

// TestD143_MaxTokensError stop=max_tokens → 告警 error 级分型。
func TestD143_MaxTokensError(t *testing.T) {
	fs := newFakeStore()
	calls := 0
	deps := mkD143Deps(fs, seqRun(
		[]string{"无 fenced 块的输出", "仍无块"},
		[]string{"max_tokens", "max_tokens"}, &calls), nil)
	if _, err := RunDaily(context.Background(), deps); err != nil {
		t.Fatalf("run: %v", err)
	}
	found := false
	for _, a := range fs.alertRows {
		if strings.HasPrefix(a, "error") && strings.Contains(a, "max_tokens") {
			found = true
		}
	}
	if !found {
		t.Fatalf("max_tokens error 级告警缺: %v", fs.alertRows)
	}
}

// TestD143_NoFenceWarn 两轮均无块且非 max_tokens → warn 级 no-fence 分型。
func TestD143_NoFenceWarn(t *testing.T) {
	fs := newFakeStore()
	calls := 0
	deps := mkD143Deps(fs, seqRun([]string{"无块一", "无块二"}, nil, &calls), nil)
	if _, err := RunDaily(context.Background(), deps); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !hasWarn(fs.alertRows, "no-fence") {
		t.Fatalf("no-fence 分型告警缺: %v", fs.alertRows)
	}
	for _, a := range fs.alertRows {
		if strings.HasPrefix(a, "error") {
			t.Fatalf("非截断不应 error 级: %v", a)
		}
	}
}

// TestD143_RateLimitNoRetry 首轮解析失败但限流过闸拒→不调第二轮（Run 仅 1 次）。
func TestD143_RateLimitNoRetry(t *testing.T) {
	fs := newFakeStore()
	calls := 0
	deps := mkD143Deps(fs, seqRun([]string{badJSONBatch, goodBatch}, nil, &calls),
		analyst.NewRateLimiter(1))
	if _, err := RunDaily(context.Background(), deps); err != nil {
		t.Fatalf("run: %v", err)
	}
	if calls != 1 {
		t.Fatalf("限流拒后不得调第二轮, calls=%d", calls)
	}
	if !hasWarn(fs.alertRows, "解析失败") {
		t.Fatalf("解析失败告警缺: %v", fs.alertRows)
	}
}
