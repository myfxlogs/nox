package cnfi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// NAVCollector 基金净值日采（东财 lsjz，契约=规格包 §1.6；T+1 best-effort，失败不阻塞主线）。
// 事件范式（D-232）：挂既有 Scheduler 心跳（1h），Poll 内部「净值日落后于 T-1」双门控。
type NAVCollector struct {
	store   *pgstore.Store
	navBase string // lsjz URL 模板（fundCode/pageIndex/pageSize 三参数，D-247 S3 回填共用）
	nowFn   func() time.Time
	fetchFn func(ctx context.Context, url string) ([]byte, error) // 可注入（测试）
}

func NewNAVCollector(store *pgstore.Store) *NAVCollector {
	return &NAVCollector{
		store:   store,
		navBase: "https://api.fund.eastmoney.com/f10/lsjz",
		nowFn:   func() time.Time { return time.Now() },
	}
}

func (c *NAVCollector) Kind() string { return "cnfi_nav" }

// eastmoney lsjz JSON 子集（契约字段 FSRQ/DWJZ/LJJZ）。
type lsjzResp struct {
	Data struct {
		LSJZList []struct {
			FSRQ string `json:"FSRQ"`
			DWJZ string `json:"DWJZ"`
			LJJZ string `json:"LJJZ"`
		} `json:"LSJZList"`
	} `json:"Data"`
	ErrCode int `json:"ErrCode"`
}

func (c *NAVCollector) navURL(code string, pageIndex, pageSize int) string {
	return fmt.Sprintf("%s?fundCode=%s&pageIndex=%d&pageSize=%d", c.navBase, code, pageIndex, pageSize)
}

func (c *NAVCollector) Poll(ctx context.Context) ([]fact.Fact, error) {
	// 活跃基金清单（D-047：code 公开非个人数据）。
	rs, err := c.store.Pool().Query(ctx,
		`SELECT code, name FROM cnfi_instruments WHERE kind='fund' AND is_active ORDER BY code`)
	if err != nil {
		return nil, err
	}
	var codes []string
	for rs.Next() {
		var code, name string
		if err := rs.Scan(&code, &name); err != nil {
			rs.Close()
			return nil, err
		}
		codes = append(codes, code)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return nil, err
	}
	for _, code := range codes {
		if err := c.pollOne(ctx, code); err != nil {
			// best-effort：单基金失败记日志不中断其他基金（§1.6 失败告警由断流规则兜底）
			log.Printf("cnfi: nav %s: %v", code, err)
		}
		time.Sleep(3 * time.Second) // 礼貌间隔
	}
	return nil, nil
}

func (c *NAVCollector) pollOne(ctx context.Context, code string) error {
	// 门控：本地最新净值日 >= 昨天（T+1 发布）即跳过。
	latest, err := c.store.LatestCNFINAVDate(ctx, code)
	if err != nil {
		return err
	}
	yesterday := c.nowFn().AddDate(0, 0, -1).Format("2006-01-02")
	if latest >= yesterday {
		return nil
	}
	raw, err := c.fetch(ctx, c.navURL(code, 1, 5))
	if err != nil {
		return err
	}
	entries, err := parseNAVBody(raw)
	if err != nil {
		return fmt.Errorf("nav %s: %w", code, err)
	}
	for _, e := range entries {
		if err := c.store.UpsertCNFINAV(ctx, code, e.date, e.nav, e.ljjz, "eastmoney", time.Now().UTC()); err != nil {
			return err
		}
	}
	// D-247 S3 日采增量断点检测：新点 vs 前一非空 LJJZ 点（首点不判），>15% 写 caliber_break_date。
	c.detectIncrementalBreak(ctx, code)
	return nil
}

// detectIncrementalBreak D-247 S3/R1：日采增量口径断点检测（新点 vs 前一非空 LJJZ 点）。
func (c *NAVCollector) detectIncrementalBreak(ctx context.Context, code string) {
	last2, err := c.store.LastTwoCNFILJJZ(ctx, code)
	if err != nil || len(last2) < 2 {
		return // 首点不判
	}
	prev, cur := last2[1].LJJZ, last2[0].LJJZ
	if prev <= 0 {
		return
	}
	if chg := (cur - prev) / prev; chg > 0.15 || chg < -0.15 {
		day, _ := time.Parse("2006-01-02", last2[0].Date)
		log.Printf("cnfi: caliber break: code=%s date=%s", code, last2[0].Date)
		if err := c.store.SetCNFIInstrumentBreak(ctx, code, day); err != nil {
			log.Printf("cnfi: set break: %v", err)
		}
	}
}

// navEntry is one validated NAV row (D-247 S2：ljjz=累计净值，收益计算基准；nil=源未披露)。
type navEntry struct {
	date string
	nav  float64
	ljjz *float64
}

// parseNAVBody extracts valid entries: 空 FSRQ/空 DWJZ/非正净值全部拒绝（R3 教训：空键静默坍缩）。
// LJJZ 空串 → ljjz=nil 容忍（仅该字段缺失，不坍缩整行）。
func parseNAVBody(body []byte) ([]navEntry, error) {
	var resp lsjzResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}
	if len(resp.Data.LSJZList) == 0 {
		return nil, fmt.Errorf("empty LSJZList（源空=断流信号输入，非静默）")
	}
	var out []navEntry
	for _, e := range resp.Data.LSJZList {
		if e.FSRQ == "" || e.DWJZ == "" {
			continue
		}
		nav, err := parseNav(e.DWJZ)
		if err != nil {
			continue
		}
		var ljjz *float64
		if e.LJJZ != "" {
			if v, err := parseNav(e.LJJZ); err == nil && v > 0 {
				ljjz = &v
			}
		}
		out = append(out, navEntry{date: e.FSRQ, nav: nav, ljjz: ljjz})
	}
	return out, nil
}

func parseNav(s string) (float64, error) {
	var v float64
	if _, err := fmt.Sscanf(s, "%f", &v); err != nil {
		return 0, err
	}
	if v <= 0 {
		return 0, fmt.Errorf("nav must be positive, got %v", v)
	}
	return v, nil
}

func (c *NAVCollector) fetch(ctx context.Context, url string) ([]byte, error) {
	if c.fetchFn != nil {
		return c.fetchFn(ctx, url)
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/108.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://fundf10.eastmoney.com/")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("eastmoney http %d", resp.StatusCode)
	}
	return body, nil
}
