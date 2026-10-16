// cex_transfer_fetch.go —— D-539①：CEX 内部划转采集（钱包间移动台账）。
// direction='transfer'，唯一键沿用 (venue,config_id,direction,external_id)：
// binance tranId / okx billId / binance earn purchaseId·redeemId。
// 钱包名与 fin_venue_account_snapshot.venue_type 同键域（spot/funding/earn/perp/
// unified/margin/…）——残差对账件按 venue_type 直配。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"time"

	"nox/internal/platform/httpx"
	"nox/internal/store/pgstore"
)

// binanceTransferTypes D-539①：asset/transfer 枚举面——万能划转史须逐 type 拉
// （endpoint 无全量无 type 径）。覆盖系统内已建池对应钱包对；未列 type 的划转漏采
// → 残差对账会报 unexplained（自盯补口语义：漏采以告警暴露而非静默）。
var binanceTransferTypes = []string{
	"MAIN_UMFUTURE", "UMFUTURE_MAIN",
	"MAIN_CMFUTURE", "CMFUTURE_MAIN",
	"MAIN_MARGIN", "MARGIN_MAIN",
	"MAIN_FUNDING", "FUNDING_MAIN",
	"FUNDING_UMFUTURE", "UMFUTURE_FUNDING",
	"FUNDING_CMFUTURE", "CMFUTURE_FUNDING",
	"MAIN_PORTFOLIO", "PORTFOLIO_MAIN",
	"MAIN_MINING", "MINING_MAIN",
}

// binanceIsolatedMarginTypes 不可采域（裁定=标域不逐 symbol 拉）：
// *_ISOLATED_MARGIN* 四型端点需逐 symbol 参数（不带必 400「Symbol is null
// or empty!」，SG 实证灭整批病灶）；本账户 isolated margin 钱包池未建
// （capability 账本无凭据行），逐 symbol 枚举面不存在=如实标域外——域
// 启用时须先立 isolated margin symbol 采集面再回接。每拍 log warn 一次
// 钉缺席原因（防静默）。
var binanceIsolatedMarginTypes = []string{
	"MAIN_ISOLATED_MARGIN", "ISOLATED_MARGIN_MAIN",
	"ISOLATED_MARGIN_UMFUTURE", "UMFUTURE_ISOLATED_MARGIN",
}

// binanceWalletOf asset/transfer type 两段码 → 快照 venue_type 键域名。
// 未知段如实落原码小写（不映射不留空——对账面看得见新钱包）。
func binanceWalletOf(seg string) string {
	switch seg {
	case "MAIN":
		return "spot"
	case "UMFUTURE":
		return "perp"
	case "CMFUTURE":
		return "cmfuture"
	case "MARGIN":
		return "margin"
	case "ISOLATED_MARGIN":
		return "isolated_margin"
	case "FUNDING":
		return "funding"
	case "PORTFOLIO":
		return "portfolio"
	case "MINING":
		return "mining"
	case "CARD":
		return "card"
	case "PAXG":
		return "paxg"
	default:
		return "acct_" + seg
	}
}

// okxWalletOf asset/bills from/to 账户码 → venue_type 键域名
// （okx 账户码：6=funding，18=trading/unified；其他如实记 acct_<n>）。
func okxWalletOf(code string) string {
	switch code {
	case "6":
		return "funding"
	case "18":
		return "unified"
	default:
		return "acct_" + code
	}
}

// binanceTransferRow /sapi/v1/asset/transfer 行。
type binanceTransferRow struct {
	TranID    int64  `json:"tranId"`
	Asset     string `json:"asset"`
	Amount    string `json:"amount"`
	Status    string `json:"status"` // CONFIRMED/FAILED/PENDING
	Timestamp int64  `json:"timestamp"`
}

// binanceEarnHistRow simple-earn flexible subscription/redemption history 行
// （D-524 申购反推升级为真台账：申购=spot→earn、赎回=earn→spot 划转事件）。
type binanceEarnHistRow struct {
	PurchaseID json.Number `json:"purchaseId"` // Record 端点为数值（*History 旧名时为串）——Number 双兼容
	RedeemID   json.Number `json:"redeemId"`
	Asset      string      `json:"asset"`
	Amount     string      `json:"amount"`
	Status     string      `json:"status"`
	Time       int64       `json:"time"`
}

// fetchTransfer 拉单配置内部划转（binance=asset/transfer 逐 type + earn 申购赎回史；
// okx=asset/bills 划转类行）。水位沿用 LatestVenueIOTs(direction='transfer')。
func (c *CexIOCollector) fetchTransfer(ctx context.Context, acc CexIOAccount) ([]pgstore.VenueIO, error) {
	watermark, hasWM, err := c.sink.LatestVenueIOTs(ctx, acc.ID, "transfer")
	if err != nil {
		return nil, err
	}
	startMS := c.now().AddDate(0, 0, -89).UnixMilli() // 同 deposit/withdraw 首拉窗口
	if hasWM {
		startMS = watermark.UnixMilli() + 1
	}
	switch acc.Venue {
	case "binance":
		return c.fetchBinanceTransfer(ctx, acc, startMS)
	case "okx":
		return c.fetchOkxTransfer(ctx, acc, hasWM, watermark)
	default:
		return nil, fmt.Errorf("unsupported venue %q", acc.Venue)
	}
}

// fetchBinanceTransfer asset/transfer 逐 type + simple-earn 申购/赎回史。
func (c *CexIOCollector) fetchBinanceTransfer(ctx context.Context, acc CexIOAccount, startMS int64) ([]pgstore.VenueIO, error) {
	sapiBase := httpx.BinanceSapiBase(acc.BaseURL)
	out := []pgstore.VenueIO{}
	for _, typ := range binanceTransferTypes {
		extra := url.Values{}
		extra.Set("type", typ)
		extra.Set("limit", "100")
		extra.Set("startTime", strconv.FormatInt(startMS, 10))
		body, err := signedBinanceGet(ctx, httpx.Client(), sapiBase, "/sapi/v1/asset/transfer", acc.APIKey, acc.APISecret, extra)
		if err != nil {
			// per-type skip：单型错只跳该型记 warn，不弃整批（一颗雷灭全部
			// =SG 实证病灶，fail-fast 禁回潮）。
			log.Printf("[io] binance transfer %s 型错跳过（留痕防静默）: %v", typ, err)
			continue
		}
		var resp struct {
			Rows []binanceTransferRow `json:"rows"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			log.Printf("[io] binance transfer %s 解码错跳过: %v", typ, err)
			continue
		}
		for _, r := range resp.Rows {
			if r.Status != "CONFIRMED" {
				continue // 未成账不入台账（FAILED/PENDING 不改余额）
			}
			amt, perr := f64z(r.Amount)
			if perr != nil {
				return nil, fmt.Errorf("parse transfer amount %q: %w", r.Amount, perr)
			}
			// type=A_B：A=源钱包 B=目的钱包（venue 枚举两段式）。
			var from, to string
			for i, ch := range typ {
				if ch == '_' {
					from, to = binanceWalletOf(typ[:i]), binanceWalletOf(typ[i+1:])
					break
				}
			}
			out = append(out, pgstore.VenueIO{
				Venue: "binance", ConfigID: acc.ID, Direction: "transfer",
				Ccy: r.Asset, Amount: amt, Status: r.Status,
				ExternalID: strconv.FormatInt(r.TranID, 10),
				Ts:         time.UnixMilli(r.Timestamp),
				FromWallet: from, ToWallet: to,
			})
		}
	}
	// simple-earn 申购/赎回史（D-524 反推升级真台账；limit=100 单页同既有惯例）。
	for _, leg := range []struct {
		path, idKey string
		from, to    string
	}{
		{"/sapi/v1/simple-earn/flexible/history/subscriptionRecord", "purchaseId", "spot", "earn"},
		{"/sapi/v1/simple-earn/flexible/history/redemptionRecord", "redeemId", "earn", "spot"},
	} {
		extra := url.Values{}
		extra.Set("size", "100")
		extra.Set("startTime", strconv.FormatInt(startMS, 10))
		body, err := signedBinanceGet(ctx, httpx.Client(), sapiBase, leg.path, acc.APIKey, acc.APISecret, extra)
		if err != nil {
			log.Printf("[io] binance earn hist %s 错跳过: %v", leg.path, err)
			continue
		}
		var resp struct {
			Rows []binanceEarnHistRow `json:"rows"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			log.Printf("[io] binance earn hist %s 解码错跳过: %v", leg.path, err)
			continue
		}
		for _, r := range resp.Rows {
			if r.Status != "SUCCESS" {
				continue
			}
			id := r.PurchaseID.String()
			if leg.idKey == "redeemId" {
				id = r.RedeemID.String()
			}
			if id == "" || id == "0" {
				continue // 无幂等键不猜
			}
			amt, perr := f64z(r.Amount)
			if perr != nil {
				return nil, fmt.Errorf("parse earn hist amount %q: %w", r.Amount, perr)
			}
			out = append(out, pgstore.VenueIO{
				Venue: "binance", ConfigID: acc.ID, Direction: "transfer",
				Ccy: r.Asset, Amount: amt, Status: "SUCCESS",
				ExternalID: "se-" + id,
				Ts:         time.UnixMilli(r.Time),
				FromWallet: leg.from, ToWallet: leg.to,
			})
		}
	}
	if len(binanceIsolatedMarginTypes) > 0 {
		log.Printf("[io] binance isolated-margin 四型域外未采（需逐 symbol 参数，域未启用）: %v", binanceIsolatedMarginTypes)
	}
	return out, nil
}

// okxAssetBillRow /api/v5/asset/bills 行（资金账户账单；划转行带 from/to 账户码）。
type okxAssetBillRow struct {
	BillID  string `json:"billId"`
	Ccy     string `json:"ccy"`
	BalChg  string `json:"balChg"`
	Type    string `json:"type"`    // 大类（130=transfer 等，venue 原生码原样存 status）
	SubType string `json:"subType"` // 细分
	From    string `json:"from"`    // 划转源账户码（划转行才有）
	To      string `json:"to"`      // 划转目的账户码
	Ts      string `json:"ts"`      // ms 字符串
}

// fetchOkxTransfer okx 资金账单划转行（from/to 非空=账户间移动；限 100 单页+
// 客户端水位过滤，同 fetchOkx 核证点①）。
func (c *CexIOCollector) fetchOkxTransfer(ctx context.Context, acc CexIOAccount, hasWM bool, watermark time.Time) ([]pgstore.VenueIO, error) {
	body, err := signedOkxGet(ctx, httpx.Client(), acc.BaseURL, "/api/v5/asset/bills?limit=100", acc.APIKey, acc.APISecret, acc.Passphrase)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Code string            `json:"code"`
		Data []okxAssetBillRow `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if resp.Code != "0" {
		return nil, fmt.Errorf("okx code=%s", resp.Code)
	}
	out := []pgstore.VenueIO{}
	for _, r := range resp.Data {
		if r.From == "" && r.To == "" {
			continue // 非账户间移动（充提/结息行各有专道）
		}
		if r.BillID == "" {
			continue // 无幂等键不猜
		}
		tsMS, perr := strconv.ParseInt(r.Ts, 10, 64)
		if perr != nil {
			continue
		}
		ts := time.UnixMilli(tsMS)
		if hasWM && !ts.After(watermark) {
			continue
		}
		amt, perr := f64z(r.BalChg)
		if perr != nil {
			return nil, fmt.Errorf("parse bills balChg %q: %w", r.BalChg, perr)
		}
		out = append(out, pgstore.VenueIO{
			Venue: "okx", ConfigID: acc.ID, Direction: "transfer",
			Ccy: r.Ccy, Amount: amt, Status: r.Type + "/" + r.SubType,
			ExternalID: r.BillID, Ts: ts,
			FromWallet: okxWalletOf(r.From), ToWallet: okxWalletOf(r.To),
		})
	}
	return out, nil
}
