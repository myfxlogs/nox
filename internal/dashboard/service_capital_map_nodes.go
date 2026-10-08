// service_capital_map_nodes.go —— D-546 S2：全景树节点构造与三缺口缓存机制
// （从 service_capital_map.go 拆出控行数 <300；口径 SSOT 见彼文件头注）。
package dashboard

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	"arb/internal/bus"
	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/reconcile"
)

// capitalGaps 三缺口缓存读口：60s 窗口内复用上次实拉（A4：连开页面第二次零 REST）；
// 失败结果同样入缓存（防故障期 REST 风暴，Errors 随缓存透出，窗口后自动重试）；
// fetcher 缺省自装配（binance real spot 行），装配不出=nil（节点如实标缺）。
func (s *Service) capitalGaps(ctx context.Context) *reconcile.CapitalGaps {
	s.capGapsMu.Lock()
	defer s.capGapsMu.Unlock()
	if s.capGapsCache != nil && time.Since(s.capGapsAt) < s.capGapsTTL() {
		return s.capGapsCache
	}
	if s.capGaps == nil {
		g := s.newBinanceCapitalGaps(ctx)
		if g == nil {
			return nil
		}
		s.capGaps = g
	}
	gp, err := s.capGaps.Fetch(ctx)
	if gp == nil {
		return nil
	}
	if err != nil {
		log.Printf("[capital-map] gaps fetch: %v（降级入缓存，窗口后重试）", err)
	}
	s.capGapsCache = gp
	s.capGapsAt = time.Now()
	return gp
}

// capGapsTTL 缓存期（capGapsTTLDur 测试可缩短；缺省 60s）。
func (s *Service) capGapsTTL() time.Duration {
	if s.capGapsTTLDur > 0 {
		return s.capGapsTTLDur
	}
	return capitalGapTTL
}

// newBinanceCapitalGaps 生产装配：binance real spot 行凭据+resolveExchangeBaseURL 代理链
// （realOnlyConfigs：sim 行 fail-closed 不连——与 livePoolQuerier 同纪律）。
func (s *Service) newBinanceCapitalGaps(ctx context.Context) CapitalGapFetcher {
	cfgs, err := s.gradDB().ListExchangeConfigs(ctx)
	if err != nil {
		log.Printf("[capital-map] list exchange configs: %v", err)
		return nil
	}
	live := realOnlyConfigs(cfgs, &s.liveSimWarned, "capital-map")
	for i := range live {
		c := &live[i]
		if c.Venue == "binance" && c.VenueType == "spot" {
			return reconcile.NewBinanceCapitalGaps(resolveExchangeBaseURL(*c), c.APIKey, c.APISecret)
		}
	}
	log.Printf("[capital-map] 无 binance real spot 配置行——三缺口面如实标缺")
	return nil
}

// ledgerSpot 现货账本读口（nil 安全；D-542 同 TTL 语义）。
func (s *Service) ledgerSpot() (bus.BalanceEntry, bool) {
	if s.balLedger == nil {
		return bus.BalanceEntry{}, false
	}
	return s.balLedger.Get("binance:spot", s.liveRestTTL(), time.Now())
}

// capNode 已采节点（USD 折算：USDT/LDUSDT 1:1；杂币缺省标未折算，tick 通路由 capConvertTick 覆写）。
func capNode(label string, amount float64, ccy, src string, at time.Time) *v1.CapitalMapNode {
	n := &v1.CapitalMapNode{
		Label: label, Amount: amount, Currency: ccy, Source: src,
		UpdatedAt: at.UTC().Format(time.RFC3339),
	}
	if ccy == "USDT" || ccy == "LDUSDT" {
		n.HasUsd = true
		n.AmountUsd = amount
		return n
	}
	n.HasUsd = false
	n.Note = "未折算（无 " + ccy + "USDT 价，不猜）"
	return n
}

// capConvertTick 杂币折算：binance <币>USDT tick mid；缺价=has_usd=false 不猜。
// USDT/LDUSDT 已在 capNode 1:1 折算（HasUsd=true）——直接跳过，勿以 USDTUSDT 查询覆写。
func capConvertTick(ctx context.Context, s *Service, n *v1.CapitalMapNode) {
	if n.HasUsd {
		return
	}
	price, ok := s.store.LatestTickMid(ctx, "binance", n.Currency+"USDT")
	if !ok {
		n.HasUsd = false
		n.Note += "；未折算（无 " + n.Currency + "USDT 价，不猜）"
		return
	}
	n.HasUsd = true
	n.AmountUsd = n.Amount * price
	n.Note += "；折算价=binance " + n.Currency + "USDT 中间价"
}

// capMissing 未采节点（占位如实标缺，不猜零）。
func capMissing(label, reason string) *v1.CapitalMapNode {
	return &v1.CapitalMapNode{Label: label, Note: "未采：" + reason}
}

// faceErr 取指定面前缀的失败原因（前缀=采集器 Errors 约定：币本位面/期权面）。
func faceErr(gaps *reconcile.CapitalGaps, prefix string) (string, bool) {
	for _, e := range gaps.Errors {
		if strings.HasPrefix(e, prefix) {
			return e, true
		}
	}
	return "", false
}

// capCoinOptNodes 币本位/期权逐币节点（可用注记在前，折算注记在后；零行=诚实零节点）。
func capCoinOptNodes(ctx context.Context, s *Service, labelPrefix, src string, at time.Time, rows []capRow) []*v1.CapitalMapNode {
	if len(rows) == 0 {
		return []*v1.CapitalMapNode{capNode(labelPrefix+"无持仓", 0, "USDT", src, at)}
	}
	out := make([]*v1.CapitalMapNode, 0, len(rows))
	for _, r := range rows {
		n := capNode(labelPrefix+r.asset, r.amount, r.asset, src, at)
		n.Note = "可用 " + fmtFloat(r.avail) + " " + r.asset
		capConvertTick(ctx, s, n)
		out = append(out, n)
	}
	return out
}

// capRow 币本位/期权行归一（label 前缀拼 asset）。
type capRow struct {
	asset         string
	amount, avail float64
}

func coinmRows(gaps *reconcile.CapitalGaps) []capRow {
	out := make([]capRow, 0, len(gaps.CoinM))
	for _, r := range gaps.CoinM {
		out = append(out, capRow{asset: r.Asset, amount: r.Balance, avail: r.Available})
	}
	return out
}

func optionsRows(gaps *reconcile.CapitalGaps) []capRow {
	out := make([]capRow, 0, len(gaps.Options))
	for _, r := range gaps.Options {
		out = append(out, capRow{asset: r.Asset, amount: r.Equity, avail: r.Available})
	}
	return out
}

func faceFailed(gaps *reconcile.CapitalGaps, prefix string) bool {
	_, ok := faceErr(gaps, prefix)
	return ok
}

func faceMsg(gaps *reconcile.CapitalGaps, prefix string) string {
	msg, _ := faceErr(gaps, prefix)
	return msg
}

// capSrcNote allocPool.src → 中文来源注记。
func capSrcNote(p allocPool) string {
	switch p.src {
	case "ws":
		return "活账本（私有流）"
	case "rest":
		return "REST 校准（活账本回写）"
	}
	return "快照兜底"
}

// capTierSum 档合计（仅计已折算节点；has_usd=有任一可计节点）。
func capTierSum(t *v1.CapitalMapTier) {
	for _, n := range t.Nodes {
		if n.HasUsd {
			t.TotalUsd += n.AmountUsd
			t.HasUsd = true
		}
	}
}

func fmtFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
