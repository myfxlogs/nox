package main

// venue 全量合约枚举 Source——三所各封装自己的 N 个 HTTP 调用并归一成 Row。
// 新所接入=实现 Source + allSources 注册一行。全部端点 9-16 经 cex-proxy 实测（公共端点无凭据）。
// 数字字段兼容字符串/数字（flexFloat/flexInt），nil/解析失败置 0 不炸行。
// status 归一：各所「在交易中」状态统一为 "TRADING"（binance=TRADING/okx=live/gate=trading
// 且 !in_delisting），其余存原值——diff.go 的 DELISTED/RELISTED 只认 "TRADING"。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Row 归一化的单合约快照行。
type Row struct {
	Symbol, Base, Class, Status string
	ContractType                string    // PERP/FUTURES（源适配显式赋；空=perp 存量语义）
	LaunchTS                    time.Time // 零值=无上市时间
	ExpiryTS                    time.Time // FUTURES 交割日（venue 仪器字段原值；零值=无→basis_ann nil）
	FundingRate                 float64   // 单次结算费率（原始小数）
	IntervalH                   int       // funding 结算间隔小时（0=无数据）
	Vol24h, Mark, Index         float64
	CtVal                       *float64 // D-311 S2：合约面值（1 张=基础资产数）；nil=缺失/解析失败→落 NULL
}

// f64ptr 正数转指针；≤0 返回 nil（ct_val 无义值不落库，下游 fail-closed 兜底）。
func f64ptr(v float64) *float64 {
	if v <= 0 {
		return nil
	}
	return &v
}

// Source venue 适配接口。
type Source interface {
	Venue() string
	Scan(ctx context.Context) ([]Row, error)
}

// allSources 注册表：新所=实现接口+注册一行。
// D-205①：proxy 空→per-source 官方公共域直连（binance/okx/gate 公共端点无凭据——
// 域为 venue 官方文档钦定取值非域值编造；路径自带 /fapi、/api/v5、/api/v4 前缀）；
// proxy 非空→现状 cex-proxy 径（封禁防护同源）。
func allSources(hc *http.Client, proxy string, pool *pgxpool.Pool, cacheDir string) []Source {
	bbase, obase, gbase := proxy, proxy, proxy
	if proxy == "" {
		bbase = "https://fapi.binance.com"
		obase = "https://www.okx.com"
		gbase = "https://api.gateio.ws"
	}
	return []Source{
		binance{hc: hc, base: bbase, pool: pool, cacheDir: cacheDir},
		okx{hc: hc, base: obase, pool: pool, cacheDir: cacheDir, throttle: 110 * time.Millisecond},
		gate{hc: hc, base: gbase, pool: pool},
	}
}

// fundNote D-205③：覆盖文案分「行在值空」vs「无行」——venue 枚举空集与
// funding 覆盖缺口是两态（前者 venue 面病灶，后者数据面缺口）。
func fundNote(venue string, rows, funded int) {
	switch {
	case rows == 0:
		log.Printf("[universe-radar] %s 无行（venue 枚举空集）", venue)
	case funded < rows:
		log.Printf("[universe-radar] %s funding 行在值空：覆盖 %d/%d", venue, funded, rows)
	}
}

func getJSON(ctx context.Context, hc *http.Client, url string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

// flexFloat 兼容字符串/数字 JSON 字段；null/空串/解析失败置 0。
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		*f = 0
		return nil
	}
	*f = flexFloat(v)
	return nil
}

// flexInt 同上，整型语义（ms/秒时间戳、funding_interval）。
type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		if fv, ferr := strconv.ParseFloat(s, 64); ferr == nil {
			*f = flexInt(int64(fv))
			return nil
		}
		*f = 0
		return nil
	}
	*f = flexInt(v)
	return nil
}

func msTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func secTime(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}
