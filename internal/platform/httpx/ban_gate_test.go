package httpx

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

type stubRT struct {
	status int
	body   string
}

func (s stubRT) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: s.status,
		Body:   io.NopCloser(strings.NewReader(s.body)),
		Header: http.Header{}, Request: r}, nil
}

func bnReq() *http.Request {
	r, _ := http.NewRequest("GET", "https://api.binance.com/api/v3/ping", nil)
	return r
}

func TestBanGate_418ParsesAndShortCircuits(t *testing.T) {
	banUntilUnixMs.Store(0)
	banLogged.Store(false)
	defer banUntilUnixMs.Store(0)
	until := time.Now().Add(300 * time.Second)
	g := banGateRT{base: stubRT{418, `{"code":-1003,"msg":"Way too many requests; IP(1.2.3.4) banned until ` +
		strconv.FormatInt(until.UnixMilli(), 10) + `. Please use the websocket."}`}}
	if _, err := g.RoundTrip(bnReq()); err != nil {
		t.Fatalf("418 透传径 err=%v", err)
	}
	if _, err := g.RoundTrip(bnReq()); !errors.Is(err, ErrBinanceBanned) {
		t.Fatalf("闸窗内未短路: %v", err)
	}
}

func TestBanGate_ReleaseAfterT(t *testing.T) {
	banUntilUnixMs.Store(0)
	banLogged.Store(false)
	recordBan(time.Now().Add(-time.Second))
	g := banGateRT{base: stubRT{200, "{}"}}
	resp, err := g.RoundTrip(bnReq())
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("T 后未放行: %v", err)
	}
}

func TestBanGate_429LightBackoff(t *testing.T) {
	banUntilUnixMs.Store(0)
	banLogged.Store(false)
	defer banUntilUnixMs.Store(0)
	g := banGateRT{base: stubRT{429, "{}"}}
	g.RoundTrip(bnReq())
	if rem := bannedNow(); rem <= 0 || rem > 31*time.Second {
		t.Fatalf("429 轻退避窗缺: %v", rem)
	}
}

func TestBanGate_NonBinanceUnaffected(t *testing.T) {
	banUntilUnixMs.Store(time.Now().Add(time.Hour).UnixMilli())
	defer banUntilUnixMs.Store(0)
	g := banGateRT{base: stubRT{200, "{}"}}
	r, _ := http.NewRequest("GET", "https://api.coinbase.com/v2/time", nil)
	resp, err := g.RoundTrip(r)
	if err != nil || resp.StatusCode != 200 {
		t.Fatal("非 bn 域误伤")
	}
}
