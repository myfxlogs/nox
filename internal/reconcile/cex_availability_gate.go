// cex_availability_gate.go —— D-547 S1：gate 钱包面三档（spot/futures/earn，同 cex_availability.go 分层模型）。
package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"arb/internal/signutil"
)

// === Gate ===

// availabilityGate 两钱包面：spot=/spot/accounts（available=立即可用，locked=锁定慢动）+
// futures=/futures/usdt/accounts（available=立即可用，total−available=保证金占用档）。
// gate earn/理财面官方文档无独立面=如实注记缺席（S0③核实结论）。
func (q *CEXAccountQuerier) availabilityGate(ctx context.Context) (*VenueAvailability, error) {
	out := &VenueAvailability{VenueKey: q.VenueKey()}
	sp, err := q.availabilityGateSpot(ctx)
	if err != nil {
		out.Nodes = append(out.Nodes, &AvailabilityNode{Wallet: "spot", Detail: "拉取失败: " + err.Error(), At: time.Now()})
	} else {
		out.Nodes = append(out.Nodes, sp)
	}
	fu, err := q.availabilityGateFutures(ctx)
	if err != nil {
		return nil, fmt.Errorf("gate availability futures: %w", err)
	}
	out.Nodes = append(out.Nodes, fu)
	if en, err := q.availabilityGateEarn(ctx); err != nil {
		out.Nodes = append(out.Nodes, &AvailabilityNode{Wallet: "earn", Detail: "拉取失败: " + err.Error(), At: time.Now()})
	} else {
		out.Nodes = append(out.Nodes, en)
	}
	return out, nil
}

// availabilityGateEarn /earn/uni/lends 理财面（官方 EarnUni 端点，D-547 S0③核实）：
// current_amount=出借中本金→锁定慢动（按时点赎回语义）；权限不足/无单如实注记。
func (q *CEXAccountQuerier) availabilityGateEarn(ctx context.Context) (*AvailabilityNode, error) {
	body, err := q.gateSignedGet(ctx, "/earn/uni/lends")
	if err != nil {
		return nil, fmt.Errorf("gate earn availability: %w", err)
	}
	var rows []struct {
		Currency string `json:"currency"`
		Amount   string `json:"current_amount"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("gate earn availability json: %w", err)
	}
	n := &AvailabilityNode{Wallet: "earn", At: time.Now()}
	for _, r := range rows {
		v, _ := strconv.ParseFloat(r.Amount, 64)
		if v <= 0 {
			continue
		}
		if r.Currency == "USDT" {
			n.LockedUSD += v
		} else {
			n.Detail += fmt.Sprintf("%s amt=%s(未折价); ", r.Currency, r.Amount)
		}
	}
	return n, nil
}

func (q *CEXAccountQuerier) gateSignedGet(ctx context.Context, endpoint string) ([]byte, error) {
	ts := time.Now().Unix()
	sign := signutil.GateSign(q.apiSecret, "GET", "/api/v4"+endpoint, "", "", ts)
	headers := http.Header{
		"KEY":       {q.apiKey},
		"Timestamp": {strconv.FormatInt(ts, 10)},
		"SIGN":      {sign},
	}
	return q.get(ctx, q.baseURL+endpoint, headers)
}

// availabilityGateSpot /spot/accounts 全币种（USDT 直计；非 USDT 经现货 ticker 折价）。
func (q *CEXAccountQuerier) availabilityGateSpot(ctx context.Context) (*AvailabilityNode, error) {
	body, err := q.gateSignedGet(ctx, "/spot/accounts")
	if err != nil {
		return nil, fmt.Errorf("gate spot availability: %w", err)
	}
	return q.gateSpotNode(ctx, body, q.gateSpotPrice)
}

// gateSpotNode /spot/accounts 响应→spot 面三档（折价回调注入，T4 fixture 锚点）。
func (q *CEXAccountQuerier) gateSpotNode(ctx context.Context, body []byte, priceOf func(context.Context, string) (float64, error)) (*AvailabilityNode, error) {
	var rows []struct {
		Currency  string `json:"currency"`
		Available string `json:"available"`
		Locked    string `json:"locked"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("gate spot availability json: %w", err)
	}
	n := &AvailabilityNode{Wallet: "spot", At: time.Now()}
	for _, r := range rows {
		av, _ := strconv.ParseFloat(r.Available, 64)
		lk, _ := strconv.ParseFloat(r.Locked, 64)
		if av == 0 && lk == 0 {
			continue
		}
		px := 1.0
		if r.Currency != "USDT" {
			p, perr := priceOf(ctx, r.Currency)
			if perr != nil {
				n.Detail += fmt.Sprintf("%s av=%s lk=%s(未折价); ", r.Currency, r.Available, r.Locked)
				continue
			}
			px = p
		}
		n.AvailableUSD += av * px
		n.LockedUSD += lk * px
	}
	return n, nil
}

// availabilityGateFutures /futures/usdt/accounts（available=立即可用；
// total−available=position_margin+order_margin 保证金占用→锁定慢动）。
func (q *CEXAccountQuerier) availabilityGateFutures(ctx context.Context) (*AvailabilityNode, error) {
	body, err := q.gateSignedGet(ctx, "/futures/usdt/accounts")
	if err != nil {
		return nil, fmt.Errorf("gate futures availability: %w", err)
	}
	return gateFuturesNode(body)
}

// gateFuturesNode /futures/usdt/accounts 响应→futures 面三档（纯函数，T4 fixture 锚点）。
func gateFuturesNode(body []byte) (*AvailabilityNode, error) {
	var acc struct {
		Total          string `json:"total"`
		Available      string `json:"available"`
		PositionMargin string `json:"position_margin"`
		OrderMargin    string `json:"order_margin"`
	}
	if err := json.Unmarshal(body, &acc); err != nil {
		return nil, fmt.Errorf("gate futures availability json: %w", err)
	}
	av, _ := strconv.ParseFloat(acc.Available, 64)
	pm, _ := strconv.ParseFloat(acc.PositionMargin, 64)
	om, _ := strconv.ParseFloat(acc.OrderMargin, 64)
	return &AvailabilityNode{
		Wallet: "futures", At: time.Now(),
		AvailableUSD: av, LockedUSD: pm + om,
		Detail: fmt.Sprintf("total=%s available=%s position_margin=%s order_margin=%s",
			acc.Total, acc.Available, acc.PositionMargin, acc.OrderMargin),
	}, nil
}

// gateSpotPrice 公开 ticker 折价（currency→USDT；无对=err 不猜价）。
func (q *CEXAccountQuerier) gateSpotPrice(ctx context.Context, ccy string) (float64, error) {
	body, err := q.get(ctx, q.baseURL+"/spot/currency_pairs/"+ccy+"_USDT", nil)
	if err != nil {
		return 0, err
	}
	var t struct {
		Last string `json:"last"`
	}
	if err := json.Unmarshal(body, &t); err != nil {
		return 0, err
	}
	p, err := strconv.ParseFloat(t.Last, 64)
	if err != nil || p <= 0 {
		return 0, fmt.Errorf("gate ticker %s_USDT last=%q", ccy, t.Last)
	}
	return p, nil
}
