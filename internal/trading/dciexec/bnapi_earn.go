// bnapi_earn.go —— D-191/D-203：earn 活期子账读径（自 bnapi.go 拆分，纯移动零语义改）。
package dciexec

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type EarnPosition struct {
	ProductID string
	Amount    float64
	Locked    bool // locked=定期（不可 FAST 赎回）
	// MinRedeem venue 最小赎回额（flexible/position 行 minAmount 键，
	// 实拍待证——键缺/零=venue 未给证据按需求额过，禁硬编底数）。
	MinRedeem float64
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
				MinAmount   string `json:"minAmount"`
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
			if amt == "" {
				continue
			}
			f, perr := strconv.ParseFloat(amt, 64)
			if perr != nil {
				return nil, fmt.Errorf("%s %s 持仓金额解析败 %q: %w", seg.path, asset, amt, perr)
			}
			var minR float64
			if row.MinAmount != "" {
				if m, merr := strconv.ParseFloat(row.MinAmount, 64); merr == nil {
					minR = m
				}
			}
			if f > 0 {
				out = append(out, EarnPosition{
					ProductID: row.ProductID, Amount: f, Locked: seg.locked, MinRedeem: minR})
			}
		}
	}
	return out, nil
}

// redeemFlexible 活期赎回步：POST /v1/simple-earn/flexible/redeem（签名；
// 撤 type=FAST——官方参数表无此参（lending 家族残留）。
func (e *Executor) redeemFlexible(ctx context.Context, productID string, amount float64) error {
	q := fmt.Sprintf("productId=%s&amount=%.8f&timestamp=%d&recvWindow=5000",
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
