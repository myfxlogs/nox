// settle_venue.go —— DCI 交割观察的 venue 读取面（自 settle.go 拆出控行数）：
// positions 持仓全列表 / subscriptions 申购史兜底 / settleDate 解析。
package dciexec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// dciPositions venue 持仓全列表（签名 GET；PositionsFn=测试注入面）。
func (e *Executor) dciPositions(ctx context.Context) ([]DCIPosition, error) {
	if e.PositionsFn != nil {
		return e.PositionsFn(ctx)
	}
	q := fmt.Sprintf("timestamp=%d&recvWindow=5000", e.Now().UnixMilli())
	url := fmt.Sprintf("%s/v1/dci/product/positions?%s&signature=%s",
		strings.TrimRight(e.Sapi, "/"), q, sign(e.BnSec, q))
	b, err := e.get(ctx, url, true)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Data []DCIPosition `json:"data"`
		List []DCIPosition `json:"list"`
		Rows []DCIPosition `json:"rows"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("dci positions decode: %w", err)
	}
	rows := raw.Data
	if len(rows) == 0 {
		rows = raw.List
	}
	if len(rows) == 0 {
		rows = raw.Rows
	}
	return rows, nil
}

// dciSettledPositions 交割终态行全列表（签名 GET；SettledPositionsFn=测试注入面）。
// 死端切源：positions 端点随交割把已结行移出默认列表（生产 #7 实证 row==nil
// 永跳不翻 settled）；status=SETTLED 过滤显式取终态行（Binance 无 subscriptions
// 端点——M3 切流实拍 404→改证此径，订单 response.positionId 精确对拍）。
func (e *Executor) dciSettledPositions(ctx context.Context) ([]DCIPosition, error) {
	if e.SettledPositionsFn != nil {
		return e.SettledPositionsFn(ctx)
	}
	q := fmt.Sprintf("status=SETTLED&timestamp=%d&recvWindow=5000", e.Now().UnixMilli())
	url := fmt.Sprintf("%s/v1/dci/product/positions?%s&signature=%s",
		strings.TrimRight(e.Sapi, "/"), q, sign(e.BnSec, q))
	b, err := e.get(ctx, url, true)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Data []DCIPosition `json:"data"`
		List []DCIPosition `json:"list"`
		Rows []DCIPosition `json:"rows"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("dci settled positions decode: %w", err)
	}
	for _, rows := range [][]DCIPosition{raw.Data, raw.List, raw.Rows} {
		if len(rows) > 0 {
			return rows, nil
		}
	}
	return nil, nil
}

// positionIDOf 订单 response.positionId 解析——venue 仓位行 id 的真值键
// （product_ref 是产品号非仓位号；M3 切流实拍 order#7 response.positionId=
// 18537557 恰与 SETTLED 行 id 对拍命中）。数字/数字串双形，缺/解析败=空如实。
func positionIDOf(resp string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(resp), &m); err != nil {
		return ""
	}
	raw, ok := m["positionId"]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String()
	}
	return ""
}

// settleDateOf response.settleDate 解析（回执原值不估算——ms epoch 或数字串；
// venue 异名键不猜，缺/解析败=false）。
func settleDateOf(resp string) (time.Time, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(resp), &m); err != nil {
		return time.Time{}, false
	}
	raw, ok := m["settleDate"]
	if !ok {
		return time.Time{}, false
	}
	var ms int64
	if err := json.Unmarshal(raw, &ms); err != nil {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return time.Time{}, false
		}
		if _, err := fmt.Sscanf(s, "%d", &ms); err != nil {
			return time.Time{}, false
		}
	}
	if ms <= 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(ms), true
}
