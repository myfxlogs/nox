// cex_capital_gaps_test.go —— D-546 S1：三缺口采集器（mock 三端点）。
// 锚：三面解析正确 / 签名参数在位 / 单面失败不拖垮其余面（Errors 留痕）/ 三面全败报错。
package reconcile

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockCapitalGapsServer 三端点同 server 不同路径前缀（/sapi /dapi /eapi——构造器派生约定同形）。
func mockCapitalGapsServer(t *testing.T, earnFail, coinmFail, optionsFail bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/sapi/v1/simple-earn/account", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-MBX-APIKEY") == "" || r.URL.Query().Get("signature") == "" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		if earnFail {
			http.Error(w, "earn down", 500)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{
			"totalAmountInBTC":          "0.00000000",
			"totalAmountInUSDT":         "1234.56",
			"totalFlexibleAmountInBTC":  "0.00000000",
			"totalFlexibleAmountInUSDT": "1000.00",
			"totalLockedInBTC":          "0.00000000",
			"totalLockedInUSDT":         "234.56",
		})
	})
	mux.HandleFunc("/dapi/v1/balance", func(w http.ResponseWriter, r *http.Request) {
		if coinmFail {
			http.Error(w, "coinm down", 500)
			return
		}
		json.NewEncoder(w).Encode([]map[string]string{
			{"asset": "BTC", "balance": "0.05", "availableBalance": "0.04"},
			{"asset": "ETH", "balance": "0.00", "availableBalance": "0.00"}, // 零余额行应滤除
		})
	})
	mux.HandleFunc("/eapi/v1/account", func(w http.ResponseWriter, r *http.Request) {
		if optionsFail {
			http.Error(w, "options down", 500)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"asset": []map[string]string{
				{"asset": "USDT", "marginBalance": "617.77", "equity": "617.77", "available": "300.00", "locked": "0"},
			},
		})
	})
	return httptest.NewServer(mux)
}

func gapsFromServer(t *testing.T, srv *httptest.Server) *BinanceCapitalGaps {
	t.Helper()
	base := srv.URL + "/api"
	return &BinanceCapitalGaps{
		client: srv.Client(), key: "k", secret: "s",
		sapiURL: strings.Replace(base, "/api", "/sapi", 1),
		dapiURL: strings.Replace(base, "/api", "/dapi", 1),
		eapiURL: strings.Replace(base, "/api", "/eapi", 1),
	}
}

func TestD546_CapitalGapsFetch(t *testing.T) {
	srv := mockCapitalGapsServer(t, false, false, false)
	defer srv.Close()
	out, err := gapsFromServer(t, srv).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(out.Errors) != 0 {
		t.Errorf("Errors 应空, got %v", out.Errors)
	}
	if out.EarnFlexibleUSDT != 1000.00 || out.EarnLockedUSDT != 234.56 || out.EarnTotalUSDT != 1234.56 {
		t.Errorf("earn 面解析错: flexible=%v locked=%v total=%v", out.EarnFlexibleUSDT, out.EarnLockedUSDT, out.EarnTotalUSDT)
	}
	if len(out.CoinM) != 1 || out.CoinM[0].Asset != "BTC" || out.CoinM[0].Balance != 0.05 || out.CoinM[0].Available != 0.04 {
		t.Errorf("coinm 面解析错: %+v（零余额 ETH 行应滤除）", out.CoinM)
	}
	if len(out.Options) != 1 || out.Options[0].Asset != "USDT" || out.Options[0].Equity != 617.77 || out.Options[0].Available != 300.00 {
		t.Errorf("options 面解析错: %+v", out.Options)
	}
}

// TestD546_CapitalGapsSignature 签名在位：query 的 signature=HMAC-SHA256(secret, timestamp&recvWindow 串)。
func TestD546_CapitalGapsSignature(t *testing.T) {
	var gotSig, gotRaw string
	mux := http.NewServeMux()
	mux.HandleFunc("/sapi/v1/simple-earn/account", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		gotSig = q.Get("signature")
		gotRaw = "timestamp=" + q.Get("timestamp") + "&recvWindow=5000" // 签名原串（构造器顺序）
		json.NewEncoder(w).Encode(map[string]string{"totalFlexibleAmountInUSDT": "0", "totalLockedInUSDT": "0", "totalAmountInUSDT": "0"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if _, err := gapsFromServer(t, srv).Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	mac := hmac.New(sha256.New, []byte("s"))
	mac.Write([]byte(gotRaw))
	if want := hex.EncodeToString(mac.Sum(nil)); gotSig != want {
		t.Errorf("signature=%s, want HMAC(%s)=%s", gotSig, gotRaw, want)
	}
}

// TestD546_CapitalGapsDegraded 单面失败不拖垮其余面；三面全败报错且 Errors 全留痕。
func TestD546_CapitalGapsDegraded(t *testing.T) {
	srv := mockCapitalGapsServer(t, true, false, false)
	defer srv.Close()
	out, err := gapsFromServer(t, srv).Fetch(context.Background())
	if err != nil {
		t.Fatalf("单面失败不应报错: %v", err)
	}
	if len(out.Errors) != 1 || !strings.Contains(out.Errors[0], "理财面") {
		t.Errorf("Errors 应留痕理财面失败, got %v", out.Errors)
	}
	if len(out.CoinM) != 1 || len(out.Options) != 1 {
		t.Errorf("其余两面应正常: coinm=%d options=%d", len(out.CoinM), len(out.Options))
	}

	srv2 := mockCapitalGapsServer(t, true, true, true)
	defer srv2.Close()
	out2, err := gapsFromServer(t, srv2).Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "三面全败") {
		t.Errorf("三面全败应报错, got err=%v", err)
	}
	if len(out2.Errors) != 3 {
		t.Errorf("Errors 应=3, got %v", out2.Errors)
	}
}
