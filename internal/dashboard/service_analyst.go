package dashboard

// service_analyst.go D-091 前端分析员 RPC：AskAnalyst（server streaming）+ AppendAnalystNote。
// 装配经 SetAnalyst 注入（main 构造 analyst deps 后注入，与 SetSettler 同模式）。
// 未注入（cfg.AIAPIKey 空）→ AskAnalyst 返回 failed_precondition，AppendAnalystNote 返回 unavailable。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"arb/internal/analyst"
	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/store/pgstore"

	"connectrpc.com/connect"
)

// analystDeps 分析员依赖（main 注入；nil = 未配置）。
type analystDeps struct {
	client   *analyst.Client // env 回落 client（DB 空时用）
	tools    *analyst.Tools
	rateLim  *analyst.RateLimiter
	knRoot   string // 经验库根（默认 /kn）
	notePath string // 待归纳文件相对 knRoot 路径
	// R3 返修②：env 回落值（DB 空或字段空时用）
	envBaseURL string
	envModel   string
	envAPIKey  string
}

// SetAnalyst 注入分析员依赖（main 装配后调用）。
func (s *Service) SetAnalyst(client *analyst.Client, tools *analyst.Tools, rateLim *analyst.RateLimiter, knRoot, notePath string) {
	s.analyst = &analystDeps{
		client: client, tools: tools, rateLim: rateLim, knRoot: knRoot, notePath: notePath,
		envBaseURL: client.BaseURL, envModel: client.Model, envAPIKey: client.APIKey,
	}
}

// EnrichPositionForAnalyst R2 返修②：返回 position 派生字段回调（复用 enrichOne 同源派生）。
// 闭包捕获 s（含 bpm/store），analyst.Tools.EnrichPosition 注入后 get_entity position 含派生字段。
func (s *Service) EnrichPositionForAnalyst() func(ctx context.Context, p *pgstore.Position, legs []pgstore.PositionLeg) map[string]any {
	return func(ctx context.Context, p *pgstore.Position, legs []pgstore.PositionLeg) map[string]any {
		if s.bpm == nil {
			return nil // 未接线 → 诚实缺省
		}
		pb := s.positionToProto(ctx, p)
		// D-118：analyst 路径无批量预取，传空 map（enrichOne 退化为纯结算天数，无三倍日加成）
		// D-146 S5：analyst 路径无 funding 预取，传空 map（enrichOne 无 funding 加成）
		// D-375：estimated 参考行同空（analyst 派生不消费该字段）
		enrichOne(ctx, s, pb, legs, s.bpm.Snapshot(), time.Now(), map[string]time.Weekday{}, map[string]float64{}, map[int64]float64{})
		derived := map[string]any{}
		if pb.FloatingPnlUsd != nil {
			derived["floating_pnl_usd"] = *pb.FloatingPnlUsd
		}
		if pb.SwapAccruedUsd != nil {
			derived["swap_accrued_usd"] = *pb.SwapAccruedUsd
		}
		if pb.NetPnlUsd != nil {
			derived["net_pnl_usd"] = *pb.NetPnlUsd
		}
		if pb.ExpectedSwapUsdToDate != nil {
			derived["expected_swap_usd_to_date"] = *pb.ExpectedSwapUsdToDate
		}
		if pb.AttainmentPct != nil {
			derived["attainment_pct"] = *pb.AttainmentPct
		}
		if pb.CurrentNetBps != nil {
			derived["current_net_bps"] = *pb.CurrentNetBps
		}
		return derived
	}
}

// resolveAiConfig R3 返修②：每问读库（DB 优先 → env 回落）。
// DB enabled=false → 拒绝；DB+env api_key 双双空 → failed_precondition。
// 返回临时 client（按解析出的 baseURL/model/apiKey 构造）。
func (s *Service) resolveAiConfig(ctx context.Context) (*analyst.Client, error) {
	deps := s.analyst
	if deps == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("分析员未配置"))
	}
	baseURL := deps.envBaseURL
	model := deps.envModel
	apiKey := deps.envAPIKey
	// DB 优先（每问读库，镜像 exchange_configs 模式，改完即生效免重启）
	if s.store != nil {
		if cfg, err := s.store.GetAiSettings(ctx); err == nil {
			if !cfg.Enabled {
				return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("分析员已禁用（DB enabled=false）"))
			}
			if cfg.BaseURL != "" {
				baseURL = cfg.BaseURL
			}
			if cfg.Model != "" {
				model = cfg.Model
			}
			if cfg.APIKey != "" {
				apiKey = cfg.APIKey // DB api_key 覆盖 env（仅存 DB 不打日志）
			}
		}
	}
	if apiKey == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("分析员未配置（api_key 为空）"))
	}
	return analyst.NewClient(baseURL, apiKey, model), nil
}

// AskAnalyst 流式回答：限流 → 每问读库解析配置 → 构造 messages → analyst.Run → delta/citation/follow_ups/done 流出。
func (s *Service) AskAnalyst(ctx context.Context, req *connect.Request[v1.AskAnalystRequest], stream *connect.ServerStream[v1.AskAnalystResponse]) error {
	deps := s.analyst
	if deps == nil {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("分析员未配置"))
	}
	if deps.rateLim != nil && !deps.rateLim.Allow() {
		return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("已达每小时提问上限"))
	}
	client, err := s.resolveAiConfig(ctx)
	if err != nil {
		return err
	}
	// 构造初始 messages：历史轮次 + 本轮问题（含上下文摘要注入）。
	msgs := buildAnalystMessages(req.Msg)
	// 循环回调 → proto 流。
	cb := analyst.LoopCallbacks{
		OnDelta: func(text string) {
			_ = stream.Send(&v1.AskAnalystResponse{Part: &v1.AskAnalystResponse_Delta{Delta: text}})
		},
		OnCitation: func(c analyst.Citation) {
			_ = stream.Send(&v1.AskAnalystResponse{Part: &v1.AskAnalystResponse_Citation{Citation: &v1.AnalystCitation{
				Kind: c.Kind, Ref: c.Ref, Excerpt: c.Excerpt,
			}}})
		},
		OnFollowUps: func(fu []string) {
			_ = stream.Send(&v1.AskAnalystResponse{Part: &v1.AskAnalystResponse_FollowUps{FollowUps: &v1.SuggestedFollowUps{FollowUps: fu}}})
		},
	}
	result, err := analyst.Run(ctx, client, deps.tools, msgs, cb)
	if err != nil {
		return fmt.Errorf("analyst run: %w", err)
	}
	// 末帧 done（grounded=false → 前端灰显「⚠ 无数据支撑」）。
	return stream.Send(&v1.AskAnalystResponse{Part: &v1.AskAnalystResponse_Done{Done: &v1.AnalystDone{Grounded: result.Grounded}}})
}

// GetAiSettings R3 返修②：读 DB 配置（api_key 掩码返回尾 4 位）。
func (s *Service) GetAiSettings(ctx context.Context, req *connect.Request[v1.GetAiSettingsRequest]) (*connect.Response[v1.GetAiSettingsResponse], error) {
	if s.store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("store 未配置"))
	}
	cfg, err := s.store.GetAiSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("get ai settings: %w", err)
	}
	return connect.NewResponse(&v1.GetAiSettingsResponse{
		BaseUrl:      cfg.BaseURL,
		Model:        cfg.Model,
		ApiKeyMasked: maskAPIKey(cfg.APIKey),
		Enabled:      cfg.Enabled,
	}), nil
}

// SetAiSettings R3 返修②：UPSERT DB 配置（空 api_key=不改动）。
func (s *Service) SetAiSettings(ctx context.Context, req *connect.Request[v1.SetAiSettingsRequest]) (*connect.Response[v1.SetAiSettingsResponse], error) {
	if s.store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("store 未配置"))
	}
	if err := s.store.SetAiSettings(ctx, req.Msg.BaseUrl, req.Msg.Model, req.Msg.ApiKey, req.Msg.Enabled); err != nil {
		return nil, fmt.Errorf("set ai settings: %w", err)
	}
	return connect.NewResponse(&v1.SetAiSettingsResponse{Ok: true}), nil
}

// maskAPIKey 尾 4 位掩码（如 "****a1b2"）；空串返回空。
func maskAPIKey(key string) string {
	if len(key) <= 4 {
		return key
	}
	return "****" + key[len(key)-4:]
}

// truncateStr 截断字符串到 max 个 rune（超长加…，rune 安全防 UTF-8 中文切半出 U+FFFD）。
func truncateStr(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// cleanNoteField 落盘前清洗表格单元格字段（零信任：服务端清洗，勿信前端）。
// \r/\n → 空格（防换行撕碎表格行）；| → \|（GFM 表格内标准逃逸，防竖线劈列）。
func cleanNoteField(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return s
}

// AppendAnalystNote 追加「记一下」到 docs/经验库/待归纳-分析员.md（并发安全 append）。
func (s *Service) AppendAnalystNote(ctx context.Context, req *connect.Request[v1.AppendAnalystNoteRequest]) (*connect.Response[v1.AppendAnalystNoteResponse], error) {
	deps := s.analyst
	if deps == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("分析员未配置"))
	}
	if req.Msg.Question == "" || req.Msg.Summary == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("question/summary 不能为空"))
	}
	root := deps.knRoot
	if root == "" {
		root = analyst.KnRoot
	}
	full := filepath.Join(root, deps.notePath)
	// 路径仍在 root 下（防篡改）。
	if rel, err := filepath.Rel(root, full); err != nil || strings.HasPrefix(rel, "..") {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("笔记路径非法"))
	}
	// R5 返修②：落盘格式改表格行（| 日期 | 问题前60字 | 结论前120字 | 引用数 |）
	// R1 返修③：服务端清洗（零信任）——换行→空格、|→\|；truncateStr rune 安全防 UTF-8 切半。
	block := fmt.Sprintf("| %s | %s | %s | %d |\n",
		time.Now().Format("2006-01-02 15:04"),
		truncateStr(cleanNoteField(req.Msg.Question), 60),
		truncateStr(cleanNoteField(req.Msg.Summary), 120),
		len(req.Msg.Citations))
	if err := appendNote(full, block); err != nil {
		return nil, fmt.Errorf("append note: %w", err)
	}
	return connect.NewResponse(&v1.AppendAnalystNoteResponse{Ok: true}), nil
}

// appendNote 并发安全追加（文件锁互斥，防多请求交错）。
var noteMu sync.Mutex

func appendNote(path, block string) error {
	noteMu.Lock()
	defer noteMu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(block)
	return err
}

// buildAnalystMessages 构造初始 messages：历史轮次 + 本轮问题（含上下文摘要）。
func buildAnalystMessages(req *v1.AskAnalystRequest) []analyst.Message {
	var msgs []analyst.Message
	for _, turn := range req.History {
		role := "assistant"
		if turn.User {
			role = "user"
		}
		msgs = append(msgs, analyst.TextContent(role, turn.Text))
	}
	// 本轮问题：注入页面 + 上下文实体引用摘要。
	parts := []string{req.Question}
	if req.Page != "" {
		parts = append([]string{fmt.Sprintf("[当前页面: %s]", req.Page)}, parts...)
	}
	if len(req.Context) > 0 {
		var refs []string
		for _, e := range req.Context {
			refs = append(refs, fmt.Sprintf("%s:%s", e.Type, e.Id))
		}
		parts = append([]string{fmt.Sprintf("[上下文实体: %s]", strings.Join(refs, ", "))}, parts...)
	}
	msgs = append(msgs, analyst.TextContent("user", strings.Join(parts, "\n")))
	return msgs
}
