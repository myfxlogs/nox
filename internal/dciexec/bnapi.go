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

// freeBalance 实时余额读（/v3/account 签名端点；free+locked；测试注 BalanceFn）。
func (e *Executor) freeBalance(ctx context.Context, asset string) (float64, error) {
	if e.BalanceFn != nil {
		return e.BalanceFn(ctx, asset)
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
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
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
