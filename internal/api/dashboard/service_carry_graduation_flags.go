// service_carry_graduation_flags.go D-475a S4：blocked 旗标吊销编排（单点）。
// 从 service_carry_graduation.go 拆出保 450 硬线——行数治理纯搬家，零逻辑变更。
package dashboard

import (
	"context"
	"fmt"
	"log"
	"strings"

	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/store/pgstore"
	"connectrpc.com/connect"
)

// revokeCombosForFlag D-475a S4 吊销编排（单点）：旗标 venue 双源列逐段
// DisableCombosByFlag → 停用行数>0 则引擎热重载+快照刷新+warn 告警列数；
// 受影响 venue 在管仓命中→warn 列 posID 知情（不自动平仓，blocked=禁新开非强平）。
// **契约：任何 blocked flag 写口（reject/KB 投影/S7 升级/手工后补 RPC）必须走
// 本编排——InsertFlag 保持纯写语义，吊销是可审计服务层动作。**
// 复活=人工（flag resolve 不自动复活 combo）。
func (s *Service) revokeCombosForFlag(ctx context.Context, venueA, venueB, symbol string) {
	db := s.gradDB()
	total := int64(0)
	seenVenues := map[string]bool{}
	for _, bare := range []string{venueA, venueB} { // 双源列直读（'3001157'/'okx' 各查 venue_key 前缀吊销）
		if bare == "" || seenVenues[bare] {
			continue
		}
		seenVenues[bare] = true
		n, err := db.DisableCombosByFlag(ctx, bare, symbol)
		if err != nil {
			log.Printf("[graduation] flag revoke %s/%s: %v", bare, symbol, err)
			continue
		}
		total += n
		// 在管仓知情（P1 修）：该 venue 有 OPEN/LEGGED/CLOSING 仓→warn 列 posID。
		cnt, ids, err := db.CountOpenPositionsOnVenue(ctx, bare)
		if err != nil {
			log.Printf("[graduation] positions on %s: %v", bare, err)
		} else if cnt > 0 {
			s.insertGraduationAlertLevel(ctx, "warn",
				fmt.Sprintf("%s 有 %d 个在管仓受旗标影响（持仓 #%v），需业主决策是否平仓", bare, cnt, ids))
		}
	}
	if total == 0 {
		return
	}
	if s.xassetReloader != nil {
		if err := s.xassetReloader.ReloadXAssetAllowCombos(ctx); err != nil {
			log.Printf("[graduation] flag revoke reload: %v", err)
		}
	}
	if err := s.refreshXAssetSnapshots(ctx); err != nil {
		log.Printf("[graduation] flag revoke snapshot refresh: %v", err)
	}
	s.insertGraduationAlertLevel(ctx, "warn",
		fmt.Sprintf("封杀旗标 %s×%s：吊销 %d 条白名单组合", func() string {
			if venueB == "" {
				return venueA
			}
			return venueA + "×" + venueB
		}(), symbol, total))
}

// insertGraduationAlertLevel 指定级别告警（revoke warn 与裁决 info 分用）。
func (s *Service) insertGraduationAlertLevel(ctx context.Context, level, msg string) {
	if err := s.gradDB().InsertAlert(ctx, nil, level, msg); err != nil {
		log.Printf("[graduation] alert: %v", err)
	}
}

// RejectCarryGraduation pending→rejected（不可逆；note 留业主裁决记录）。
func (s *Service) RejectCarryGraduation(ctx context.Context, req *connect.Request[v1.RejectCarryGraduationRequest]) (*connect.Response[v1.RejectCarryGraduationResponse], error) {
	resp := &v1.RejectCarryGraduationResponse{}
	g := req.Msg
	db := s.gradDB()
	grad, err := db.GetCarryGraduation(ctx, g.GraduationId)
	if err != nil {
		resp.Error = fmt.Sprintf("建议 #%d 不存在: %v", g.GraduationId, err)
		return connect.NewResponse(resp), nil
	}
	if grad.Status != "pending" {
		resp.Error = fmt.Sprintf("建议 #%d status=%s 非 pending", grad.ID, grad.Status)
		return connect.NewResponse(resp), nil
	}
	if err := db.UpdateCarryGraduationDecision(ctx, grad.ID, "rejected", nil, strings.TrimSpace(g.Note)); err != nil {
		resp.Error = fmt.Sprintf("落账失败: %v", err)
		return connect.NewResponse(resp), nil
	}
	// D-463 S4：拒绝理由回写旗标表（业主勾选时；同品种下次建议自动带旗/拦纸面）。
	if g.WriteFlag {
		level := strings.TrimSpace(g.FlagLevel)
		if level != "blocked" {
			level = "warn" // 未显式选 blocked 一律 warn（默认级保守）
		}
		reason := strings.TrimSpace(g.Note)
		if reason == "" {
			reason = "业主拒绝（无理由注记）"
		}
		if err := db.InsertFlag(ctx, &pgstore.VenueSymbolFlag{
			VenueA: grad.VenueA, VenueB: grad.VenueB, Symbol: grad.Symbol, Level: level, Reason: reason,
		}); err != nil {
			resp.Error = fmt.Sprintf("拒绝已落账但 flag 回写失败: %v", err)
			return connect.NewResponse(resp), nil
		}
		// D-475a S4 写旗即踢（P1 修）：blocked 旗标不等 5min ticker 盲区；
		// 契约=任何旗标 mutation RPC 成功路径必须 kickConfig（含未来 ResolveFlag 解旗）。
		s.kickConfig()
		if level == "blocked" {
			s.revokeCombosForFlag(ctx, grad.VenueA, grad.VenueB, grad.Symbol)
		}
	}
	s.insertGraduationAlert(ctx, grad, "已拒绝", "")
	resp.Ok = true
	return connect.NewResponse(resp), nil
}

// insertGraduationAlert 毕业裁决 info 告警（feishu 投递走既有 pusher 链）。
func (s *Service) insertGraduationAlert(ctx context.Context, g *pgstore.CarryGraduation, action, detail string) {
	msg := fmt.Sprintf("[毕业链] %s×%s %s %s", g.VenuePair(), g.Symbol, g.Structure, action)
	if detail != "" {
		msg += "：" + detail
	}
	// D-508 S9：裁决路径=建议/白名单双域已落账→前端重取（blocked 旗可撤 combo）。
	s.PublishDataRefresh(RefreshGraduation, RefreshAllowRules)
	if err := s.gradDB().InsertAlert(ctx, nil, "info", msg); err != nil {
		log.Printf("[graduation] alert: %v", err)
	}
}
