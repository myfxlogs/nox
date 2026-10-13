// Package hearing —— 策略淘汰多空听证（D-249①，TradingAgents 对辩式）。
// 裁定击杀前跑两路 LLM 对辩（多方辩持有/空方辩击杀，各 ≤400 字，输入=确定性
// 统计面快照+判据理由）→ 对辩文本随裁定记录落 fin_strategy_hearing 供复核。
// 禁线：听证仅观察面——淘汰判定权=判据表确定性函数，LLM 不翻裁定只留证；
// 零入口于执行/风控路径；事件触发非日频（~2 调用/次）；AI 未配置=听证缺如
// 正常淘汰（fail-open——行落 status=skipped_no_ai/failed 留痕不阻塞）。
package hearing

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"
	"unicode/utf8"

	"nox/internal/platform/analyst"
	"nox/internal/store/pgstore"
)

// Stats 听证输入=裁定所依确定性统计面（对辩双稿共用同一输入，零 LLM 重算）。
type Stats struct {
	TotalTrades int     `json:"total_trades"`
	WinRate     float64 `json:"win_rate"`
	AvgPnl      float64 `json:"avg_pnl"`
	MedianPnl   float64 `json:"median_pnl"`
}

// Store 听证行写面（*pgstore.Store 原生满足）。
type Store interface {
	InsertElimHearing(ctx context.Context, h pgstore.ElimHearing) error
}

// Runner RunWithSpecs 等价 seam（同 deptreport，测试注入 fake 断言 prompt）。
type Runner func(ctx context.Context, c *analyst.Client, systemPrompt string,
	msgs []analyst.Message, cb analyst.LoopCallbacks) (analyst.LoopResult, error)

// Deps 听证依赖（cmd/core 装配；测试注入）。
type Deps struct {
	Store         Store
	ResolveClient func(ctx context.Context) (*analyst.Client, error)
	Run           Runner // 空=RunWithSpecs 包装（90s 预算+思考关+1024 上限）
	Now           func() time.Time
}

func (d *Deps) fill() {
	if d.Run == nil {
		d.Run = func(ctx context.Context, c *analyst.Client, systemPrompt string,
			msgs []analyst.Message, cb analyst.LoopCallbacks) (analyst.LoopResult, error) {
			return analyst.RunWithSpecsMaxTokensThinking(ctx, c, nil, nil, systemPrompt, msgs, cb, 1024, json.RawMessage(`{"type":"disabled"}`))
		}
	}
	if d.Now == nil {
		d.Now = time.Now
	}
}

const bullPrompt = `你是策略淘汰听证的多方辩护人。输入=该策略确定性统计面（JSON）+判据命中理由。
输出=辩「应继续观察、不应击杀」的最强论据（≤400 字纯文本，无 markdown）。
红线：数字只能引用输入段原值，禁止编造；只摆论据不下操作指令。`

const bearPrompt = `你是策略淘汰听证的空方控诉人。输入=该策略确定性统计面（JSON）+判据命中理由。
输出=辩「应立即淘汰」的最强论据（≤400 字纯文本，无 markdown）。
红线：数字只能引用输入段原值，禁止编造；只摆论据不下操作指令。`

// Debate 多空听证——裁定击杀前同步调用（ctx 内封顶 200s）。
// 两路各重试一次（同 deptreport 面）；AI 缺配/败→行照落 status 标痕。
func (d *Deps) Debate(ctx context.Context, trigger string, fam []string, st Stats, reason string) {
	d.fill()
	ctx, cancel := context.WithTimeout(ctx, 200*time.Second)
	defer cancel()
	statsJSON, _ := json.Marshal(st)
	h := pgstore.ElimHearing{Strategy: trigger, Family: fam, Stats: statsJSON, Reason: reason, Status: "failed"}
	input := fmt.Sprintf("策略 %s（族 %v）\n统计面：%s\n判据命中：%s\n持有期统计：未采",
		trigger, fam, string(statsJSON), reason)
	if client, err := d.ResolveClient(ctx); err == nil && client != nil {
		h.BullText, _ = d.side(ctx, client, bullPrompt, input)
		h.BearText, _ = d.side(ctx, client, bearPrompt, input)
		h.Status = "debated"
	} else {
		h.Status = "skipped_no_ai"
	}
	if err := d.Store.InsertElimHearing(ctx, h); err != nil {
		// 落库败只 log——听证观察面不阻塞淘汰主径。
		log.Printf("[hearing] insert: %v", err)
	}
}

// side 单路对辩调用（重试一次——上游安全闸概率判分同文重发可过）。
func (d *Deps) side(ctx context.Context, client *analyst.Client, system, input string) (string, error) {
	msgs := []analyst.Message{analyst.TextContent("user", input)}
	var out string
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
		out = ""
		_, err = d.Run(ctx, client, system, msgs, analyst.LoopCallbacks{
			OnDelta: func(t string) { out += t },
		})
		if err == nil {
			return clampRunes(out, 400), nil
		}
	}
	return "", err
}

// clampRunes 截 400 字（票面字数上限硬闸——LLM 不守约束由 Go 收口）。
func clampRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}
