// cex_io_fetch.go —— D-365 S3：binance/okx 双向拉取实现（自 cex_io.go 拆出保 check-lines
// <300；签名/路由核证点④见 cex_io.go 头注）。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"arb/internal/platform/httpx"
	"arb/internal/store/pgstore"
)

// fetchDirection 拉单配置单方向（binance/okx 分派），返回待插行（调用方幂等落库）。
func (c *CexIOCollector) fetchDirection(ctx context.Context, acc CexIOAccount, direction string) ([]pgstore.VenueIO, error) {
	watermark, hasWM, err := c.sink.LatestVenueIOTs(ctx, acc.ID, direction)
	if err != nil {
		return nil, err
	}
	startMS := c.now().AddDate(0, 0, -89).UnixMilli() // 核证点②：90d 窗口首拉
	if hasWM {
		startMS = watermark.UnixMilli() + 1
	}
	switch acc.Venue {
	case "binance":
		return c.fetchBinance(ctx, acc, direction, startMS)
	case "okx":
		return c.fetchOkx(ctx, acc, direction, hasWM, watermark)
	default:
		return nil, fmt.Errorf("unsupported venue %q", acc.Venue)
	}
}

// fetchBinance binance 两向（GET 签名；startTime 水位）。
func (c *CexIOCollector) fetchBinance(ctx context.Context, acc CexIOAccount, direction string, startMS int64) ([]pgstore.VenueIO, error) {
	sapiBase := httpx.BinanceSapiBase(acc.BaseURL) // 核证点④：/api 前缀 404，sapi 走代理根路径（path 自带 /sapi 段）
	extra := url.Values{}
	extra.Set("limit", "100")
	extra.Set("startTime", strconv.FormatInt(startMS, 10))
	var path string
	switch direction {
	case "deposit":
		path = "/sapi/v1/capital/deposit/hisrec"
	case "withdraw":
		path = "/sapi/v1/capital/withdraw/history"
	default:
		return nil, fmt.Errorf("bad direction %q", direction)
	}
	// binance 必须走 httpx.Client()——静态代理自签 CA（ARB_BINANCE_PROXY_CA）
	// 与 X-Proxy-Token（ARB_BINANCE_PROXY_TOKEN）全靠该 client 注入；裸 DefaultClient →
	// 生产 x509: unknown authority 每轮 skip（reconcile querier httpx.Client() 同款先例）。
	// okx 同走 httpx.Client()（D-396：代理需 CA+token；直连兜底时 token 头与公共采集现状一致无害）。
	body, err := signedBinanceGet(ctx, httpx.Client(), sapiBase, path, acc.APIKey, acc.APISecret, extra)
	if err != nil {
		return nil, err
	}
	out := []pgstore.VenueIO{}
	switch direction {
	case "deposit":
		var rows []cexIODepositRow
		if err := json.Unmarshal(body, &rows); err != nil {
			return nil, err
		}
		for _, r := range rows {
			amt, perr := f64z(r.Amount)
			if perr != nil {
				return nil, fmt.Errorf("parse %s=%q: %w", "r.Amount", r.Amount, perr)
			}
			out = append(out, pgstore.VenueIO{
				Venue: "binance", ConfigID: acc.ID, Direction: "deposit",
				Ccy: r.Coin, Amount: amt, Status: strconv.Itoa(r.Status),
				TxID: r.TxID, ExternalID: r.ID,
				Ts: time.UnixMilli(r.InsertTime),
			})
		}
	case "withdraw":
		var rows []cexIOWithdrawRow
		if err := json.Unmarshal(body, &rows); err != nil {
			return nil, err
		}
		for _, r := range rows {
			amt, perr := f64z(r.Amount)
			if perr != nil {
				return nil, fmt.Errorf("parse %s=%q: %w", "r.Amount", r.Amount, perr)
			}
			fee, perr := f64z(r.TransactionFee)
			if perr != nil {
				return nil, fmt.Errorf("parse %s=%q: %w", "r.TransactionFee", r.TransactionFee, perr)
			}
			// applyTime 是 "2006-01-02 15:04:05" 字符串（UTC），非 ms int64
			//（真探 9-21 实证："2026-09-10 16:04:44"）；解析失败 fail-closed 报错不断言。
			ts, terr := time.Parse("2006-01-02 15:04:05", r.ApplyTime)
			if terr != nil {
				return nil, fmt.Errorf("withdraw %s applyTime %q: %w", r.ID, r.ApplyTime, terr)
			}
			out = append(out, pgstore.VenueIO{
				Venue: "binance", ConfigID: acc.ID, Direction: "withdraw",
				Ccy: r.Coin, Amount: amt, Fee: &fee, Status: strconv.Itoa(r.Status),
				TxID: r.TxID, ExternalID: r.ID,
				Ts: ts.UTC(),
			})
		}
	}
	return out, nil
}

// fetchOkx okx 两向（GET 签名直连；limit=100 单页+客户端 ts>水位过滤，核证点①：
// 15min 间隔新增远小于 100 行，溢出补 before 游标为 v2 候选）。
func (c *CexIOCollector) fetchOkx(ctx context.Context, acc CexIOAccount, direction string, hasWM bool, watermark time.Time) ([]pgstore.VenueIO, error) {
	var path string
	switch direction {
	case "deposit":
		path = "/api/v5/asset/deposit-history?limit=100"
	case "withdraw":
		path = "/api/v5/asset/withdrawal-history?limit=100"
	default:
		return nil, fmt.Errorf("bad direction %q", direction)
	}
	body, err := signedOkxGet(ctx, httpx.Client(), acc.BaseURL, path, acc.APIKey, acc.APISecret, acc.Passphrase)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Code string `json:"code"`
		Data []cexIOOkxRow
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if resp.Code != "0" {
		return nil, fmt.Errorf("okx code=%s", resp.Code)
	}
	out := []pgstore.VenueIO{}
	for _, r := range resp.Data {
		tsMS, perr := strconv.ParseInt(r.Ts, 10, 64)
		if perr != nil {
			continue
		}
		ts := time.UnixMilli(tsMS)
		if hasWM && !ts.After(watermark) {
			continue // 客户端水位过滤（核证点①）
		}
		amt, perr := f64z(r.Amt)
		if perr != nil {
			return nil, fmt.Errorf("parse %s=%q: %w", "r.Amt", r.Amt, perr)
		}
		extID := r.DepID
		if direction == "withdraw" {
			extID = r.WdID // 核证点④：字段名 wdId 实证
		}
		if extID == "" {
			continue // 无外部 id 无法幂等，跳过不猜
		}
		row := pgstore.VenueIO{
			Venue: "okx", ConfigID: acc.ID, Direction: direction,
			Ccy: r.Ccy, Amount: amt, Status: r.State,
			TxID: r.TxID, ExternalID: extID, Ts: ts,
		}
		if direction == "withdraw" {
			fee, perr := f64z(r.Fee)
			if perr != nil {
				return nil, fmt.Errorf("parse %s=%q: %w", "r.Fee", r.Fee, perr)
			}
			row.Fee = &fee
		}
		out = append(out, row)
	}
	return out, nil
}
