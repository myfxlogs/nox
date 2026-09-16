package main

// D-307 S3：venue 全量合约枚举 Source——三所各封装自己的 N 个 HTTP 调用并归一成 Row。
// 新所接入=实现 Source + allSources 注册一行。全部端点 9-16 经 cex-proxy 实测（公共端点无凭据）。
// 数字字段兼容字符串/数字（flexFloat/flexInt），nil/解析失败置 0 不炸行。
// status 归一：各所「在交易中」状态统一为 "TRADING"（binance=TRADING/okx=live/gate=trading
// 且 !in_delisting），其余存原值——diff.go 的 DELISTED/RELISTED 只认 "TRADING"。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Row 归一化的单合约快照行。
type Row struct {
	Symbol, Base, Class, Status string
	LaunchTS                    time.Time // 零值=无上市时间
	FundingRate                 float64   // 单次结算费率（原始小数）
	IntervalH                   int       // funding 结算间隔小时（0=无数据）
	Vol24h, Mark, Index         float64
}

// Source venue 适配接口。
type Source interface {
	Venue() string
	Scan(ctx context.Context) ([]Row, error)
}

// allSources 注册表：新所=实现接口+注册一行。
func allSources(hc *http.Client, proxy string) []Source {
	return []Source{binance{hc, proxy}, okx{hc, proxy}, gate{hc, proxy}}
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
