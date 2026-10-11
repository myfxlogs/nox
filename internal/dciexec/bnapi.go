// bnapi.go —— D-191 R3：binance REST 读/写面（实时现货、签名余额、申购 POST）。
package dciexec

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

	"arb/internal/bus"
	"arb/internal/dephealth"
	"arb/internal/store/pgstore"
)

// spotPrice 实时现货读（/v3/ticker/price 公开端点；base 含 /api 段从 /vN 起拼——earn.go 约定；测试注 PriceFn）。
func (e *Executor) spotPrice(ctx context.Context, symbol string) (float64, error) {
	if e.PriceFn != nil {
		return e.PriceFn(ctx, symbol)
	}
	url := fmt.Sprintf("%s/v3/ticker/price?symbol=%s",
		strings.TrimRight(e.APIBase, "/"), symbol)
	b, err := e.get(ctx, url, false)
	if err != nil {
		return 0, err
	}
	var r struct {
		Price string `json:"price"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return 0, err
	}
	return strconv.ParseFloat(r.Price, 64)
}

// freeBalance 实时余额读（D-192：私有流资产级活缓存主源，闸=dephealth 流连接
// 存活标非事件新旧；死流/无值→REST /v3/account 兜底；边界=现货域，earn 仍 R6 读；
// free+locked；测试注 BalanceFn/StreamFn）。
func (e *Executor) freeBalance(ctx context.Context, asset string) (float64, error) {
	if e.BalanceFn != nil {
		return e.BalanceFn(ctx, asset)
	}
	if e.streamAlive() {
		if v, ok := e.streamBal(asset); ok {
			return v, nil
		}
	}
	q := fmt.Sprintf("timestamp=%d&recvWindow=5000", e.Now().UnixMilli())
	url := fmt.Sprintf("%s/v3/account?%s&signature=%s",
		strings.TrimRight(e.APIBase, "/"), q, sign(e.BnSec, q))
	b, err := e.get(ctx, url, true)
	if err != nil {
		return 0, err
	}
	var r struct {
		Balances []struct {
			Asset  string `json:"asset"`
			Free   string `json:"free"`
			Locked string `json:"locked"`
		} `json:"balances"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return 0, err
	}
	for _, bal := range r.Balances {
		if bal.Asset == asset { // free+locked：DCI 申购锁定币仍在池内（漂移误警防线）
			free, _ := strconv.ParseFloat(bal.Free, 64)
			locked, _ := strconv.ParseFloat(bal.Locked, 64)
			return free + locked, nil
		}
	}
	return 0, nil
}

func (e *Executor) get(ctx context.Context, url string, signed bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if signed {
		req.Header.Set("X-MBX-APIKEY", e.BnKey)
	}
	resp, err := e.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // R5：/v3/account 大 JSON 防截断（post 侧 4096 保留）
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	return b, nil
}

func (e *Executor) post(ctx context.Context, projectID string, lots float64, at time.Time) (string, error) {
	_, url := e.SubscribeBody(projectID, lots, at)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-MBX-APIKEY", e.BnKey)
	resp, err := e.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("subscribe HTTP %d: %s", resp.StatusCode, string(b))
	}
	return string(b), nil
}

func (e *Executor) client() *http.Client {
	if e.Client != nil {
		return e.Client
	}
	return http.DefaultClient
}

func (e *Executor) finish(ctx context.Context, id int64, st, resp string) {
	if err := e.Store.FinishDCIOrder(ctx, id, st, resp); err != nil {
		log.Printf("[dciexec] finish order %d: %v", id, err)
	}
}

// gap 缺口落+请求（D-186 通道复用；err 回执即缺口文案）。
func (e *Executor) gap(ctx context.Context, l leg, kind, subject, format string, args ...any) error {
	text := fmt.Sprintf(format, args...)
	id, err := e.Store.OpenConditionGap(ctx, &pgstore.ConditionGap{
		TemplateID: l.tmpl, Kind: kind, Subject: subject, RequestText: text})
	if err != nil {
		return err
	}
	if err := e.Store.RequestConditionGap(ctx, id); err != nil {
		return err
	}
	return fmt.Errorf("条件缺口：%s", text)
}

// EarnPosition 赚币子账持仓行（flexible/locked position 读径归并）。
type EarnPosition struct {
	ProductID string
	Amount    float64
	Locked    bool // locked=定期（不可 FAST 赎回）
}

// earnPositions 赚币子账持仓读：GET /v1/simple-earn/{flexible,locked}/position
// （签名；rows[].totalAmount|amount 归并——R6 池口径 spot+earn）。
func (e *Executor) earnPositions(ctx context.Context, asset string) ([]EarnPosition, error) {
	var out []EarnPosition
	for _, seg := range []struct {
		path   string
		locked bool
	}{
		{"/v1/simple-earn/flexible/position", false},
		{"/v1/simple-earn/locked/position", true},
	} {
		q := fmt.Sprintf("asset=%s&timestamp=%d&recvWindow=5000", asset, e.Now().UnixMilli())
		url := fmt.Sprintf("%s%s?%s&signature=%s",
			strings.TrimRight(e.Sapi, "/"), seg.path, q, sign(e.BnSec, q))
		b, err := e.get(ctx, url, true)
		if err != nil {
			return nil, err
		}
		var r struct {
			Rows []struct {
				ProductID   string `json:"productId"`
				TotalAmount string `json:"totalAmount"`
				Amount      string `json:"amount"`
			} `json:"rows"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		for _, row := range r.Rows {
			amt := row.TotalAmount
			if amt == "" {
				amt = row.Amount
			}
			f, _ := strconv.ParseFloat(amt, 64)
			if f > 0 {
				out = append(out, EarnPosition{ProductID: row.ProductID, Amount: f, Locked: seg.locked})
			}
		}
	}
	return out, nil
}

// redeemFlexible 活期赎回步：POST /v1/simple-earn/flexible/redeem（签名；
// type=FAST 赎至现货——R6 申购前置「spot 不足→赎回缺口→重读」）。
func (e *Executor) redeemFlexible(ctx context.Context, productID string, amount float64) error {
	q := fmt.Sprintf("productId=%s&amount=%.8f&type=FAST&timestamp=%d&recvWindow=5000",
		productID, amount, e.Now().UnixMilli())
	url := fmt.Sprintf("%s/v1/simple-earn/flexible/redeem?%s&signature=%s",
		strings.TrimRight(e.Sapi, "/"), q, sign(e.BnSec, q))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-MBX-APIKEY", e.BnKey)
	resp, err := e.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("redeem HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// streamAlive 流连接存活闸（binance spot 用户流 dephealth 键；测试注 StreamFn 绕开）。
func (e *Executor) streamAlive() bool {
	if e.StreamFn != nil {
		return e.StreamFn()
	}
	return !dephealth.Degraded("binance:ws-spot-user")
}

// streamBal 流缓存读（bus.SpotAssets；资产无值→false→REST 兜底）。
func (e *Executor) streamBal(asset string) (float64, bool) {
	b, ok := bus.SpotAssets.Get("binance:spot", asset)
	if !ok {
		return 0, false
	}
	return b.Free + b.Locked, true
}
