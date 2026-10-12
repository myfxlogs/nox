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

// dciSubscriptions 申购史全列表（签名 GET；SubscriptionsFn=测试注入面）。
// 死端切源：positions 端点随交割把已结行移出列表（生产 #7/#8 实证 row==nil
// 永跳不翻 settled），申购史端点保留终态行作确认源。
func (e *Executor) dciSubscriptions(ctx context.Context) ([]DCIPosition, error) {
	if e.SubscriptionsFn != nil {
		return e.SubscriptionsFn(ctx)
	}
	q := fmt.Sprintf("timestamp=%d&recvWindow=5000", e.Now().UnixMilli())
	url := fmt.Sprintf("%s/v1/dci/product/subscriptions?%s&signature=%s",
		strings.TrimRight(e.Sapi, "/"), q, sign(e.BnSec, q))
	b, err := e.get(ctx, url, true)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Data          []DCIPosition `json:"data"`
		List          []DCIPosition `json:"list"`
		Rows          []DCIPosition `json:"rows"`
		Subscriptions []DCIPosition `json:"subscriptions"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("dci subscriptions decode: %w", err)
	}
	for _, rows := range [][]DCIPosition{raw.Data, raw.List, raw.Rows, raw.Subscriptions} {
		if len(rows) > 0 {
			return rows, nil
		}
	}
	return nil, nil
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
