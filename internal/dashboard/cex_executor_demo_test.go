// cex_executor_demo_test.go —— demo 真单验证（D-059，手动 gate）。
// 仅当 ARB_DEMO_ORDER=1 且 ARB_DEMO_PG_DSN 设置时运行；对 exchange_configs 每条配置
// （spot/perp 都验）下极小 qty 买单 → 断言 orderId → 反向平掉，证明 demo key 真实具备
// 交易权限 + 签名/端点/路由正确。
// 安全闸：demo 模拟盘假钱 + 极小量 + 立即反向平仓；连的是生产库（只读 exchange_configs，
// 与测试基建的「dbname 含 test」闸不同——本测试无 TRUNCATE/DROP，仅下单/平仓 demo 订单）。
package dashboard

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"arb/internal/exec"
	"arb/internal/httpx"
	"arb/internal/store/pgstore"
)

// TestDemoRealOrder 对 DB 中每条 exchange_configs 配置下 demo 真单并反向平掉。
// 验证目标：CEXExecutor 接通后 demo key 能真实下单（binance /demo 路由 + okx 模拟盘四头）。
// 平仓策略按类型区分：
//   - binance spot：查账户实际 free（手续费已扣）对齐 LOT_SIZE stepSize（0.00001）；
//   - okx spot：手续费按 USDT 计，base 可全卖 → 用买入量；
//   - perp：平仓=平掉持仓数量（手续费从 USDT 结算，持仓数不变）→ 用买入量。
func TestDemoRealOrder(t *testing.T) {
	if os.Getenv("ARB_DEMO_ORDER") != "1" {
		t.Skip("ARB_DEMO_ORDER=1 才跑 demo 真单（手动 gate，避免常规 go test 触发真实下单）")
	}
	dsn := os.Getenv("ARB_DEMO_PG_DSN")
	if dsn == "" {
		t.Skip("ARB_DEMO_PG_DSN 未设置")
	}

	ctx := context.Background()
	st, err := pgstore.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	defer st.Close()

	cfgs, err := st.ListExchangeConfigs(ctx)
	if err != nil {
		t.Fatalf("list exchanges: %v", err)
	}

	ran := false
	for i := range cfgs {
		c := &cfgs[i]
		ran = true
		t.Run(c.Venue+":"+c.VenueType, func(t *testing.T) {
			sym, qty := demoSymQty(c.Venue, c.VenueType)
			base := resolveExchangeBaseURL(*c)
			e := exec.NewCEXExecutor(c.Venue, c.VenueType, c.APIKey, c.APISecret, c.Passphrase, base, c.Simulated)

			if c.Venue == "okx" && c.VenueType == "perp" {
				// 开仓前先自愈清残留双向仓（历史 bug 曾留 long+short，D-059 实施修正②），
				// 使本测试幂等可重跑——否则残留仓会污染本轮的 BUY/SELL 往返。
				if err := healOKXPerp(ctx, e, sym, t); err != nil {
					t.Fatalf("okx perp 自愈清理残留持仓失败: %v", err)
				}
			}

			t.Logf("== %s:%s demo BUY %s x %v (base=%s simulated=%v)", c.Venue, c.VenueType, sym, qty, base, c.Simulated)

			buy, err := e.PlaceOrder(ctx, exec.Order{Symbol: sym, Side: "BUY", Qty: qty})
			if err != nil {
				// 50120 = OKX key 未开交易权限（只读 key，余额查询通但下单被拒）。
				// 这是后台配置状态，非代码缺陷：如实标注，业主到 OKX 后台给该 key 开 Trade 权限后再验。
				if c.Venue == "okx" && strings.Contains(err.Error(), "50120") {
					t.Skipf("OKX demo key 无交易权限（code 50120）：业主需在 OKX 后台给 key 开 Trade 权限后重跑；本次其余 venue 照常验证")
				}
				t.Fatalf("%s:%s demo buy: %v", c.Venue, c.VenueType, err)
			}
			if buy.OrderID == "" {
				t.Fatalf("%s:%s demo buy: empty orderID", c.Venue, c.VenueType)
			}
			t.Logf("%s:%s demo BUY order %s filled qty=%v price=%v", c.Venue, c.VenueType, buy.OrderID, buy.Qty, buy.Price)
			if buy.Price <= 0 {
				t.Errorf("%s:%s demo buy: price=0（均价解析失败，见 binanceOrderAvgPrice）", c.Venue, c.VenueType)
			}

			// 反向平掉（见函数头注释的平仓策略）。
			closeQty := buy.Qty
			positionSide := ""
			switch {
			case c.Venue == "binance" && c.VenueType == "spot":
				free := binanceFree(ctx, c, "BTC")
				if free <= 0 {
					t.Fatalf("binance free BTC <= 0: %v", free)
				}
				closeQty = floorToStep(free, 0.00001) // BTCUSDT LOT_SIZE stepSize
				t.Logf("binance free BTC=%v → closeQty=%v (step 0.00001)", free, closeQty)
			case c.Venue == "okx" && c.VenueType == "perp":
				// long_short_mode：BUY 开的 long 须用 SELL+posSide=long 平（减仓），否则 SELL
				// 派生 posSide=short 会新开空仓（D-059 实施修正② 实测双双持仓）。
				positionSide = "long"
			}
			close, err := e.PlaceOrder(ctx, exec.Order{Symbol: sym, Side: "SELL", Qty: closeQty, PositionSide: positionSide})
			if err != nil {
				if c.Venue == "okx" && c.VenueType == "spot" && isOKXSimSpotSellImpossible(err) {
					// OKX 模拟盘 spot 结构性不可卖（见 isOKXSimSpotSellImpossible 注释）：环境缺陷
					// 非代码缺陷。BUY 已证执行器下单/签名/解析/成交全通；SELL 被模拟盘拒为环境限制。
					// 如实 skip（带证据），不假绿也不误红。
					t.Skipf("OKX 模拟盘 spot SELL 结构性不可成交（环境限制，非代码缺陷）：%v；BUY 侧已证执行器工作，详见 isOKXSimSpotSellImpossible", err)
				}
				t.Fatalf("%s:%s demo sell(close): %v", c.Venue, c.VenueType, err)
			}
			t.Logf("%s:%s demo SELL close order %s filled qty=%v price=%v", c.Venue, c.VenueType, close.OrderID, close.Qty, close.Price)
			if close.Qty <= 0 {
				t.Fatalf("%s:%s demo sell(close): filled qty=0（平仓未成交——订单被取消/拒绝，executor 轮询已应报错）", c.Venue, c.VenueType)
			}
			if c.Venue == "okx" && c.VenueType == "perp" {
				// 强断言：往返后 perp 仓位归零（BUY→long，SELL+posSide=long→减 long）。
				if poss, err := e.OKXPositions(ctx, sym); err != nil {
					t.Fatalf("okx perp 平仓后核对持仓: %v", err)
				} else if len(poss) > 0 {
					t.Errorf("okx perp 往返后仍有持仓 %+v", poss)
				}
			}
		})
	}
	if !ran {
		t.Skip("无 exchange_configs 配置，跳过")
	}
	log.Println("[demo-order] 全部 exchange_configs venue demo 下单+平仓完成")
}

// demoSymQty 返回 venue+type 的 demo 符号与下单 qty（已按真实最小单/精度实测校准）。
func demoSymQty(venue, venueType string) (string, float64) {
	switch {
	case venue == "binance":
		// spot LOT_SIZE stepSize 0.00001，0.0001（≈$7）足够；perp 需 Binance 单独 futures
		// testnet key（demo-api 不服务 /fapi，D-059 实施修正②），本函数不覆盖。
		return "BTCUSDT", 0.0001
	case venue == "okx" && venueType == "perp":
		// OKX swap sz=合约数：BTC-USDT-SWAP ctVal=0.01 BTC、minSz=lotSz=0.01（实测校准）。
		return "BTC-USDT-SWAP", 0.01
	default: // okx spot
		// tgtCcy=base_ccy 后 sz=基础币数量（缺省买方向=报价币金额，0.0001 当 0.0001 USDT 恒 51020，实测）。
		// 0.0002（≈$14）稳妥高于 min notional。模拟盘 spot SELL 结构性不可成交（见
		// isOKXSimSpotSellImpossible），买量取小以约束无法回卖导致的假币积累。
		return "BTC-USDT", 0.0002
	}
}

// isOKXSimSpotSellImpossible 判别 OKX 模拟盘 spot SELL 是否因环境结构性缺陷而不可成交
// （此时跳过验证，不视为代码缺陷）。实测证据（2026-08-20 D-059 实施修正②）：
//   - 模拟盘 BTC-USDT 订单簿单边不对称：bid 侧深度 0.00005 BTC，ask 侧 71 BTC；
//   - 市价 SELL 任一大于 bid 深度的量 → 滑穿 bid 触发 5% 滑点保护 → state=canceled，
//     cancelSourceReason「slipped beyond the best bid ... by at least 5%」；
//   - 而 ≤ bid 深度的量（0.00004）又低于最小订单金额（51020 minimum order amount）；
//   - 因此任何可行量都不存在 → SELL 结构性无法成交，与执行器实现无关。
// 仅当错误精确匹配上述两种签名才判为环境限制；其余错误如实 fail（防掩盖真实回归）。
func isOKXSimSpotSellImpossible(err error) bool {
	s := err.Error()
	if strings.Contains(s, "51020") && strings.Contains(s, "minimum order") {
		return true
	}
	if strings.Contains(s, "state=canceled") && strings.Contains(s, "slipped beyond the best bid") {
		return true
	}
	return false
}

// binanceFree 查询 Binance 账户某资产可卖 free（签名请求，httpx 走代理注入）。
func binanceFree(ctx context.Context, c *pgstore.ExchangeConfig, asset string) float64 {
	params := url.Values{}
	params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	params.Set("recvWindow", "5000")
	params.Set("signature", binanceHMAC(c.APISecret, params.Encode()))
	u := c.BaseURL + "/api/v3/account?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("X-MBX-APIKEY", c.APIKey)
	resp, err := httpx.Client().Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var acc struct {
		Balances []struct {
			Asset string `json:"asset"`
			Free  string `json:"free"`
		} `json:"balances"`
	}
	if err := json.Unmarshal(b, &acc); err != nil {
		return 0
	}
	for _, bal := range acc.Balances {
		if bal.Asset == asset {
			v, _ := strconv.ParseFloat(bal.Free, 64)
			return v
		}
	}
	return 0
}

// binanceHMAC Binance HMAC-SHA256 hex 签名（与 exec 同算法，测试本地复制避免跨包）。
func binanceHMAC(secret, query string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(query))
	return hex.EncodeToString(mac.Sum(nil))
}

// floorToStep 向下取整到 step 的整数倍（对齐 Binance LOT_SIZE 精度）。
func floorToStep(v, step float64) float64 {
	if v < 0 {
		return 0
	}
	return math.Floor(v/step+1e-9) * step
}

// healOKXPerp 自愈关闭 instId 上残留的 perp 持仓（历史 bug 双向仓），使 demo 往返测试幂等。
// 平仓量 = |pos|，方向/posSide 按原持仓侧（long→SELL、short→BUY），保证减仓而非新开反向仓。
func healOKXPerp(ctx context.Context, e *exec.CEXExecutor, instID string, t *testing.T) error {
	poss, err := e.OKXPositions(ctx, instID)
	if err != nil {
		return fmt.Errorf("查询 %s 持仓: %w", instID, err)
	}
	for _, p := range poss {
		if p.Pos == 0 {
			continue
		}
		side := "SELL"
		if p.PosSide == "short" {
			side = "BUY"
		}
		f, err := e.PlaceOrder(ctx, exec.Order{Symbol: instID, Side: side, Qty: math.Abs(p.Pos), PositionSide: p.PosSide})
		if err != nil {
			return fmt.Errorf("heal %s %s x%v: %w", instID, p.PosSide, p.Pos, err)
		}
		if f.Qty <= 0 {
			return fmt.Errorf("heal %s %s x%v: 平仓未成交 qty=0", instID, p.PosSide, p.Pos)
		}
		t.Logf("heal 已平 %s %s x%v order=%s filled qty=%v", instID, p.PosSide, p.Pos, f.OrderID, f.Qty)
	}
	return nil
}
