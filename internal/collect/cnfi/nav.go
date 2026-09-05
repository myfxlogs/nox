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
	base    string
	nowFn   func() time.Time
	fetchFn func(ctx context.Context, url string) ([]byte, error) // 可注入（测试）
}

func NewNAVCollector(store *pgstore.Store) *NAVCollector {
	return &NAVCollector{
		store: store,
		base:  "https://api.fund.eastmoney.com/f10/lsjz?fundCode=%s&pageIndex=1&pageSize=5",
		nowFn: func() time.Time { return time.Now() },
	}
}

func (c *NAVCollector) Kind() string { return "cnfi_nav" }

// eastmoney lsjz JSON 子集（契约字段 FSRQ/DWJZ）。
type lsjzResp struct {
	Data struct {
		LSJZList []struct {
			FSRQ string `json:"FSRQ"`
			DWJZ string `json:"DWJZ"`
		} `json:"LSJZList"`
	} `json:"Data"`
	ErrCode int `json:"ErrCode"`
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
	url := fmt.Sprintf(c.base, code)
	raw, err := c.fetch(ctx, url)
	if err != nil {
		return err
	}
	entries, err := parseNAVBody(raw)
	if err != nil {
		return fmt.Errorf("nav %s: %w", code, err)
	}
	for _, e := range entries {
		if err := c.store.UpsertCNFINAV(ctx, code, e.date, e.nav, "eastmoney", time.Now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

// navEntry is one validated NAV row.
type navEntry struct {
	date string
	nav  float64
}

// parseNAVBody extracts valid entries: 空 FSRQ/空 DWJZ/非正净值全部拒绝（R3 教训：空键静默坍缩）。
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
		out = append(out, navEntry{date: e.FSRQ, nav: nav})
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
