// cex_executor_gate_position.go —— D-564 S2/S3：gate 持仓读与平仓域
// （自 cex_executor_gate.go 拆出控行数 <450；dual 路由/auto_size 平仓语义注释随件）。
package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"arb/internal/signutil"
)

// GatePositionsRaw 同 GatePositions，额外返回 GET 原始响应 body（D-098 返修② 探针形状证据：
// 一次运行定案 funding 字段是否真实存在于 positions 响应 JSON，与 account_book 双保险对拍）。
func (e *CEXExecutor) GatePositionsRaw(ctx context.Context, contract string) ([]byte, []GatePosition, error) {
	// D-564 S2 U3+U5：模式感知路由——dual/dual_plus 走 /dual_comp/positions/{c}
	//（[]Position 双侧，mode=dual_long/dual_short 各带 lever/pos_margin_mode/funding）；
	// single 走旧单仓端点。position_mode 每次实读 accounts（禁缓存——app 可切模式，
	// 缓存=快照进决策违禁）。POSITION_NOT_FOUND（flat 实测）→ 空切片不误报。
	mode, err := e.gatePositionModeFetch(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("gate positions: %w", err)
	}
	if mode.isDual() {
		return e.gateDualCompPositionsRaw(ctx, contract)
	}
	endpoint := "/futures/usdt/positions/" + contract
	urlPath := "/api/v4" + endpoint
	ts := time.Now().Unix()
	sign := signutil.GateSign(e.apiSecret, "GET", urlPath, "", "", ts)
	headers := http.Header{
		"KEY":       {e.apiKey},
		"Timestamp": {strconv.FormatInt(ts, 10)},
		"SIGN":      {sign},
	}
	body, err := e.get(ctx, e.baseURL+endpoint, headers)
	if err != nil {
		if strings.Contains(err.Error(), "Position not found") || strings.Contains(err.Error(), "POSITION_NOT_FOUND") {
			return body, nil, nil // flat 实测：单仓端点无仓亦同语义
		}
		return nil, nil, fmt.Errorf("gate positions: %w", err)
	}
	var p struct {
		Contract   string `json:"contract"`
		Size       int64  `json:"size"`
		EntryPrice string `json:"entry_price"`
		MarkPrice  string `json:"mark_price"`
		Funding    string `json:"funding"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return body, nil, fmt.Errorf("gate positions json: %w", err)
	}
	if p.Size == 0 {
		return body, nil, nil // 无仓不误报
	}
	ep, err := f64z(p.EntryPrice)
	if err != nil {
		return body, nil, fmt.Errorf("gate position %s entry_price=%q: %w", p.Contract, p.EntryPrice, err)
	}
	mp, err := f64z(p.MarkPrice)
	if err != nil {
		return body, nil, fmt.Errorf("gate position %s mark_price=%q: %w", p.Contract, p.MarkPrice, err)
	}
	fd, err := f64z(p.Funding)
	if err != nil {
		return body, nil, fmt.Errorf("gate position %s funding=%q: %w", p.Contract, p.Funding, err)
	}
	return body, []GatePosition{{
		Contract: p.Contract, Size: p.Size,
		EntryPrice: ep, MarkPrice: mp, Funding: fd,
	}}, nil
}

// gateDualCompPositionsRaw D-564 S2：dual 模式持仓读——GET /dual_comp/positions/{c}
// 返回 []Position 双侧（mode=dual_long/dual_short 各带 funding/realised_pnl——D-098
// 对拍字段族同有）。flatten 为 []GatePosition（size 符号/mode 归属以实返为准）；
// POSITION_NOT_FOUND → 空。
func (e *CEXExecutor) gateDualCompPositionsRaw(ctx context.Context, contract string) ([]byte, []GatePosition, error) {
	endpoint := "/futures/usdt/dual_comp/positions/" + contract
	urlPath := "/api/v4" + endpoint
	ts := time.Now().Unix()
	sign := signutil.GateSign(e.apiSecret, "GET", urlPath, "", "", ts)
	body, err := e.get(ctx, e.baseURL+endpoint, http.Header{
		"KEY": {e.apiKey}, "Timestamp": {strconv.FormatInt(ts, 10)}, "SIGN": {sign},
	})
	if err != nil {
		if strings.Contains(err.Error(), "Position not found") || strings.Contains(err.Error(), "POSITION_NOT_FOUND") {
			return body, nil, nil // flat 实测（404 语义）→ 空切片不误报
		}
		return nil, nil, fmt.Errorf("gate dual_comp positions: %w", err)
	}
	var arr []struct {
		Contract   string `json:"contract"`
		Size       int64  `json:"size"`
		EntryPrice string `json:"entry_price"`
		MarkPrice  string `json:"mark_price"`
		Funding    string `json:"funding"`
		Mode       string `json:"mode"`
	}
	if err := json.Unmarshal(body, &arr); err != nil {
		return body, nil, fmt.Errorf("gate dual_comp positions json: %w", err)
	}
	out := make([]GatePosition, 0, len(arr))
	for _, p := range arr {
		if p.Size == 0 {
			continue // 侧空（单侧持仓时对侧 size=0）不误报
		}
		ep, err := f64z(p.EntryPrice)
		if err != nil {
			return body, nil, fmt.Errorf("gate dual position %s(%s) entry_price=%q: %w", p.Contract, p.Mode, p.EntryPrice, err)
		}
		mp, err := f64z(p.MarkPrice)
		if err != nil {
			return body, nil, fmt.Errorf("gate dual position %s(%s) mark_price=%q: %w", p.Contract, p.Mode, p.MarkPrice, err)
		}
		fd, err := f64z(p.Funding)
		if err != nil {
			return body, nil, fmt.Errorf("gate dual position %s(%s) funding=%q: %w", p.Contract, p.Mode, p.Funding, err)
		}
		out = append(out, GatePosition{
			Contract: p.Contract, Size: p.Size,
			EntryPrice: ep, MarkPrice: mp, Funding: fd,
		})
	}
	return body, out, nil
}
