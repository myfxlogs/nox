// CMC WebSocket 辅助函数（D-225 v2.1，从 ws.go 拆出控行数 <800）。
package cmc

import (
	"encoding/json"
	"fmt"
	"strings"
)

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// jsonEqual 浅比较两个 JSON RawMessage（规范化后比较）。
func jsonEqual(a, b json.RawMessage) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	var va, vb any
	if json.Unmarshal(a, &va) != nil || json.Unmarshal(b, &vb) != nil {
		return false
	}
	return fmt.Sprintf("%v", va) == fmt.Sprintf("%v", vb)
}

// AlertsToState D-225 v2.1：alerts JSON → 平台预警状态（normal/warning/breach）。
// alerts 空=normal；含 In*Warning=warning；含 In*Breach=breach。
func AlertsToState(alerts json.RawMessage) string {
	if len(alerts) == 0 {
		return "normal"
	}
	var items []accountValueAlertJSON
	if err := json.Unmarshal(alerts, &items); err != nil {
		return "normal"
	}
	if len(items) == 0 {
		return "normal"
	}
	hasWarning, hasBreach := false, false
	for _, a := range items {
		if strings.Contains(a.AlertType, "Breach") {
			hasBreach = true
		} else if strings.Contains(a.AlertType, "Warning") {
			hasWarning = true
		}
	}
	if hasBreach {
		return "breach"
	}
	if hasWarning {
		return "warning"
	}
	return "normal"
}

// mergePositionFields 将 patch 字段 merge 到 positionJSON（t=="m" 逐字段更新）。
func mergePositionFields(pos *positionJSON, d map[string]json.RawMessage) {
	fields := map[string]*string{
		"instrumentId": &pos.InstrumentID, "direction": &pos.Direction, "quantity": &pos.Quantity,
		"price": &pos.Price, "standardMarginAmount": &pos.StandardMarginAmount, "marginAmount": &pos.MarginAmount,
		"standardMarginPercentage":       &pos.StandardMarginPercentage,
		"unrealisedPnLInPrimaryCurrency": &pos.UnrealisedPnLInPrimaryCurrency,
		"evaluationBidPrice":             &pos.EvaluationBidPrice,
		"evaluationAskPrice":             &pos.EvaluationAskPrice,
	}
	for k, v := range d {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			continue
		}
		if ptr, ok := fields[k]; ok {
			*ptr = s
		}
	}
}

// mergeOrderFields 将 patch 字段 merge 到 orderJSON。
func mergeOrderFields(ord *orderJSON, d map[string]json.RawMessage) {
	fields := map[string]*string{
		"orderId":            &ord.OrderID,
		"filledPrice":        &ord.FilledPrice,
		"submittedTimestamp": &ord.SubmittedTimestamp,
	}
	for k, v := range d {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			continue
		}
		if ptr, ok := fields[k]; ok {
			*ptr = s
		}
	}
}
