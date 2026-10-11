// D-284 S1：飞书自然语言查询 handler。
// 接收飞书消息 → LLM 意图识别 + tool calling → 模板回复 → SendText 异步发送。
// 4 专用工具白名单硬编码（参数零，无用户输入注入风险）。
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"arb/internal/analyst"
	"arb/internal/safego"
	"arb/internal/store/pgstore"
)

// feishuQuerySystemPrompt 飞书查询专用系统提示词（不复用 analyst.SystemPrompt——后者面向前端分析员）。
const feishuQuerySystemPrompt = `你是 ARB 套利系统的飞书查询助手。业主通过飞书发自然语言问题，你调用工具查数据库，用中文回复数值。

规则：
1. 只用工具返回的数字，不编造。
2. 工具查不到→回复"无数据"。
3. 回复简洁：直接给数值+单位，不展开分析。
4. 只读查询，不执行任何写操作。

可用工具：
- query_trx_price：查 TRX 当前价格（CMC 平台 X-AFQUL 合约中间价）
- query_trx_account：查 TRX 账户（余额/质押/待领取奖励/能量/带宽/票数）
- query_cmc_position：查 CMC 空单持仓（数量/开仓价/浮盈/强平价/距强平%）
- query_today_income：查今日收益（能量租金+投票奖励+隔夜息，USD）
- query_macro_events：查未来 N 日宏观经济事件（财历：非农/CPI/议息等）
- query_cot_latest：查 CFTC COT 最新持仓（按品种：日元/黄金/标普等）`

// AISettingsReader 读 ai_settings 表（*pgstore.Store 实现）。
type AISettingsReader interface {
	GetAiSettings(ctx context.Context) (*pgstore.AiSettings, error)
}

// FeishuQueryHandler 飞书自然语言查询处理器（实现 ReplyHandler）。
type FeishuQueryHandler struct {
	store        FeishuQueryStore
	aiSettings   AISettingsReader
	feishu       *FeishuClient
	envBaseURL   string
	envModel     string
	envAPIKey    string
	rateLim      *analyst.RateLimiter
	now          func() time.Time
	queryTimeout time.Duration // 默认 25s，测试可覆盖
	// probeDecide D-176②：探针提案批准/拒绝回调（nil=未装配→口令回「未接线」）。
	// 两表面（飞书口令/前端按钮）同路由同一 RPC 语义。
	probeDecide ProbeDecider
	// dciGo D-191②：DCI 申购执行回调（nil=未装配→口令回「未接线」）。
	dciGo DCIGoRunner
}

// NewFeishuQueryHandler 创建飞书查询处理器。
func NewFeishuQueryHandler(store FeishuQueryStore, aiSettings AISettingsReader, feishu *FeishuClient, envBaseURL, envModel, envAPIKey string, rateLim *analyst.RateLimiter) *FeishuQueryHandler {
	return &FeishuQueryHandler{
		store:        store,
		aiSettings:   aiSettings,
		feishu:       feishu,
		envBaseURL:   envBaseURL,
		envModel:     envModel,
		envAPIKey:    envAPIKey,
		rateLim:      rateLim,
		now:          time.Now,
		queryTimeout: 25 * time.Second,
	}
}

// ProbeDecider D-176②：探针提案裁定局——装配侧注入 dashboard.Service 的
// ApproveProbe/RejectProbe 等价物（notify 不 import dashboard/gen 防环）。
type ProbeDecider interface {
	ApproveProbe(ctx context.Context, proposalID int64, decisionRef string) (status, message string, positionID int64, err error)
	RejectProbe(ctx context.Context, proposalID int64, reason string) (status, message string, err error)
}

// SetProbeDecider 装配「批准#N/拒绝#N」口令后端（webhook 装配方晚绑定）。
func (h *FeishuQueryHandler) SetProbeDecider(d ProbeDecider) { h.probeDecide = d }

// HandleReply 处理飞书自然语言查询。异步调 LLM，立即返回 ""（webhook 不阻塞 ACK）。
func (h *FeishuQueryHandler) HandleReply(ctx context.Context, ev ReplyEvent) (string, error) {
	boundChatID, boundOpenID, err := h.store.FeishuTarget(ctx)
	if err != nil {
		return "系统错误：无法读取推送目标", fmt.Errorf("feishu target: %w", err)
	}
	if boundChatID == "" {
		return "推送目标未绑定", nil
	}
	if ev.ChatID != boundChatID {
		return "非授权 chat", nil
	}
	if ev.SenderOpenID != boundOpenID {
		return "非授权发送者", nil
	}
	if ev.MessageType != "text" {
		return "仅支持文本查询", nil
	}
	text, err := extractText(ev.Content)
	if err != nil {
		return "无法解析消息内容", nil
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "请输入查询内容", nil
	}
	// D-168 ④：ack 裸词在 LLM 前拦截——「收到/知道了/ack」。
	// 带 parent_id=引用回复 → 只 ack 该集；裸发 → ack 全部 open episode。
	// alerts.acked 同步清铃铛；非 ack 词走原 LLM 径不动。
	if isAckWord(text) {
		return h.handleAck(ctx, ev)
	}
	// D-176②：探针审批口令——「批准#N [D-xxx]」「拒绝#N [理由]」在 LLM 前拦截，
	// 路由与前端按钮同一 RPC 语义+幂等应答。
	if cmd, ok := parseProbeCmd(text); ok {
		return h.handleProbeCmd(ctx, cmd)
	}
	// D-191②：执行口令「go」——业主一句触发 DCI 申购（幂等 request_id 防重）。
	if gid, gdec, ok := parseGoCmd(text); ok {
		return h.handleGoCmd(ctx, gid, gdec)
	}
	if h.rateLim != nil && !h.rateLim.Allow() {
		return "已达每小时查询上限，请稍后重试", nil
	}
	chatID := ev.ChatID
	safego.GoLoop("feishu_query:handle", func() {
		queryCtx, cancel := context.WithTimeout(context.Background(), h.queryTimeout)
		defer cancel()
		client, err := h.resolveClient(queryCtx)
		if err != nil {
			h.feishu.SendText(context.Background(), chatID, "AI 未配置："+err.Error())
			return
		}
		tools := &feishuQueryTools{store: h.store}
		msgs := []analyst.Message{analyst.TextContent("user", text)}
		var replyText strings.Builder
		_, err = analyst.RunWithSpecs(queryCtx, client, tools, feishuQueryToolSpecs(), feishuQuerySystemPrompt, msgs, analyst.LoopCallbacks{
			OnDelta: func(s string) { replyText.WriteString(s) },
		})
		if err != nil {
			msg := "查询失败，请稍后重试"
			if errors.Is(err, context.DeadlineExceeded) {
				msg = "查询超时，请稍后重试"
			}
			h.feishu.SendText(context.Background(), chatID, msg)
			return
		}
		reply := strings.TrimSpace(replyText.String())
		if reply == "" {
			reply = "无数据"
		}
		h.feishu.SendText(context.Background(), chatID, reply)
	})
	return "", nil
}

// resolveClient 读 ai_settings（DB 优先→env 回落）构造临时 analyst.Client。
// base_url 语义：根 URL，不含 /v1/messages（analyst.Client 内部自动拼接 /v1/messages）。
func (h *FeishuQueryHandler) resolveClient(ctx context.Context) (*analyst.Client, error) {
	baseURL := h.envBaseURL
	model := h.envModel
	apiKey := h.envAPIKey
	if h.aiSettings != nil {
		if cfg, err := h.aiSettings.GetAiSettings(ctx); err == nil {
			if !cfg.Enabled {
				return nil, fmt.Errorf("AI 已禁用")
			}
			if cfg.BaseURL != "" {
				baseURL = cfg.BaseURL
			}
			if cfg.Model != "" {
				model = cfg.Model
			}
			if cfg.APIKey != "" {
				apiKey = cfg.APIKey
			}
		}
	}
	if apiKey == "" {
		return nil, fmt.Errorf("AI 未配置（api_key 为空）")
	}
	return analyst.NewClient(baseURL, apiKey, model), nil
}

// --- 4 专用工具 ---

const trxInstrument = "X-AFQUL"

func feishuQueryToolSpecs() []analyst.ToolSpec {
	empty := json.RawMessage(`{"type":"object","properties":{}}`)
	macroDays := json.RawMessage(`{"type":"object","properties":{"days_ahead":{"type":"integer","description":"向前看 N 天，缺省 7"}}}`)
	return []analyst.ToolSpec{
		{Name: "query_trx_price", Description: "查 TRX 当前价格（CMC 平台 X-AFQUL 合约中间价）", InputSchema: empty},
		{Name: "query_trx_account", Description: "查 TRX 账户（余额/质押/待领取奖励/能量/带宽/票数）", InputSchema: empty},
		{Name: "query_cmc_position", Description: "查 CMC 空单持仓（数量/开仓价/浮盈/强平价/距强平%）", InputSchema: empty},
		{Name: "query_today_income", Description: "查今日收益（能量租金+投票奖励+隔夜息，USD）", InputSchema: empty},
		{Name: "query_macro_events", Description: "查未来 N 日宏观经济事件（财历：非农/CPI/议息等）", InputSchema: macroDays},
	}
}

type feishuQueryTools struct {
	store FeishuQueryStore
}

func (t *feishuQueryTools) Names() []string {
	return []string{"query_trx_price", "query_trx_account", "query_cmc_position", "query_today_income",
		"query_macro_events"}
}

func (t *feishuQueryTools) Execute(ctx context.Context, call analyst.ToolCall) (analyst.ToolResult, []analyst.Citation, error) {
	switch call.Name {
	case "query_trx_price":
		return t.execTrxPrice(ctx, call)
	case "query_trx_account":
		return t.execTrxAccount(ctx, call)
	case "query_cmc_position":
		return t.execCmcPosition(ctx, call)
	case "query_today_income":
		return t.execTodayIncome(ctx, call)
	case "query_macro_events":
		return t.execMacroEvents(ctx, call)
	default:
		return analyst.ToolResult{ToolUseID: call.ID, Content: "未知工具: " + call.Name, IsError: true}, nil, nil
	}
}

func (t *feishuQueryTools) execTrxPrice(ctx context.Context, call analyst.ToolCall) (analyst.ToolResult, []analyst.Citation, error) {
	price, fetchedAt, err := t.store.LastCMCPriceMid(ctx, trxInstrument)
	if err != nil {
		return analyst.ToolResult{ToolUseID: call.ID, Content: "查 TRX 价格失败: " + err.Error(), IsError: true}, nil, nil
	}
	out, _ := json.Marshal(map[string]any{"price": price, "fetched_at": fetchedAt.Format(time.RFC3339)})
	return analyst.ToolResult{ToolUseID: call.ID, Content: string(out)}, nil, nil
}

func (t *feishuQueryTools) execTrxAccount(ctx context.Context, call analyst.ToolCall) (analyst.ToolResult, []analyst.Citation, error) {
	acc, err := t.store.LastTRXAccount(ctx)
	if err != nil {
		return analyst.ToolResult{ToolUseID: call.ID, Content: "查 TRX 账户失败: " + err.Error(), IsError: true}, nil, nil
	}
	pending := acc.PendingRewardTRX
	out, _ := json.Marshal(map[string]any{
		"balance_trx":        acc.BalanceTRX,
		"allowance_trx":      acc.AllowanceTRX,
		"pending_reward_trx": pending,
		"tron_power_trx":     acc.TronPowerTRX,
		"votes_total":        acc.VotesTotal,
		"energy_delegated":   acc.EnergyDelegated,
		"bandwidth_staked":   acc.BandwidthStaked,
		"fetched_at":         acc.FetchedAt.Format(time.RFC3339),
	})
	return analyst.ToolResult{ToolUseID: call.ID, Content: string(out)}, nil, nil
}

func (t *feishuQueryTools) execCmcPosition(ctx context.Context, call analyst.ToolCall) (analyst.ToolResult, []analyst.Citation, error) {
	pos, err := t.store.LastCMCPosition(ctx, trxInstrument)
	if err != nil {
		return analyst.ToolResult{ToolUseID: call.ID, Content: "查 CMC 持仓失败: " + err.Error(), IsError: true}, nil, nil
	}
	sum, _ := t.store.LastCMCAccountSummary(ctx)
	out, _ := json.Marshal(map[string]any{
		"instrument_id":            pos.InstrumentID,
		"direction":                pos.Direction,
		"quantity":                 pos.Quantity,
		"entry_price":              pos.EntryPrice,
		"unrealised_pnl":           pos.UnrealisedPnL,
		"closeout_price":           sum.CloseoutPrice,
		"distance_to_closeout_pct": sum.DistanceToCloseoutPct,
		"fetched_at":               pos.FetchedAt.Format(time.RFC3339),
	})
	return analyst.ToolResult{ToolUseID: call.ID, Content: string(out)}, nil, nil
}

func (t *feishuQueryTools) execTodayIncome(ctx context.Context, call analyst.ToolCall) (analyst.ToolResult, []analyst.Citation, error) {
	energy, voting, interest, err := t.store.SumTodayIncome(ctx)
	if err != nil {
		return analyst.ToolResult{ToolUseID: call.ID, Content: "查今日收益失败: " + err.Error(), IsError: true}, nil, nil
	}
	out, _ := json.Marshal(map[string]any{
		"energy_trx":   energy,
		"voting_trx":   voting,
		"interest_usd": interest,
	})
	return analyst.ToolResult{ToolUseID: call.ID, Content: string(out)}, nil, nil
}

// extractText 从飞书 content JSON 提取 text 字段。
// D-139：原 repo_reply.go 定义随 repo 面摘除，挪此（本文件唯一在用方）。
func extractText(content string) (string, error) {
	if content == "" {
		return "", fmt.Errorf("empty content")
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(content), &m); err != nil {
		return "", err
	}
	return m["text"], nil
}
