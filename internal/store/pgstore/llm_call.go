// llm_call.go —— D-246 v3 §4.4：fin_llm_call LLM 调用账本读写面
// （每次真实调用一行；费用入 D-013 单源账本口径）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// LLMCall 单次 LLM 调用行（token 上游无 usage 事件=0 诚实不虚报；
// usd_est 费率未配=0）。
type LLMCall struct {
	Role             string
	Model            string
	PromptTokens     int
	CompletionTokens int
	UsdEst           float64
	ElapsedMs        int
	OK               bool
	Err              string
}

// InsertLLMCall 调用落行（日志级失败上抛——账本断点须可见，调用方按需吞错降级）。
func (s *Store) InsertLLMCall(ctx context.Context, c *LLMCall) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO fin_llm_call (role, model, prompt_tokens, completion_tokens, usd_est, elapsed_ms, ok, err)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		c.Role, c.Model, c.PromptTokens, c.CompletionTokens, c.UsdEst, c.ElapsedMs, c.OK, c.Err)
	return err
}

// LLMCallCostSince 窗内调用成本+次数合计（§4.3 指标⑥月成本对月净额）。
func (s *Store) LLMCallCostSince(ctx context.Context, since time.Time) (float64, int, error) {
	var usd float64
	var n int
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(usd_est),0), COUNT(*) FROM fin_llm_call
		WHERE created_at >= $1`, since).Scan(&usd, &n); err != nil {
		return 0, 0, fmt.Errorf("llm call cost: %w", err)
	}
	return usd, n, nil
}
