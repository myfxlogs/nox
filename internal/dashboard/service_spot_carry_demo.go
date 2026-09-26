// service_spot_carry_demo.go D-350 阶段③：spot-carry demo 手动实测（无 auto——发现≠授权，
// auto 化待经验数据另立 D#）。双腿原子：先现货后永续，任一失败即反腿市价回滚已成交腿。
// 现货腿端点=perp demo 配置（exchange_configs id=25）base_url 换 /api 段（D-330 ⑥同款坑预检）；
// demo spot 可用性首验在部署后（A8-③），不可用则降级「real 纸面强化」并在 LOG 留痕（不硬造模拟）。
package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"arb/internal/exec"
	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/httpx"
	"arb/internal/store/pgstore"

	"connectrpc.com/connect"
)

// pickSimulatedConfig D-350R1 S1（缺陷 1 P0 实盘混选）：配置选择必须命中 simulated=true
// （demo）——ListExchangeConfigs 按 created_at 序，venue+venueType 只比二者会命中主网行
// （id=20 主网 perp 先于 id=25 demo），以实盘 key 打主网真仓=fail-dangerous。
// 无命中返回 nil（RPC 侧报 failed_precondition 明示需 demo 配置）。
func pickSimulatedConfig(cfgs []pgstore.ExchangeConfig, venue, venueType string) *pgstore.ExchangeConfig {
	for i := range cfgs {
		if cfgs[i].Venue == venue && cfgs[i].VenueType == venueType && cfgs[i].Simulated {
			return &cfgs[i]
		}
	}
	return nil
}

// deriveSpotBaseURL demo 现货端点构造：perp demo base（…/demofapi/fapi）尾部 /fapi → /api
// （T3 unit 锚）；非 /fapi 尾（如 id=19 主网 …/api）原样返回。
func deriveSpotBaseURL(perpBase string) string {
	if strings.HasSuffix(perpBase, "/fapi") {
		return strings.TrimSuffix(perpBase, "/fapi") + "/api"
	}
	return perpBase
}

// validateSpotCarryDemoParams RPC 参数校验（T3 unit 锚；store 访问前执行）。
func validateSpotCarryDemoParams(venue, symbol, side string, qtyUSD float64) error {
	if venue != "binance" {
		return fmt.Errorf("venue %q 不支持（当前仅 binance demo）", venue)
	}
	if strings.TrimSpace(symbol) == "" {
		return fmt.Errorf("symbol 必填")
	}
	if side != "short_perp" && side != "long_perp" {
		return fmt.Errorf("side %q 非法（short_perp/long_perp）", side)
	}
	if qtyUSD <= 0 {
		return fmt.Errorf("qty_usd 必须 >0")
	}
	return nil
}

// errDemoRowExists 目标 symbol 已有 demo 实测行（promote 报 ErrPaperPromoteNoActive）——
// RPC 侧映射 connect CodeAlreadyExists（语义沿 R1）。
var errDemoRowExists = errors.New("spot-carry demo: demo row already exists")

// demoPaperStore demo 在册窄接口（三方法，*pgstore.Store 原生满足；单测 fake 注入走三分支）。
type demoPaperStore interface {
	InsertPaperSpotCarry(ctx context.Context, r *pgstore.PaperSpotCarry) error
	PromotePaperToDemo(ctx context.Context, venueA, venueB, symbol string, r *pgstore.PaperSpotCarry) error
	MarkPaperSpotCarryDemo(ctx context.Context, id int64, note string) error
}

// registerSpotCarryDemoRow D-350R2 S1（缺陷①）demo 在册收口：三分支终态必 demo 行。
// fresh（无 active/demo 行）：Insert 成功后立即 Mark——Insert 硬编码 status='active'，不 Mark 则
// 行以 active 纸面行存在（ListSpotCarryDemo 漏此仓+纸面自动退出误作用+实际 qty 污染纸面账；
// 可达场景：纸面行已 normalized/age_7d 退出后再 demo，或目标 symbol 在纸面闸外）。
// active 行在：Insert 撞 ErrPaperExists → Promote（旧行 promoted_demo 封账+新 demo 行，R1 S2）。
// demo 行在：Promote 报 ErrPaperPromoteNoActive → errDemoRowExists。
func registerSpotCarryDemoRow(ctx context.Context, st demoPaperStore, venue, symbol, note string, pr *pgstore.PaperSpotCarry) error {
	err := st.InsertPaperSpotCarry(ctx, pr)
	switch {
	case err == nil:
		return st.MarkPaperSpotCarryDemo(ctx, pr.ID, note)
	case err != pgstore.ErrPaperExists:
		return fmt.Errorf("insert paper row: %w", err)
	}
	perr := st.PromotePaperToDemo(ctx, venue, "", symbol, pr)
	switch {
	case perr == nil:
		return nil
	case perr == pgstore.ErrPaperPromoteNoActive:
		return errDemoRowExists
	default:
		return fmt.Errorf("promote paper row: %w", perr)
	}
}

// ExecuteSpotCarryDemo 手动双腿闭环：现货腿（demo /api 段）+永续腿（既有 demo-fapi 路径）。
func (s *Service) ExecuteSpotCarryDemo(ctx context.Context, req *connect.Request[v1.ExecuteSpotCarryDemoRequest]) (*connect.Response[v1.ExecuteSpotCarryDemoResponse], error) {
	in := req.Msg
	if err := validateSpotCarryDemoParams(in.Venue, in.Symbol, in.Side, in.QtyUsd); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	cfgs, err := s.store.ListExchangeConfigs(ctx)
	if err != nil {
		return nil, err
	}
	perpCfg := pickSimulatedConfig(cfgs, in.Venue, "perp")
	if perpCfg == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("venue %q 无 demo（simulated=true）perp 配置", in.Venue))
	}
	spotBase := deriveSpotBaseURL(perpCfg.BaseURL)
	// 降级路径：demo spot 可用性首验（部署后 A8-③）；不可用 → 本 RPC 报 failed_precondition
	//（纸面轨不受影响，LOG 留痕降级）。
	spotPrice, err := publicGETNumber(ctx, spotBase+"/v3/ticker/price?symbol="+in.Symbol, "price")
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("demo spot 不可用（降级=real 纸面强化）：ticker %v", err))
	}
	perpPrice, err := publicGETNumber(ctx, perpCfg.BaseURL+"/v1/premiumIndex?symbol="+in.Symbol, "markPrice")
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("demo perp mark 不可用：%v", err))
	}
	// 双腿执行器（同 demo key；spot 走换段 base）
	spotEx := exec.NewCEXExecutor(in.Venue, "spot", perpCfg.APIKey, perpCfg.APISecret, "", spotBase, false)
	perpEx := exec.NewCEXExecutor(in.Venue, "perp", perpCfg.APIKey, perpCfg.APISecret, "", perpCfg.BaseURL, false)

	spotSide, perpSide := "BUY", "SELL" // short_perp：买现货+空永续
	if in.Side == "long_perp" {
		spotSide, perpSide = "SELL", "BUY"
	}
	resp := &v1.ExecuteSpotCarryDemoResponse{SpotPrice: spotPrice, PerpPrice: perpPrice}
	spotLeg := &v1.SpotCarryDemoLeg{Side: spotSide, Market: "spot"}
	perpLeg := &v1.SpotCarryDemoLeg{Side: perpSide, Market: "perp"}
	resp.Legs = append(resp.Legs, spotLeg, perpLeg)

	rollback := func(ex *exec.CEXExecutor, leg *v1.SpotCarryDemoLeg) {
		rbSide := "SELL"
		if leg.Side == "SELL" {
			rbSide = "BUY"
		}
		if _, err := ex.PlaceOrder(ctx, exec.Order{Symbol: in.Symbol, Side: rbSide, Qty: leg.Qty, Venue: in.Venue}); err != nil {
			s.store.InsertAlert(ctx, nil, "critical",
				fmt.Sprintf("[spot-carry demo] 回滚失败 %s %s qty=%v: %v（人工处置）", in.Symbol, rbSide, leg.Qty, err))
		} else {
			leg.RolledBack = true
		}
	}
	// 先现货
	spotFill, err := spotEx.PlaceOrder(ctx, exec.Order{
		Symbol: in.Symbol, Side: spotSide, Qty: in.QtyUsd / spotPrice, Venue: in.Venue,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("现货腿失败（未开永续腿）：%v", err))
	}
	spotLeg.OrderId, spotLeg.Qty = spotFill.OrderID, spotFill.Qty
	// 后永续；失败 → 反腿回滚现货
	perpFill, err := perpEx.PlaceOrder(ctx, exec.Order{
		Symbol: in.Symbol, Side: perpSide, Qty: in.QtyUsd / perpPrice, Venue: in.Venue,
	})
	if err != nil {
		rollback(spotEx, spotLeg)
		return nil, connect.NewError(connect.CodeInternal,
			fmt.Errorf("永续腿失败，现货腿已回滚：%v", err))
	}
	perpLeg.OrderId, perpLeg.Qty = perpFill.OrderID, perpFill.Qty
	// 在册：paper_spot_carry status='demo'（同 symbol 活跃唯一索引兜底防重复开）。
	// D-350R2 S1（缺陷①）：在册收口三分支终态必 demo 行——fresh（纸面行已退出/闸外 symbol）
	// Insert 后立即 Mark demo 化；active 在册 → Promote 支路（R1 S2）；demo 在册 → already_exists。
	note := fmt.Sprintf("spot=%s@%.6g perp=%s@%.6g", spotFill.OrderID, spotPrice, perpFill.OrderID, perpPrice)
	pr := &pgstore.PaperSpotCarry{
		VenueA: in.Venue, Symbol: in.Symbol, Side: in.Side,
		EntryFundingAnn: 0, QtyUSD: in.QtyUsd, Note: note,
	}
	if err := registerSpotCarryDemoRow(ctx, s.store, in.Venue, in.Symbol, note, pr); err != nil {
		if err == errDemoRowExists {
			return nil, connect.NewError(connect.CodeAlreadyExists,
				fmt.Errorf("%s 已有 demo 实测仓（无 active 纸面行可升级）", in.Symbol))
		}
		resp.Ok = true // 腿已成交：在册失败不回滚（真实仓位），critical 告警人工补录（语义沿 R1）
		s.store.InsertAlert(ctx, nil, "critical",
			fmt.Sprintf("[spot-carry demo] 双腿已成交但在册失败（%v）——人工补录 paper_spot_carry", err))
		return connect.NewResponse(resp), nil
	}
	resp.PaperId = pr.ID
	resp.Ok = true
	return connect.NewResponse(resp), nil
}

// ListSpotCarryDemo demo 实测仓清单（双腿成交态=在册即双腿已成交；当前 funding+应计复用纸面口径）。
func (s *Service) ListSpotCarryDemo(ctx context.Context, req *connect.Request[v1.ListSpotCarryDemoRequest]) (*connect.Response[v1.ListSpotCarryDemoResponse], error) {
	rows, err := s.store.ListDemoPaperSpotCarry(ctx)
	if err != nil {
		return nil, err
	}
	uni, err := s.store.ListCexUniverse(ctx)
	if err != nil {
		return nil, err
	}
	resp := &v1.ListSpotCarryDemoResponse{}
	for _, r := range rows {
		last := 0.0
		if u, ok := uni[r.VenuePair()+"|"+r.Symbol]; ok && u.FundingAnn != nil {
			last = *u.FundingAnn
		}
		resp.Rows = append(resp.Rows, &v1.SpotCarryDemoRow{
			PaperId: r.ID, Venue: r.VenuePair(), Symbol: r.Symbol, Side: r.Side,
			QtyUsd: r.QtyUSD, EntryFundingAnn: r.EntryFundingAnn, LastFundingAnn: last,
			AccruedUsd: r.AccruedUSD, EntryTs: r.EntryTs.Format(time.RFC3339), Note: r.Note,
		})
	}
	return connect.NewResponse(resp), nil
}

// publicGETNumber 公开 GET 取数值字段（ticker/price、premiumIndex；httpx 注入代理令牌）。
func publicGETNumber(ctx context.Context, url, field string) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := httpx.Client().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}
	var v map[string]any
	if err := json.Unmarshal(body, &v); err != nil {
		return 0, err
	}
	switch n := v[field].(type) {
	case string:
		var f float64
		if _, err := fmt.Sscanf(n, "%f", &f); err != nil {
			return 0, err
		}
		return f, nil
	case float64:
		return n, nil
	}
	return 0, fmt.Errorf("field %q 缺失", field)
}
