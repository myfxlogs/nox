// call_log.go —— D-246 v3 §4.4：fin_llm_call 调用账本落行面。
// 每次真实 HTTP 调用（loop 内每个 stream round）落一行；token 取上游 usage
// 事件原值（无事件=0 诚实不虚报）；usd_est 费率未配=0。
package analyst

import (
	"context"
	"log"
	"time"

	"nox/internal/store/pgstore"
)

// CallRecord 单次 HTTP 调用记录（loop 每轮一条）。
type CallRecord struct {
	Model     string
	InTokens  int
	OutTokens int
	ElapsedMs int
	Err       string // 空=ok
}

// LLMLogger 消费面装配：client.CallLog=pgstore 落行适配器（role=消费面名）。
// 落行用 detached ctx（调用点 ctx 在流结束/失败路径可能已 cancel）；
// 落库失败只记 log 降级——账本断点不毁业务调用。
func LLMLogger(st *pgstore.Store, role string) func(context.Context, CallRecord) {
	return func(_ context.Context, r CallRecord) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := st.InsertLLMCall(ctx, &pgstore.LLMCall{
			Role: role, Model: r.Model,
			PromptTokens: r.InTokens, CompletionTokens: r.OutTokens,
			ElapsedMs: r.ElapsedMs, OK: r.Err == "", Err: r.Err,
		}); err != nil {
			log.Printf("[analyst] llm_call ledger %s: %v", role, err)
		}
	}
}
