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

	"arb/internal/fact"
	"arb/internal/store/pgstore"

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
	// D-225 v2.1 S3：ws 健康期降频——wsHealthy() && time.Since(lastWrite)<15min → return nil。
	wsHealthy func() bool
	lastWrite time.Time
	// D-234 R-V S2：REST 写库后触发同通知（frameHook seam，仿 WsClient.SetFrameHook）。
	// nil=未接线→不通知（测试默认）。
	frameHook func(FrameKind)
}

func NewPricesCollector(auth jwtProvider, instrumentID string, store *pgstore.Store, restBase string) *PricesCollector {
	return &PricesCollector{auth: auth, instrumentID: instrumentID, store: store, restBase: restBase}
}

// SetWSHealthy D-225 v2.1 S3：注入 ws 健康查询（wire 层回填）；nil=无 ws，照旧 REST 轮询。
func (c *PricesCollector) SetWSHealthy(fn func() bool) { c.wsHealthy = fn }

// SetFrameHook D-234 R-V S2：注入帧钩子（main 装配 dashboard.NotifyCMCFrame）；nil=未接线→不通知。
// REST 写库后触发同通知——5min mid/净敞口至少推得动（ws 静默期兜底）。
func (c *PricesCollector) SetFrameHook(fn func(FrameKind)) { c.frameHook = fn }

func (c *PricesCollector) Kind() string { return fact.KindCMCPrice }

func (c *PricesCollector) Poll(ctx context.Context) ([]fact.Fact, error) {
	// D-225 v2.1 S3：ws 健康期降频——15min 内短路（REST 只留 marketStatus 探测 + bid/mid 补充 + WS 不健康时接管）。
	if c.wsHealthy != nil && c.wsHealthy() && time.Since(c.lastWrite) < 15*time.Minute {
		return nil, nil
	}
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
	// D-234 R-V S2：REST 写库后触发同通知（5min mid/净敞口至少推得动）。
	if c.frameHook != nil {
		defer func() { _ = recover() }()
		c.frameHook(FramePrice)
	}
	// 门控缓存更新（S4）：成功后记录 status + 探测时刻
	c.lastStatus = p.MarketStatus
	c.lastProbe = time.Now()
	c.lastWrite = time.Now() // D-225 v2.1 S3：ws 健康期降频计时基准
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
				log.Printf("[cmc] stale warning: no price update for %v (src=%s)", time.Since(last).Round(time.Second), src)
			}
		}
	}
}

// ---------- AccountCollector 见 collector_account.go ----------

// ---------- HistoryCollector 见 history.go ----------
