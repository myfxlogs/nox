// cex_executor_demo_test.go —— demo 真单验证（D-059，手动 gate）。
// 仅当 ARB_DEMO_ORDER=1 且 ARB_DEMO_PG_DSN 设置时运行；验证 demo key 真实具备交易权限 +
// 签名/端点/路由正确：对每个 spot 配置下一笔极小 qty 买单 → 断言 orderId → 反向平掉。
// 安全闸：demo 模拟盘假钱 + 极小量 + 立即反向平仓；连的是生产库（只读 exchange_configs，
// 与测试基建的「dbname 含 test」闸不同——本测试无 TRUNCATE/DROP，仅下单/平仓 demo 订单）。
package dashboard

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// TestDemoRealOrder 对 DB 中每个 spot 配置下 demo 真单并反向平掉。
// 验证目标：CEXExecutor 接通后 demo key 能真实下单（binance /demo 路由 + okx 模拟盘四头）。
// Binance 平仓 qty 取账户实际 free（手续费已扣）并对齐 LOT_SIZE stepSize（0.00001）；
// OKX spot sz 无需扣费（Taker 手续费按 USDT 计，BTC 可全卖）。
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
		if c.VenueType != "spot" {
			t.Logf("skip %s:%s (本次仅验证 spot)", c.Venue, c.VenueType)
			continue
		}
		ran = true
		t.Run(c.Venue+":"+c.VenueType, func(t *testing.T) {
			base := resolveExchangeBaseURL(*c)
			e := exec.NewCEXExecutor(c.Venue, c.VenueType, c.APIKey, c.APISecret, c.Passphrase, base, c.Simulated)

			// 符号用 qbus VenueSymbol 原生格式（binance BTCUSDT / okx BTC-USDT）。
			sym := "BTCUSDT"
			if c.Venue == "okx" {
				sym = "BTC-USDT"
			}
			qty := 0.0001 // 极小量（≈$8.5），demo 余额充足
			t.Logf("== %s:%s demo BUY %s x %v (base=%s simulated=%v)", c.Venue, c.VenueType, sym, qty, base, c.Simulated)

			buy, err := e.PlaceOrder(ctx, exec.Order{Symbol: sym, Side: "BUY", Qty: qty})
			if err != nil {
				// 50120 = OKX key 未开交易权限（只读 key，余额查询通但下单被拒）。
				// 这是后台配置状态，非代码缺陷：如实标注，业主到 OKX 后台给该 key 开 Trade 权限后再验。
				if c.Venue == "okx" && strings.Contains(err.Error(), "50120") {
					t.Skipf("OKX demo key 无交易权限（code 50120）：业主需在 OKX 后台给 key 开 Trade 权限后重跑；本次其余 venue 照常验证")
				}
				t.Fatalf("%s demo buy: %v", c.Venue, err)
			}
			if buy.OrderID == "" {
				t.Fatalf("%s demo buy: empty orderID", c.Venue)
			}
			t.Logf("%s demo BUY order %s filled qty=%v price=%v", c.Venue, buy.OrderID, buy.Qty, buy.Price)
			if buy.Price <= 0 {
				t.Errorf("%s demo buy: price=0（均价解析失败，见 binanceOrderAvgPrice）", c.Venue)
			}

			// 反向平掉：Binance 取实际 free（手续费已扣）对齐 stepSize；OKX 全卖。
			closeQty := buy.Qty
			if c.Venue == "binance" {
				free := binanceFree(ctx, c, "BTC")
				if free <= 0 {
					t.Fatalf("binance free BTC <= 0: %v", free)
				}
				closeQty = floorToStep(free, 0.00001) // BTCUSDT LOT_SIZE stepSize
				t.Logf("binance free BTC=%v → closeQty=%v (step 0.00001)", free, closeQty)
			}
			close, err := e.PlaceOrder(ctx, exec.Order{Symbol: sym, Side: "SELL", Qty: closeQty})
			if err != nil {
				t.Fatalf("%s demo sell(close): %v", c.Venue, err)
			}
			t.Logf("%s demo SELL close order %s filled qty=%v price=%v", c.Venue, close.OrderID, close.Qty, close.Price)
		})
	}
	if !ran {
		t.Skip("无 spot 配置，跳过")
	}
	log.Println("[demo-order] 全部 spot venue demo 下单+平仓完成")
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
