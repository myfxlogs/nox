// Package eventwatch 采集事件驱动机会公告（私有化/要约/合并）→ 飞书推送（D-265）。
// 三源：HKEXnews 公告列表 + SEC EDGAR SC 13E-3 + 在跟代码定向。
// 数据直落 alerts 表（不进 fact 流），复用 D-264 alertHook→飞书推送链路。
package eventwatch

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

const (
	uaHeader = "ARBResearch admin@myfxlogs.org"
	// 关键词过滤（大小写不敏感）：私有化/协议安排/要约/scheme/voluntary offer/privatisation
	// 用于全量公告列表中筛选事件驱动信号。
	// failStreak/zeroStreak 阈值：连续 3 天（≈36 个 2h tick）→ 推 warn（设计 §5）。
	streakThreshold = 36
)

var keywords = []string{
	"私有化", "协议安排", "要约",
	"scheme", "voluntary offer", "privatisation",
	"going private", "takeover", "tender offer",
}

// alertStore 采集器所需的告警存储接口（解耦 *pgstore.Store，测试可注入 fake）。
type alertStore interface {
	AlertExistsByFragment(ctx context.Context, fragment string) (bool, error)
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
}

// Collector 采集事件驱动机会公告，命中新公告 → InsertAlert(warn) → D-264 飞书推送。
type Collector struct {
	store      alertStore
	watchCodes []string // 在跟代码（00626, 01788），全部公告不限关键词
	hc         *http.Client
	hkexBase   string
	edgarURL   string
	// 连续全源失败计数（in-memory，重启重置；连续 3 天=36 tick 全失败才推 warn）
	failStreak int
	// 连续全源零结果计数（R2：设计 §5；两源都成功但 0 条→累加，任一有结果→重置）
	zeroStreak int
	// 可注入（测试用）
	hkexFetch  func(ctx context.Context, hc *http.Client, base string) ([]hkexItem, error)
	edgarFetch func(ctx context.Context, hc *http.Client, url string) ([]edgarEntry, error)
}

// NewCollector 创建事件盯梢采集器。watchCodes 为在跟代码列表（全部公告不限关键词）。
func NewCollector(store *pgstore.Store, watchCodes []string) *Collector {
	return &Collector{
		store:      store,
		watchCodes: watchCodes,
		hc:         &http.Client{Timeout: 30 * time.Second},
		hkexBase:   "https://www1.hkexnews.hk",
		edgarURL:   "https://www.sec.gov/cgi-bin/browse-edgar?action=getcurrent&type=13E3&output=atom",
		hkexFetch:  fetchHKEX,
		edgarFetch: fetchEDGAR,
	}
}

func (c *Collector) Kind() string { return "eventwatch" }

// Poll 采集三源公告，命中新公告 → InsertAlert(warn)。不产 fact（直落 alerts 表）。
// 失效追踪（设计 §5）：
//   - failStreak：两源全失败 → 累加；任一成功 → 重置；连续 36 tick → warn 1 条后重置
//   - zeroStreak（R2）：两源都成功但 0 条 → 累加；任一有结果 → 重置；连续 36 tick → warn 1 条后重置
func (c *Collector) Poll(ctx context.Context) ([]fact.Fact, error) {
	var hkexCount, edgarCount, watchCount, pushed int
	var hkexFailed, edgarFailed bool

	// ① HKEXnews 全量公告 → 关键词过滤
	items, err := c.hkexFetch(ctx, c.hc, c.hkexBase)
	if err != nil {
		log.Printf("[eventwatch] hkex fetch: %v", err)
		hkexFailed = true
	} else {
		hkexCount = len(items)
		for _, item := range items {
			if matchKeyword(item.Title) {
				if c.pushAlert(ctx, item.NewsID, formatHKEX(item, false)) {
					pushed++
				}
			}
		}
	}

	// ② 在跟代码定向（全部公告不限关键词）
	for _, item := range items {
		if matchWatchCode(item.StockCode, c.watchCodes) {
			watchCount++
			if c.pushAlert(ctx, item.NewsID, formatHKEX(item, true)) {
				pushed++
			}
		}
	}

	// ③ SEC EDGAR SC 13E-3
	entries, err := c.edgarFetch(ctx, c.hc, c.edgarURL)
	if err != nil {
		log.Printf("[eventwatch] edgar fetch: %v", err)
		edgarFailed = true
	} else {
		edgarCount = len(entries)
		for _, e := range entries {
			if c.pushAlert(ctx, e.ID, formatEDGAR(e)) {
				pushed++
			}
		}
	}

	// 失效追踪：failStreak（两源全失败 → 累加；任一成功 → 重置）
	if hkexFailed && edgarFailed {
		c.failStreak++
		if c.failStreak >= streakThreshold {
			log.Printf("[eventwatch] 连续 %d 次全源失败，推送通道告警", c.failStreak)
			_ = c.store.InsertAlert(ctx, nil, "warn",
				"[事件] 事件盯梢通道异常：连续 3 天采集全失败，需人工检查 HKEXnews/EDGAR 端点")
			c.failStreak = 0 // 推一条后重置，避免刷屏
		}
	} else {
		c.failStreak = 0
	}

	// 零结果追踪（R2 设计 §5）：两源都成功但 0 条 → 累加；任一有结果 → 重置
	if !hkexFailed && !edgarFailed && hkexCount == 0 && edgarCount == 0 {
		c.zeroStreak++
		if c.zeroStreak >= streakThreshold {
			log.Printf("[eventwatch] 连续 %d 次全源零结果，推送通道告警", c.zeroStreak)
			_ = c.store.InsertAlert(ctx, nil, "warn",
				"[事件] 事件盯梢通道异常：连续 3 天采集零结果，页面结构可能变更")
			c.zeroStreak = 0
		}
	} else if hkexCount > 0 || edgarCount > 0 {
		c.zeroStreak = 0
	}

	log.Printf("[eventwatch] hkex=%d edgar=%d watch=%d pushed=%d", hkexCount, edgarCount, watchCount, pushed)
	return nil, nil
}

// pushAlert 去重后写入告警。fragment = 公告唯一标识（NEWS_ID 或 EDGAR entry ID）。
// 返回 true=新写入，false=已存在跳过。
func (c *Collector) pushAlert(ctx context.Context, fragment, message string) bool {
	if fragment == "" {
		return false
	}
	exists, err := c.store.AlertExistsByFragment(ctx, fragment)
	if err != nil {
		log.Printf("[eventwatch] dedup check %s: %v", fragment, err)
		return false
	}
	if exists {
		return false
	}
	if err := c.store.InsertAlert(ctx, nil, "warn", message); err != nil {
		log.Printf("[eventwatch] insert alert: %v", err)
		return false
	}
	return true
}

// matchKeyword 检查标题是否含事件驱动关键词（大小写不敏感）。
func matchKeyword(title string) bool {
	lower := strings.ToLower(title)
	for _, kw := range keywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// matchWatchCode 检查 STOCK_CODE 字段（可能含 <br/> 分隔多代码）是否含在跟代码。
func matchWatchCode(stockCodeField string, watchCodes []string) bool {
	codes := strings.Split(stockCodeField, "<br/>")
	for _, c := range codes {
		c = strings.TrimSpace(c)
		for _, w := range watchCodes {
			if c == w {
				return true
			}
		}
	}
	return false
}

// formatHKEX 生成 HKEXnews 告警文案。watch=true=在跟代码定向（标注"在跟"）。
func formatHKEX(item hkexItem, watch bool) string {
	prefix := "[事件] HKEXnews"
	if watch {
		prefix += " [在跟]"
	}
	code := cleanStockCode(item.StockCode)
	link := item.FileLink
	if strings.HasPrefix(link, "/") {
		link = "https://www1.hkexnews.hk" + link
	}
	return fmt.Sprintf("%s %s.HK：%s\n       %s", prefix, code, item.Title, link)
}

// formatEDGAR 生成 SEC EDGAR 告警文案。
func formatEDGAR(e edgarEntry) string {
	link := e.Link
	if link == "" {
		link = e.ID
	}
	return fmt.Sprintf("[事件] SEC EDGAR SC 13E-3：%s\n       %s", e.Title, link)
}

// cleanStockCode 取 STOCK_CODE 字段第一个代码（去掉 <br/> 多代码）。
func cleanStockCode(field string) string {
	if idx := strings.Index(field, "<br/>"); idx >= 0 {
		return strings.TrimSpace(field[:idx])
	}
	return strings.TrimSpace(field)
}
