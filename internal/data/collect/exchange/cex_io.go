// cex_io.go —— D-365 S3：CEX 进出账采集器（充值/提现，binance+okx real 配置）。
// 结构同 BinanceIncomeCollector（accounts 函数供给+水位增量+幂等落库）。
// 核证点②：binance /sapi 历史窗口上限 90 天——首拉 startTime=now-89d，之后水位+1ms。
// 核证点③：status 原样存（binance deposit 0=pending/1=success/6=credited且余额快照后入账/
// 7=wrongdeposit/8=waitingUserConfirm；withdraw 0=email sent/1=cancelled/2=awaiting approval/
// 3=rejected/4=processing/5=failure/6=completed；okx deposit state 0=等待确认/1=到账展示/
// 2=入账成功；withdraw state -3=撤销/-2=失败/-1=拦截/0=未放行/1=待审核/2=审核中?/3=待转账/
// 4=转账中/7=完成——以实采值+官方文档双验，不强行翻译）。
// 路由核证点④（D-363 同源实探 9-20）：binance sapi 走代理根路径（/api 前缀 404）；
// okx /api/v5/asset/* 代理 403 → 直连 www.okx.com（wiring 供 BaseURL 时定死）。
package exchange

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"nox/internal/store/pgstore"
)

// okxHMACBase64 okx 签名：base64(HMAC-SHA256(secret, ts+method+path+query))（reconcile
// okxSign 同法；各包独立实现先例）。
func okxHMACBase64(secret, prehash string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(prehash))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// CexIOAccount 进出账采集账户（wiring 从 exchange_configs real 行逐轮供给）。
type CexIOAccount struct {
	ID         int64
	Venue      string // binance / okx
	APIKey     string
	APISecret  string
	Passphrase string // okx
	BaseURL    string // binance=config 原值（/api 后缀采集器内派生 /sapi）；okx=直连基址
}

// FilterIOAccounts D-365 S3/R1：exchange_configs → 进出账采集账户列表（纯函数可测）。
// 只收 venue∈{binance,okx} 且 !simulated 且有凭据的配置（real-only 姿态；gate 仅 sim 跳过）；
// R1：(venue,api_key) 去重——同凭据多 config（spot/perp/funding/earn 同账号）只采一次，
// 归属 canonical=min(id)（binance 3→1、okx 4→1；不同 key 仍各采）——进出账是账号级事实，
// 重复采集只会撞幂等键浪费调用。okx base_url 取 DB 配置（现值=cex-proxy，D-396），空值才兜底直连 www.okx.com。
func FilterIOAccounts(cfgs []pgstore.ExchangeConfig) []CexIOAccount {
	pick := map[string]pgstore.ExchangeConfig{} // (venue,api_key) → canonical（min id）
	for _, c := range cfgs {
		if c.Simulated || (c.Venue != "binance" && c.Venue != "okx") || c.APIKey == "" || c.APISecret == "" {
			continue
		}
		dedupKey := c.Venue + ":" + c.APIKey
		if prev, ok := pick[dedupKey]; !ok || c.ID < prev.ID {
			pick[dedupKey] = c // canonical=min(id)，不依赖输入序
		}
	}
	out := make([]CexIOAccount, 0, len(pick))
	for _, c := range pick {
		base := c.BaseURL
		if c.Venue == "okx" && base == "" {
			base = "https://www.okx.com"
		}
		out = append(out, CexIOAccount{
			ID: c.ID, Venue: c.Venue, APIKey: c.APIKey, APISecret: c.APISecret,
			Passphrase: c.Passphrase, BaseURL: base,
		})
	}
	return out
}

// VenueIOSink 进出账落库/水位依赖（pgstore.Store 实现；单测 fake）。
type VenueIOSink interface {
	InsertVenueIO(ctx context.Context, rows []pgstore.VenueIO) (int64, error)
	LatestVenueIOTs(ctx context.Context, configID int64, direction string) (time.Time, bool, error)
}

// CexIOCollector 进出账采集器（15min 周期，水位增量+首拉 90d 回填）。
type CexIOCollector struct {
	sink     VenueIOSink
	accounts func() []CexIOAccount
	now      func() time.Time // 注入时钟（测试锚）
}

// NewCexIOCollector 构造。
func NewCexIOCollector(sink VenueIOSink, accounts func() []CexIOAccount) *CexIOCollector {
	return &CexIOCollector{sink: sink, accounts: accounts, now: time.Now}
}

// cexIODepositRow binance /sapi/v1/capital/deposit/hisrec 行。
type cexIODepositRow struct {
	ID           string `json:"id"`
	Amount       string `json:"amount"`
	Coin         string `json:"coin"`
	Network      string `json:"network"`
	Status       int    `json:"status"`
	TxID         string `json:"txId"`
	InsertTime   int64  `json:"insertTime"` // ms
	CompleteTime int64  `json:"completeTime"`
}

// cexIOWithdrawRow binance /sapi/v1/capital/withdraw/history 行。
type cexIOWithdrawRow struct {
	ID             string `json:"id"`
	Amount         string `json:"amount"`
	TransactionFee string `json:"transactionFee"`
	Coin           string `json:"coin"`
	Status         int    `json:"status"`
	TxID           string `json:"txId"`
	ApplyTime      string `json:"applyTime"` // D-365-R5："2006-01-02 15:04:05" UTC 字符串（真探实证非 ms int64）
	Network        string `json:"network"`
}

// cexIOOkxRow okx /asset/deposit-history 与 /withdrawal-history 共形行
// （核证点④：提现 id 字段名=wdId，9-20 真探实证）。
type cexIOOkxRow struct {
	DepID string `json:"depId"`
	WdID  string `json:"wdId"`
	Amt   string `json:"amt"`
	Ccy   string `json:"ccy"`
	Chain string `json:"chain"`
	Fee   string `json:"fee"`
	State string `json:"state"`
	Ts    string `json:"ts"` // ms 字符串
	TxID  string `json:"txId"`
}

// signedBinanceGet binance SAPI 签名 GET（HMAC-SHA256 query 签名，走代理）。
func signedBinanceGet(ctx context.Context, httpClient *http.Client, base, path, apiKey, apiSecret string, extra url.Values) ([]byte, error) {
	params := url.Values{}
	params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	params.Set("recvWindow", "5000")
	for k, vs := range extra {
		for _, v := range vs {
			params.Add(k, v)
		}
	}
	params.Set("signature", binanceIncomeSign(apiSecret, params.Encode()))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-MBX-APIKEY", apiKey)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// signedOkxGet okx 资金域签名 GET（OK-ACCESS 四头，直连基址）。
func signedOkxGet(ctx context.Context, httpClient *http.Client, base, pathWithQuery, apiKey, apiSecret, passphrase string) ([]byte, error) {
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	sign := okxHMACBase64(apiSecret, ts+"GET"+pathWithQuery)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+pathWithQuery, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("OK-ACCESS-KEY", apiKey)
	req.Header.Set("OK-ACCESS-SIGN", sign)
	req.Header.Set("OK-ACCESS-TIMESTAMP", ts)
	req.Header.Set("OK-ACCESS-PASSPHRASE", passphrase)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// RunOnce 对全部 real 配置拉 deposit+withdraw 两向（水位增量；首拉 90d 回填）。
// 返回实插入行数。单配置失败记日志继续（同 income 惯例）。
func (c *CexIOCollector) RunOnce(ctx context.Context) (int64, error) {
	var written int64
	if c.accounts == nil {
		return 0, nil
	}
	for _, acc := range c.accounts() {
		for _, dir := range []string{"deposit", "withdraw", "transfer"} {
			rows, err := c.fetchDirection(ctx, acc, dir)
			if err != nil {
				fmt.Printf("[cex-io] %s #%d %s: %v (skip)\n", acc.Venue, acc.ID, dir, err)
				continue
			}
			n, err := c.sink.InsertVenueIO(ctx, rows)
			if err != nil {
				fmt.Printf("[cex-io] %s #%d %s insert: %v (skip)\n", acc.Venue, acc.ID, dir, err)
				continue
			}
			written += n
		}
	}
	return written, nil
}
