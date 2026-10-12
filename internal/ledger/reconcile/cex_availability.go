// cex_availability.go —— D-547 S1：钱包树可用性三档分层（D-546 同模型共享骨架，venue 参数化）。
// 档语义钉死对齐 D-546 §2：立即可用=T+0 可开仓 / 可转可用=赎回/划转即达 / 锁定慢动=冻结/定期/保证金占用。
// 复用 CEXAccountQuerier 签名/代理链（q.get/okxSubaccountSignedGet/signutil.GateSign），禁新客户端。
// 采集节奏=页面触发+进程缓存（消费侧 60s 外壳，本文件纯拉取不含缓存）。
package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// AvailabilityNode 单钱包面三档值（USD 折值；非 USDT 明细经 venue 指数价折算，折价注记入 Note）。
type AvailabilityNode struct {
	Wallet          string  // trading|funding|earn|spot|futures
	AvailableUSD    float64 // 立即可用（T+0 可开仓）
	TransferableUSD float64 // 可转可用（赎回/划转即达）
	LockedUSD       float64 // 锁定慢动（冻结/定期/保证金占用）
	Detail          string  // 字段级注记（原始字段名→值，审计用）
	At              time.Time
}

// VenueAvailability venue 全钱包树（okx=trading+funding+earn；gate=spot+futures）。
type VenueAvailability struct {
	VenueKey string
	Nodes    []*AvailabilityNode
}

// Availability 按 venue 分派钱包树三档采集（binance=D-546 域本票不建）。
func (q *CEXAccountQuerier) Availability(ctx context.Context) (*VenueAvailability, error) {
	switch q.venue {
	case "okx":
		return q.availabilityOKX(ctx)
	case "gate":
		return q.availabilityGate(ctx)
	default:
		return nil, fmt.Errorf("availability: unsupported venue %q", q.venue)
	}
}

// === OKX ===

// availabilityOKX 三钱包面：trading=/account/balance details availEq(立即可用)+
// availBal/frozenBal(锁定慢动)；funding=/asset/balances(可转可用)；earn=savings(可转可用)。
func (q *CEXAccountQuerier) availabilityOKX(ctx context.Context) (*VenueAvailability, error) {
	out := &VenueAvailability{VenueKey: q.VenueKey()}
	tr, err := q.availabilityOKXTrading(ctx)
	if err != nil {
		return nil, err
	}
	out.Nodes = append(out.Nodes, tr)
	for _, src := range []string{"funding", "earn"} {
		n, err := q.availabilityOKXSub(ctx, src)
		if err != nil {
			// 子面败不拖垮整树——Node 降级注记如实透出。
			out.Nodes = append(out.Nodes, &AvailabilityNode{Wallet: src, Detail: "拉取失败: " + err.Error(), At: time.Now()})
			continue
		}
		out.Nodes = append(out.Nodes, n)
	}
	return out, nil
}

// availabilityOKXTrading /account/balance：availEq=立即可用（可直接用保证金），
// availBal−availEq+frozenBal 归锁定慢动口径（冻结/占用——无独立「可转」语义）。
// 非 USDT 币种按 eqUsd/eq 比值折算各字段 USD（同源比例不外引行情）。
func (q *CEXAccountQuerier) availabilityOKXTrading(ctx context.Context) (*AvailabilityNode, error) {
	const path = "/api/v5/account/balance"
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	sign := okxSign(q.apiSecret, ts+"GET"+path)
	headers := http.Header{
		"OK-ACCESS-KEY":        {q.apiKey},
		"OK-ACCESS-SIGN":       {sign},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {q.passphrase},
		"Content-Type":         {"application/json"},
	}
	body, err := q.get(ctx, q.baseURL+path, headers)
	if err != nil {
		return nil, fmt.Errorf("okx availability trading: %w", err)
	}
	return okxTradingNode(body)
}

// okxTradingNode /account/balance 响应→trading 面三档（纯函数，T4 fixture 锚点）。
func okxTradingNode(body []byte) (*AvailabilityNode, error) {
	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			Details []struct {
				Ccy       string `json:"ccy"`
				Eq        string `json:"eq"`
				EqUsd     string `json:"eqUsd"`
				AvailEq   string `json:"availEq"`
				AvailBal  string `json:"availBal"`
				FrozenBal string `json:"frozenBal"`
			} `json:"details"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("okx availability trading json: %w", err)
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		return nil, fmt.Errorf("okx availability trading: code=%s msg=%s", resp.Code, resp.Msg)
	}
	n := &AvailabilityNode{Wallet: "trading", At: time.Now()}
	for _, d := range resp.Data[0].Details {
		eqUsd, _ := strconv.ParseFloat(d.EqUsd, 64)
		if eqUsd <= 0 {
			continue
		}
		eq, _ := strconv.ParseFloat(d.Eq, 64)
		ratio := 1.0
		if d.Ccy != "USDT" && eq > 0 {
			ratio = eqUsd / eq // 同字段比折算（不引外部行情）
		}
		availEq, _ := strconv.ParseFloat(d.AvailEq, 64)
		availBal, _ := strconv.ParseFloat(d.AvailBal, 64)
		frozen, _ := strconv.ParseFloat(d.FrozenBal, 64)
		n.AvailableUSD += availEq * ratio
		// availBal−availEq=保证金占用、frozenBal=冻结——均归锁定慢动档。
		if lk := availBal - availEq + frozen; lk > 0 {
			n.LockedUSD += lk * ratio
		}
		n.Detail += fmt.Sprintf("%s availEq=%s availBal=%s frozen=%s; ", d.Ccy, d.AvailEq, d.AvailBal, d.FrozenBal)
	}
	return n, nil
}

// availabilityOKXSub 子面（funding/earn）全值归可转可用（资产↔交易划转/活期赎回 T+0）。
func (q *CEXAccountQuerier) availabilityOKXSub(ctx context.Context, source string) (*AvailabilityNode, error) {
	var path, field string
	switch source {
	case "funding":
		path, field = "/api/v5/asset/balances", "availBal"
	case "earn":
		path, field = "/api/v5/finance/savings/balance", "amt"
	default:
		return nil, fmt.Errorf("bad okx source %q", source)
	}
	body, err := q.okxSubaccountSignedGet(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("okx %s availability: %w", source, err)
	}
	return q.okxSubNode(ctx, source, field, body)
}

// okxSubNode 子面响应→可转可用档（纯解析+折价回调注入，T4 fixture 锚点）。
func (q *CEXAccountQuerier) okxSubNode(ctx context.Context, source, field string, body []byte) (*AvailabilityNode, error) {
	var resp struct {
		Code string              `json:"code"`
		Msg  string              `json:"msg"`
		Data []map[string]string `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("okx %s availability json: %w", source, err)
	}
	if resp.Code != "0" {
		return nil, fmt.Errorf("okx %s: code=%s msg=%s", source, resp.Code, resp.Msg)
	}
	n := &AvailabilityNode{Wallet: source, At: time.Now()}
	for _, d := range resp.Data {
		ccy := d["ccy"]
		v, perr := strconv.ParseFloat(d[field], 64)
		if perr != nil || v <= 0 {
			continue
		}
		if ccy == "USDT" {
			n.TransferableUSD += v
		} else if px, perr := q.okxIndexPrice(ctx, ccy); perr == nil {
			n.TransferableUSD += v * px
		} else {
			n.Detail += fmt.Sprintf("%s %s=%s(未折价); ", ccy, field, d[field])
		}
	}
	return n, nil
}
