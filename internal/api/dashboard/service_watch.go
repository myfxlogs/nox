// service_watch.go —— 业主视窗两件套 B：盯盘规则 CRUD RPC（watch_rule 表；
// 变更成功后 kickConfig → 引擎热载生效，D-469b 同径）。
package dashboard

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
	"fmt"
	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/store/pgstore"
)

// watchRuleOps/kind/fireMode 域值集——后端校验同 chk 约束（fail-closed 不静默改值）。
var watchRuleKinds = map[string]bool{"price": true}
var watchRuleOps = map[string]bool{"lt": true, "le": true, "gt": true, "ge": true}
var watchRuleModes = map[string]bool{"once": true, "repeat": true}
var watchRuleLevels = map[string]bool{"info": true, "warn": true, "critical": true}

func validateWatchRule(r *v1.WatchRule) error {
	if !watchRuleKinds[r.GetKind()] {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("kind=%q 不支持（首期仅 price）", r.GetKind()))
	}
	subject := strings.TrimSpace(r.GetSubject())
	if subject == "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("subject 空"))
	}
	if !watchRuleOps[r.GetOp()] {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("op=%q 非法", r.GetOp()))
	}
	if !watchRuleModes[r.GetFireMode()] {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("fire_mode=%q 非法", r.GetFireMode()))
	}
	if r.GetCooldownSec() < 0 {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("cooldown_sec<0"))
	}
	if lv := r.GetLevel(); lv != "" && !watchRuleLevels[lv] {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("level=%q 非法", lv))
	}
	if len(r.GetMessage()) > 200 {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("message 超 200 字符"))
	}
	return nil
}

// ListWatchRules 规则列表+stale 标（subject 最新报价超 freshTTL=stale——流死如实标不按旧值判）。
func (s *Service) ListWatchRules(ctx context.Context, _ *connect.Request[v1.ListWatchRulesRequest]) (*connect.Response[v1.ListWatchRulesResponse], error) {
	rules, err := s.store.ListWatchRules(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*v1.WatchRule, 0, len(rules))
	for _, r := range rules {
		out = append(out, watchRuleProto(r, s.ruleStale(r), s.ruleSourceMatched(r)))
	}
	return connect.NewResponse(&v1.ListWatchRulesResponse{Rules: out}), nil
}

// ruleStale stale 判定：venue_key 指定→该键最新报价；全源→subject 各源取最新。无报价=stale。
func (s *Service) ruleStale(r pgstore.WatchRule) bool {
	fresh := false
	if r.VenueKey != "" {
		if q, ok := s.qbus.Latest(r.Subject, r.VenueKey); ok && !q.Time.IsZero() {
			fresh = s.freshTTL <= 0 || q.Time.After(time.Now().Add(-s.freshTTL))
		}
	} else if venues := s.qbus.Snapshot(context.Background(), []string{r.Subject}); len(venues[r.Subject]) > 0 {
		for _, q := range venues[r.Subject] {
			if !q.Time.IsZero() && (s.freshTTL <= 0 || q.Time.After(time.Now().Add(-s.freshTTL))) {
				fresh = true
				break
			}
		}
	}
	return !fresh
}

// ruleSourceMatched 源对拍：subject 在 QuoteBus latest 面有任源报价=matched（读时计算非存储——
// 防 typo 规则静默永不响；arb source_matched 语义同构）。
func (s *Service) ruleSourceMatched(r pgstore.WatchRule) bool {
	if r.VenueKey != "" {
		_, ok := s.qbus.Latest(r.Subject, r.VenueKey)
		return ok
	}
	return len(s.qbus.Snapshot(context.Background(), []string{r.Subject})[r.Subject]) > 0
}

// UpsertWatchRule 增改（id=0 插入）；成功后 configKick 热载。
func (s *Service) UpsertWatchRule(ctx context.Context, req *connect.Request[v1.UpsertWatchRuleRequest]) (*connect.Response[v1.UpsertWatchRuleResponse], error) {
	r := req.Msg.GetRule()
	if r == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("rule 空"))
	}
	if err := validateWatchRule(r); err != nil {
		return nil, err
	}
	// enabled presence 语义：缺席=默认启用（新增/改单未携=开），显式 false 才可关。
	enabled := true
	if r.Enabled != nil {
		enabled = *r.Enabled
	}
	id, err := s.store.UpsertWatchRule(ctx, pgstore.WatchRule{
		ID: r.GetId(), Kind: r.GetKind(), Subject: strings.TrimSpace(r.GetSubject()),
		VenueKey: strings.TrimSpace(r.GetVenueKey()), Op: r.GetOp(), Threshold: r.GetThreshold(),
		FireMode: r.GetFireMode(), CooldownSec: int(r.GetCooldownSec()), Note: r.GetNote(),
		Level: r.GetLevel(), Message: r.GetMessage(), Enabled: enabled,
	})
	if err != nil {
		return nil, err
	}
	s.kickConfig()
	return connect.NewResponse(&v1.UpsertWatchRuleResponse{Id: id}), nil
}

// DeleteWatchRule 删规则；成功后 configKick 热载。
func (s *Service) DeleteWatchRule(ctx context.Context, req *connect.Request[v1.DeleteWatchRuleRequest]) (*connect.Response[v1.DeleteWatchRuleResponse], error) {
	if req.Msg.GetId() <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id=%d 非法", req.Msg.GetId()))
	}
	if err := s.store.DeleteWatchRule(ctx, req.Msg.GetId()); err != nil {
		return nil, err
	}
	s.kickConfig()
	return connect.NewResponse(&v1.DeleteWatchRuleResponse{}), nil
}

// watchRuleProto 行→proto（last_fired NULL→0；stale/source_matched 服务侧对拍注入）。
func watchRuleProto(r pgstore.WatchRule, stale, sourceMatched bool) *v1.WatchRule {
	var ts int64
	if r.LastFiredAt != nil {
		ts = r.LastFiredAt.Unix()
	}
	return &v1.WatchRule{
		Id: r.ID, Kind: r.Kind, Subject: r.Subject, VenueKey: r.VenueKey, Op: r.Op,
		Threshold: r.Threshold, FireMode: r.FireMode, CooldownSec: int32(r.CooldownSec),
		Note: r.Note, Enabled: &r.Enabled, LastFiredTs: ts, Stale: stale,
		Level: r.Level, Message: r.Message, SourceMatched: sourceMatched,
	}
}
