// cex_executor_gate_history.go —— D-577 S3：Gate 成交历史兜底读口（WS 帧缺口时 REST 查证）。
// 签名 GET 形态照 cex_executor_gate.go GateAccountBookFund 先例。
package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"arb/internal/platform/signutil"
)

// GateTradeHistory CEX 成交历史兜底读口（gate 专属可选接口，D-577）。
type GateTradeHistory interface {
	// QueryMyTrades 按 contract 查成交历史，过滤 create_time>=sinceUnix。
	QueryMyTrades(ctx context.Context, contract string, sinceUnix int64) ([]GateFill, error)
	// QueryPositionClose 按 contract 查平仓记录，返回最新一条（无=nil）。
	QueryPositionClose(ctx context.Context, contract string) (*GatePosClose, error)
}

// gateSignedGet 签名 GET（GateSign 第三段=原始 query string；镜像 GateAccountBookFund）。
func (e *CEXExecutor) gateSignedGet(ctx context.Context, endpoint, query string) ([]byte, error) {
	urlPath := "/api/v4" + endpoint
	ts := gateNow()
	sign := signutil.GateSign(e.apiSecret, "GET", urlPath, query, "", ts)
	headers := http.Header{
		"KEY":       {e.apiKey},
		"Timestamp": {strconv.FormatInt(ts, 10)},
		"SIGN":      {sign},
	}
	return e.get(ctx, e.baseURL+endpoint+"?"+query, headers)
}

// QueryMyTrades GET /futures/usdt/my_trades?contract= —— WS usertrades 帧缺口兜底。
func (e *CEXExecutor) QueryMyTrades(ctx context.Context, contract string, sinceUnix int64) ([]GateFill, error) {
	body, err := e.gateSignedGet(ctx, "/futures/usdt/my_trades", "contract="+contract+"&limit=100")
	if err != nil {
		return nil, fmt.Errorf("gate my_trades: %w", err)
	}
	var all []GateFill
	if err := json.Unmarshal(body, &all); err != nil {
		return nil, fmt.Errorf("gate my_trades json: %w", err)
	}
	var out []GateFill
	for _, f := range all {
		if f.Contract != contract || f.CreateTime < float64(sinceUnix) {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

// QueryPositionClose GET /futures/usdt/position_close?contract= —— 取 time 最新一条。
func (e *CEXExecutor) QueryPositionClose(ctx context.Context, contract string) (*GatePosClose, error) {
	body, err := e.gateSignedGet(ctx, "/futures/usdt/position_close", "contract="+contract)
	if err != nil {
		return nil, fmt.Errorf("gate position_close: %w", err)
	}
	var rows []GatePosClose
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("gate position_close json: %w", err)
	}
	var best *GatePosClose
	for i := range rows {
		if rows[i].Contract != contract {
			continue
		}
		if best == nil || rows[i].Time > best.Time {
			cp := rows[i]
			best = &cp
		}
	}
	return best, nil
}
