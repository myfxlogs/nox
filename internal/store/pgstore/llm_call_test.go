package pgstore

import (
	"context"
	"testing"
	"time"
)

// TestM40_LLMCall：fin_llm_call 落行+成本合计（迁移 1007 幂等已含表存在性）。
func TestM40_LLMCall(t *testing.T) {
	s := sharedTestStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM fin_llm_call`); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertLLMCall(ctx, &LLMCall{
		Role: "explorer", Model: "glm-test", PromptTokens: 100, CompletionTokens: 50,
		UsdEst: 0.0123, ElapsedMs: 800, OK: true}); err != nil {
		t.Fatalf("insert ok: %v", err)
	}
	if err := s.InsertLLMCall(ctx, &LLMCall{
		Role: "company_report", Model: "glm-test", OK: false, Err: "upstream 1301"}); err != nil {
		t.Fatalf("insert fail: %v", err)
	}
	usd, n, err := s.LLMCallCostSince(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("calls=%d want 2", n)
	}
	if usd < 0.0122 || usd > 0.0124 {
		t.Fatalf("usd=%v want ~0.0123", usd)
	}
	// 旧窗零行。
	if _, n, err := s.LLMCallCostSince(ctx, time.Now().Add(time.Hour)); err != nil || n != 0 {
		t.Fatalf("future window n=%d err=%v", n, err)
	}
}
