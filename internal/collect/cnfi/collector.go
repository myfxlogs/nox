package cnfi

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// MarketCollector 按 20:30 Asia/Shanghai 发布窗日采一次 chinabond 收盘曲线。
// 事件范式（D-232 单心跳约束）：挂既有 Scheduler 心跳（10min 间隔），
// Poll 内部按「发布窗已过 + 今日未采」双门控，实际抓取每日 ≤1 次，无新定时器。
// SSOT: docs/cnfi/CN-FI-P0.5规格包-2026-09-05-ZCode.md v0.2 §1.1/§4.3。
type MarketCollector struct {
	store        *pgstore.Store
	base         string
	location     *time.Location
	publishClock int
	publishMin   int
	cutoffClock  int                                                             // 断流判定截止点（发布窗+1h）
	fetchFn      func(ctx context.Context, start, end time.Time) (string, error) // 可注入（测试）
	nowFn        func() time.Time
}

func NewMarketCollector(store *pgstore.Store) *MarketCollector {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600) // 容器无 tzdata 兜底
	}
	return &MarketCollector{
		store:        store,
		base:         "https://yield.chinabond.com.cn/cbweb-pbc-web/pbc/historyQuery",
		location:     loc,
		publishClock: 20,
		publishMin:   30,
		cutoffClock:  21,
		nowFn:        func() time.Time { return time.Now() },
	}
}

// Kind satisfies collect.Collector.
func (c *MarketCollector) Kind() string { return "cnfi_market" }

// Poll satisfies collect.Collector. CN-FI 不产 fact 帧（数据直落 cnfi_*，快照 RPC 消费）。
func (c *MarketCollector) Poll(ctx context.Context) ([]fact.Fact, error) {
	now := c.nowFn().In(c.location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, c.location)
	if now.Hour() < c.publishClock || (now.Hour() == c.publishClock && now.Minute() < c.publishMin) {
		return nil, nil // 未到发布窗
	}
	seeded, err := c.alreadyCollected(ctx, today)
	if err != nil {
		return nil, fmt.Errorf("cnfi: collected check: %w", err)
	}
	if seeded {
		return nil, nil
	}
	// F4：日历缺失=calendar_unknown——不产断流结论，仅尝试抓取（假日源方自然无数据）
	if has, err := c.store.CNFICalendarHasDate(ctx, today); err != nil {
		log.Printf("cnfi: calendar query: %v", err)
	} else if !has {
		log.Printf("cnfi: calendar_unknown for %s（不产断流结论，F4）", today.Format("2006-01-02"))
	}
	written, changed, _, err := c.collect(ctx, today.AddDate(0, 0, -7), now)
	if err != nil {
		return nil, err
	}
	log.Printf("cnfi: daily collect written=%d changed=%d", written, changed)
	// P2：采集成功即评估信号（observe-only，C11）——同作业内评估（C3 生命周期）
	if err := EvaluateSignals(ctx, c.store, true); err != nil {
		log.Printf("cnfi: signal evaluate: %v（不阻断采集成功）", err)
	}
	return nil, nil
}

func (c *MarketCollector) alreadyCollected(ctx context.Context, today time.Time) (bool, error) {
	var maxAsOf string
	err := c.store.Pool().QueryRow(ctx,
		`SELECT COALESCE(max(as_of)::text,'') FROM cnfi_series WHERE series='cgb'`).Scan(&maxAsOf)
	if err != nil {
		return false, err
	}
	return maxAsOf >= today.Format("2006-01-02"), nil
}

// collect fetches year-sliced windows（复审 P3 注记①）and upserts.
func (c *MarketCollector) collect(ctx context.Context, start, end time.Time) (int, int, *time.Time, error) {
	var written, changed int
	var maxAsOf *time.Time
	var firstErr error
	for s := start; !s.After(end); s = s.AddDate(1, 0, 0) {
		e := s.AddDate(1, 0, -1)
		if e.After(end) {
			e = end
		}
		body, err := c.fetch(ctx, s, e)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s..%s: %w", s.Format("2006-01-02"), e.Format("2006-01-02"), err)
			}
			continue
		}
		points, warns, err := ParseHistoryHTML(body)
		for _, w := range warns {
			log.Printf("cnfi: warn: %s", w)
		}
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s..%s: %w", s.Format("2006-01-02"), e.Format("2006-01-02"), err)
			}
			continue
		}
		cnfiPoints := make([]pgstore.CNFIPoint, 0, len(points))
		for _, p := range points {
			cnfiPoints = append(cnfiPoints, pgstore.CNFIPoint{AsOf: p.AsOf, Series: p.Series, Tenor: p.Tenor, ValueBP: p.ValueBP})
			if t, err := time.Parse("2006-01-02", p.AsOf); err == nil && (maxAsOf == nil || t.After(*maxAsOf)) {
				tt := t
				maxAsOf = &tt
			}
		}
		w, ch, err := c.store.UpsertCNFISeries(ctx, cnfiPoints, "chinabond", "core-daily", time.Now().UTC())
		written += w
		changed += ch
		if err != nil && firstErr == nil {
			firstErr = err
		}
		time.Sleep(5 * time.Second)
	}
	return written, changed, maxAsOf, firstErr
}

func (c *MarketCollector) fetch(ctx context.Context, start, end time.Time) (string, error) {
	if c.fetchFn != nil {
		return c.fetchFn(ctx, start, end)
	}
	url := fmt.Sprintf("%s?startDate=%s&endDate=%s&gjqx=0&qxId=ycqx&locale=cn_ZH",
		c.base, start.Format("2006-01-02"), end.Format("2006-01-02"))
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/108.0.0.0 Safari/537.36")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("chinabond http %d", resp.StatusCode)
	}
	return string(body), nil
}

// WatchdogOnce 断流判定（C4 市场日历制）：营业日过发布截止点无新点 → warn；
// 跨下一营业日仍无 → critical（InsertAlert）。周末/假日/calendar_unknown 完全跳过（F4）。
// 由调用方既有心跳按小时驱动（无新定时器）。
func (c *MarketCollector) WatchdogOnce(ctx context.Context, insertAlert func(level, msg string) error) {
	now := c.nowFn().In(c.location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, c.location)
	has, err := c.store.CNFICalendarHasDate(ctx, today)
	if err != nil || !has {
		return // calendar_unknown（F4）：不产断流结论
	}
	var business bool
	if err := c.store.Pool().QueryRow(ctx,
		`SELECT is_business_day FROM cnfi_calendar WHERE cal_date=$1`, today.Format("2006-01-02")).Scan(&business); err != nil || !business {
		return
	}
	if now.Hour() < c.cutoffClock {
		return
	}
	seeded, err := c.alreadyCollected(ctx, today)
	if err != nil {
		log.Printf("cnfi: watchdog: %v", err)
		return
	}
	if seeded {
		return
	}
	msg := fmt.Sprintf("CN-FI 营业日 %s 已过发布截止点无新点（断流，观察）", today.Format("2006-01-02"))
	log.Printf("cnfi: WARN %s", msg)
	if insertAlert != nil {
		if err := insertAlert("warn", msg); err != nil {
			log.Printf("cnfi: watchdog alert: %v", err)
		}
	}
}
