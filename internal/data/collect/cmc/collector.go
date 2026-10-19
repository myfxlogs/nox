package cmc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"nox/internal/data/fact"
	"nox/internal/store/pgstore"

	"github.com/jackc/pgx/v5"
)

// jwtProvider 抽象 JWT 获取（AuthManager 实现；测试用 mockAuth，提示词 S7 要求 mock AuthManager）。
type jwtProvider interface {
	JWT(ctx context.Context) (string, error)
}

// ---------- PricesCollector ----------

type PricesCollector struct {
	auth         jwtProvider
	instrumentID string
	restBase     string
	store        *pgstore.Store
	// 门控缓存（S4）：Poll 成功后缓存 market_status + 探测时刻；休市 15min 内短路不发 REST。
	lastStatus string
	lastProbe  time.Time
	// D-234 R-V S2：REST 写库后触发同通知（frameHook seam，仿 WsClient.SetFrameHook）。
	// nil=未接线→不通知（测试默认）。
	frameHook func(FrameKind)
	// 业主令 10-19③：报价条候选接 CMC——REST 价写库后同步 publish qbus（quoteHook seam）。
	// nil=未接线→不发帧（测试默认）。
	quoteHook func(bid, ask float64, ts time.Time)
}

func NewPricesCollector(auth jwtProvider, instrumentID string, store *pgstore.Store, restBase string) *PricesCollector {
	return &PricesCollector{auth: auth, instrumentID: instrumentID, store: store, restBase: restBase}
}

// SetFrameHook D-234 R-V S2：注入帧钩子（main 装配 dashboard.NotifyCMCFrame）；nil=未接线→不通知。
// REST 写库后触发同通知——5min mid/净敞口至少推得动（ws 静默期兜底）。
func (c *PricesCollector) SetFrameHook(fn func(FrameKind)) { c.frameHook = fn }

// SetQuoteHook 业主令 10-19③：注入报价总线钩子（main 装配 qbus.Publish）；nil=未接线→不发帧。
// 仅双侧价齐全帧才发（bid/ask 缺一=残帧不发——venue 偶返单边价实拍在案）。
func (c *PricesCollector) SetQuoteHook(fn func(bid, ask float64, ts time.Time)) {
	c.quoteHook = fn
}

func (c *PricesCollector) Kind() string { return fact.KindCMCPrice }

func (c *PricesCollector) Poll(ctx context.Context) ([]fact.Fact, error) {
	// 门控：休市且距上次探测 <15min → 直接短路（不发 REST；周末/日结请求 1440/天→~96/天）
	if strings.EqualFold(c.lastStatus, "closed") && time.Since(c.lastProbe) < 15*time.Minute {
		return nil, nil
	}
	jwt, err := c.auth.JWT(ctx)
	if err != nil {
		return nil, fmt.Errorf("cmc price JWT: %w", err)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET",
		c.restBase+"/v1/markets/prices/"+c.instrumentID, nil)
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Origin", "https://platform.cmcmarkets.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cmc price request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("cmc price: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cmc price read: %w", err)
	}
	var p priceJSON
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("cmc price unmarshal: %w", err)
	}
	quoteTime, _ := time.Parse(time.RFC3339, p.Time)
	mid := parseFloat(p.MidPrice)
	rec := pgstore.CMCPrice{
		InstrumentID: c.instrumentID,
		BidPrice:     parseFloat(p.BidPrice),
		AskPrice:     parseFloat(p.AskPrice),
		MidPrice:     mid,
		MarketStatus: p.MarketStatus,
		QuoteTime:    quoteTime,
		FetchedAt:    time.Now(),
	}
	if err := c.store.InsertCMCPrice(ctx, rec); err != nil {
		return nil, fmt.Errorf("cmc price insert: %w", err)
	}
	if c.quoteHook != nil && rec.BidPrice > 0 && rec.AskPrice > 0 {
		c.quoteHook(rec.BidPrice, rec.AskPrice, quoteTime)
	}
	// D-234 R-V S2：REST 写库后触发同通知（5min mid/净敞口至少推得动）。
	if c.frameHook != nil {
		defer func() { _ = recover() }()
		c.frameHook(FramePrice)
	}
	// 门控缓存更新（S4）：成功后记录 status + 探测时刻
	c.lastStatus = p.MarketStatus
	c.lastProbe = time.Now()
	return []fact.Fact{{
		Kind: fact.KindCMCPrice, Venue: "cmc", Symbol: "TRXUSD",
		Value: mid, Unit: fact.UnitPrice, Ts: time.Now(), Src: "cmc_rest",
	}}, nil
}

// StartWatchdog 数据新鲜度 watchdog（独立 goroutine，不依赖 Poll 成功）。
// 空表（ErrNoRows）也告警：last 取零值 → Since 巨大 → 触发 warning（v7）。
// v1.2 R'-1：门控使休市段更新间隔变 15min > 现行 stale 阈值 5min → 休市期误报。
// 修法：判定前查价格行最近 market_status——closed（EqualFold）则本轮跳过 stale 判定。
// D-225 v2.1：src 感知阈值——最近行为 cmc_ws → stale 阈值 10min（配合 S2 4min 心跳行消除贴线误报）；
// 否则现行为不变（5min+closed-skip）。
func (c *PricesCollector) StartWatchdog(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			src, status, last, err := c.store.LastCMCPriceStatusAndFetchedAt(ctx)
			if err != nil {
				if !errors.Is(err, pgx.ErrNoRows) {
					continue // 其他 DB 错误（连接故障）不判 stale
				}
				// 空表：last 取零值 → Since 巨大 → 触发 warning（v7 行为保留）
			} else if strings.EqualFold(status, "closed") {
				continue // 休市：门控使更新间隔变 15min，跳过 stale 判定（v1.2 R'-1）
			}
			staleThreshold := 5 * time.Minute
			if src == "cmc_ws" || src == "cmc_ws_hb" {
				staleThreshold = 10 * time.Minute // D-225 v2.1：WS 行 4min 心跳；D-370 S4：hb 行同阈值（4min 节奏不变）
			}
			if time.Since(last) > staleThreshold {
				msg := fmt.Sprintf("CMC 报价断流：%v 无价格更新（src=%s）——WS 传输层另见 ws_guard 告警", time.Since(last).Round(time.Second), src)
				// D-033 系统自盯：落 alerts 表（30min 去重防每分钟刷屏）；失败仅 log 不断 watchdog
				if err := c.store.InsertAlertDedup(ctx, "warn", msg, 30*time.Minute); err != nil {
					log.Printf("[cmc] stale alert insert: %v", err)
				}
				log.Printf("[cmc] stale warning: no price update for %v (src=%s)", time.Since(last).Round(time.Second), src)
			}
		}
	}
}

// ---------- AccountCollector 见 collector_account.go ----------

// ---------- HistoryCollector 见 history.go ----------
