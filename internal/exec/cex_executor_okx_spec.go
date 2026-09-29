// cex_executor_okx_spec.go —— OKX instruments 规格层（D-566 自 helpers 拆出防 >300 线）。
// okxSwapSpec/okxSpotSpec 结构 + 包级缓存 + 查询 + state 闸。
package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// okxSwapSpec okx SWAP instruments 规格（ctVal=合约面值/lotSz=最小张数/maxMktSz=市价单笔上限张数）。
// at 字段 D-566 S1 预埋（包 B S6 TTL 复用，本包只加不消费）。
type okxSwapSpec struct {
	ctVal, lotSz, maxMktSz float64
	at                     time.Time
}

// okxSwapSpecCache instruments 规格包级缓存（ctVal/lotSz/maxMktSz 慢变）——
// D-311 R4：CEXExecutor 下单换算与 dashboard floorToStepQty 共用同源缓存。
var okxSwapSpecCache sync.Map // instID → okxSwapSpec

// okxSwapSpecState D-564 S6 U9：instruments.state 缓存（live/preopen/suspend/expired；
// 规格慢变元数据允许缓存——state 闸非决策时变数据面）。
var okxSwapSpecState sync.Map

// okxSpecTTL D-566 包B S6-a：规格缓存 24h 惰性判龄（instruments 慢变；变更主通道=
// instruments WS Invalidate，TTL 为断流兜底——禁 ticker）。
const okxSpecTTL = 24 * time.Hour

// OkxSwapSpec 查 okx swap 合约面值 ctVal + 最小张数 lotSz + 市价单笔上限 maxMktSz
// （D-311 R4 从 okxContractVal 提取共用；D-566 S1 扩第三值）。
// 公开端点 GET /api/v5/public/instruments?instType=SWAP&instId=XXX 无需签名。
// maxMktSz 空串→0=无闸直通（字段缺席非证据）。
func OkxSwapSpec(ctx context.Context, client *http.Client, baseURL, instID string) (ctVal, lotSz, maxMktSz float64, err error) {
	if v, ok := okxSwapSpecCache.Load(instID); ok {
		// D-566 包B S6-a：24h TTL 惰性判龄（Load 时检查，禁 ticker）。
		if s := v.(okxSwapSpec); time.Since(s.at) <= okxSpecTTL {
			return s.ctVal, s.lotSz, s.maxMktSz, nil
		}
	}
	endpoint := "/api/v5/public/instruments?instType=SWAP&instId=" + instID
	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+endpoint, nil)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("okx instruments req: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("okx instruments %s: %w", instID, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("okx instruments read: %w", err)
	}
	if resp.StatusCode != 200 {
		return 0, 0, 0, fmt.Errorf("okx instruments %s: http %d: %s", instID, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Code string `json:"code"`
		Data []struct {
			CtVal    string `json:"ctVal"`
			LotSz    string `json:"lotSz"`
			MaxMktSz string `json:"maxMktSz"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, 0, 0, fmt.Errorf("okx instruments json: %w", err)
	}
	if out.Code != "0" || len(out.Data) == 0 {
		return 0, 0, 0, fmt.Errorf("okx instruments %s: code=%s data_len=%d", instID, out.Code, len(out.Data))
	}
	ctVal, err = f64z(out.Data[0].CtVal)
	if err != nil || ctVal <= 0 {
		return 0, 0, 0, fmt.Errorf("okx instruments %s: ctVal=%q invalid", instID, out.Data[0].CtVal)
	}
	lotSz, err = f64z(out.Data[0].LotSz)
	if err != nil || lotSz <= 0 {
		return 0, 0, 0, fmt.Errorf("okx instruments %s: lotSz=%q invalid", instID, out.Data[0].LotSz)
	}
	maxMktSz, err = f64z(out.Data[0].MaxMktSz) // 空串→0=无闸直通（f64z 既有语义）
	if err != nil {
		return 0, 0, 0, fmt.Errorf("okx instruments %s: maxMktSz=%q invalid: %w", instID, out.Data[0].MaxMktSz, err)
	}
	okxSwapSpecCache.Store(instID, okxSwapSpec{ctVal: ctVal, lotSz: lotSz, maxMktSz: maxMktSz, at: time.Now()})
	return ctVal, lotSz, maxMktSz, nil
}

// okxContractVal 查 okx swap 合约面值 ctVal + 最小张数 lotSz + maxMktSz。
// 缓存包级共享（D-311 R4，原 per-executor），避免每单查。D-125。
func (e *CEXExecutor) okxContractVal(ctx context.Context, instID string) (ctVal, lotSz, maxMktSz float64, err error) {
	return OkxSwapSpec(ctx, e.client, e.baseURL, instID)
}

// okxSpotSpec okx SPOT instruments 规格（lotSz/minSz/maxMktAmt=市价单笔名义上限）。
// at 字段 D-566 S1 预埋（包 B S6 TTL 复用，本包只加不消费）。
type okxSpotSpec struct {
	lotSz, minSz, maxMktAmt float64
	at                      time.Time
}

var okxSpotSpecCache sync.Map

// okxSpotLotSz D-473（KB okx-perp-ctval-qty-conversion 同型修）：spot instruments
// 的 lotSz/minSz（instType=SPOT，sz 单位=基础币）；D-566 S1 扩 maxMktAmt 第四值。
// 取不到→err fail-closed（不下未地板单——51121/最小金额拒单先例）。
func (e *CEXExecutor) okxSpotLotSz(ctx context.Context, instID string) (lotSz, minSz, maxMktAmt float64, err error) {
	if s, ok := okxSpotSpecCache.Load(instID); ok {
		if sp := s.(okxSpotSpec); time.Since(sp.at) <= okxSpecTTL {
			return sp.lotSz, sp.minSz, sp.maxMktAmt, nil
		}
	}
	body, err := e.get(ctx, e.baseURL+"/api/v5/public/instruments?instType=SPOT&instId="+url.QueryEscape(instID), nil)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("okx spot instruments %s: %w", instID, err)
	}
	var out struct {
		Code string `json:"code"`
		Data []struct {
			LotSz     string `json:"lotSz"`
			MinSz     string `json:"minSz"`
			MaxMktAmt string `json:"maxMktAmt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, 0, 0, fmt.Errorf("okx spot instruments json: %w", err)
	}
	if out.Code != "0" || len(out.Data) == 0 {
		return 0, 0, 0, fmt.Errorf("okx spot instruments %s: code=%s data_len=%d", instID, out.Code, len(out.Data))
	}
	lotSz, err = f64z(out.Data[0].LotSz)
	if err != nil || lotSz <= 0 {
		return 0, 0, 0, fmt.Errorf("okx spot instruments %s: lotSz=%q invalid", instID, out.Data[0].LotSz)
	}
	if minSz, err = f64z(out.Data[0].MinSz); err != nil {
		return 0, 0, 0, fmt.Errorf("okx spot instruments %s: minSz=%q invalid: %w", instID, out.Data[0].MinSz, err)
	}
	maxMktAmt, err = f64z(out.Data[0].MaxMktAmt) // 空串→0=无闸直通
	if err != nil {
		return 0, 0, 0, fmt.Errorf("okx spot instruments %s: maxMktAmt=%q invalid: %w", instID, out.Data[0].MaxMktAmt, err)
	}
	okxSpotSpecCache.Store(instID, okxSpotSpec{lotSz: lotSz, minSz: minSz, maxMktAmt: maxMktAmt, at: time.Now()})
	return lotSz, minSz, maxMktAmt, nil
}

// okxStateCheck D-564 S6 U9：instruments.state 闸——非 live（preopen/suspend/expired）
// fail-closed。spec 缓存共享（okxSwapSpecCache 同包），state 随缓存写（规格慢变元数据）。
func (e *CEXExecutor) okxStateCheck(ctx context.Context, instID string) error {
	if v, ok := okxSwapSpecState.Load(instID); ok {
		se := v.(okxSwapSpecStateEntry)
		if time.Since(se.at) > okxSpecTTL {
			okxSwapSpecState.Delete(instID) // 超龄落盘重取
		} else if se.state != "" && se.state != "live" {
			return fmt.Errorf("okx %s 合约非 live 态（state=%s）fail-closed 拒单", instID, se.state)
		} else {
			return nil
		}
	}
	endpoint := "/api/v5/public/instruments?instType=SWAP&instId=" + instID
	body, err := e.get(ctx, e.baseURL+endpoint, nil)
	if err != nil {
		return fmt.Errorf("okx instruments state %s: %w", instID, err)
	}
	var out struct {
		Code string `json:"code"`
		Data []struct {
			State string `json:"state"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("okx instruments state json: %w", err)
	}
	if out.Code != "0" || len(out.Data) == 0 {
		return fmt.Errorf("okx instruments state %s: code=%s data_len=%d", instID, out.Code, len(out.Data))
	}
	st := out.Data[0].State
	if st == "" {
		return nil // 老响应无 state 字段=兼容直通（不猜不拒——字段缺席非证据）
	}
	if st != "live" {
		// 非 live 不缓存（preopen→live 转换后下次尝试重查；缓存 live 正常态）。
		return fmt.Errorf("okx %s 合约非 live 态（state=%s）fail-closed 拒单", instID, st)
	}
	okxSwapSpecState.Store(instID, okxSwapSpecStateEntry{state: st, at: time.Now()})
	return nil
}

// okxSwapSpecStateEntry D-566 包B S6：state 缓存值型（携 at 供 TTL 判龄）。
type okxSwapSpecStateEntry struct {
	state string
	at    time.Time
}

// OKXSpecInvalidate D-566 包B S6-b：三表同删单口——供 instruments WS 变更帧、
// TTL 判龄、拒单驱动三通道复用。
func OKXSpecInvalidate(instID string) {
	okxSwapSpecCache.Delete(instID)
	okxSpotSpecCache.Delete(instID)
	okxSwapSpecState.Delete(instID)
}
