package bus

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

// 票 10-20：非通道型帧（cmc:cfd）写径前置滤除——不落库、首见记一条、后续静默；
// 白名单型帧照常落库（qbus publish 侧不截，本测试只证写径）。
func TestTickSamplerFiltersNonTicksVenueType(t *testing.T) {
	var writes []TickRow
	ts := NewTickSampler(New(), time.Minute, func(_ context.Context, rows []TickRow) error {
		writes = append(writes, rows...)
		return nil
	}, nil)

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	lastWrite := map[string]time.Time{}
	var mu sync.Mutex
	base := time.Now()
	// cmc:cfd 两帧（跨间隔）——零落库。
	ts.maybeWrite(context.Background(), &mu, lastWrite, Quote{Symbol: "TRXUSDT", Venue: "cmc", VenueType: "cfd", Bid: 1, Ask: 1.1, Time: base})
	ts.maybeWrite(context.Background(), &mu, lastWrite, Quote{Symbol: "TRXUSDT", Venue: "cmc", VenueType: "cfd", Bid: 1, Ask: 1.1, Time: base.Add(2 * time.Minute)})
	if len(writes) != 0 {
		t.Fatalf("cfd frames written = %d, want 0", len(writes))
	}
	// binance:perp 照常落库。
	ts.maybeWrite(context.Background(), &mu, lastWrite, Quote{Symbol: "TRXUSDT", Venue: "binance", VenueType: "perp", Bid: 2, Ask: 2.1, Time: base})
	if len(writes) != 1 || writes[0].VenueType != "perp" {
		t.Fatalf("perp frame not written: %+v", writes)
	}
	if drops := strings.Count(buf.String(), "drop TRXUSDT cmc:cfd"); drops != 1 {
		t.Fatalf("drop log lines = %d, want 1 (首见一条): %q", drops, buf.String())
	}
}

// 票 10-20②：写失败日志降频——同键同文案冷却窗内只记首条，文案变或过窗再记。
func TestTickSamplerWriteErrLogCooldown(t *testing.T) {
	ts := NewTickSampler(New(), time.Minute, func(_ context.Context, _ []TickRow) error {
		return errors.New("boom")
	}, nil)

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	ts.logWriteErr("TRXUSDT|binance:perp", "boom")
	ts.logWriteErr("TRXUSDT|binance:perp", "boom")
	if n := strings.Count(buf.String(), "boom"); n != 1 {
		t.Fatalf("log lines = %d, want 1 (冷却窗内)", n)
	}
	ts.logWriteErr("TRXUSDT|binance:perp", "different")
	if n := strings.Count(buf.String(), "write TRXUSDT"); n != 2 {
		t.Fatalf("log lines = %d, want 2 (文案变化再记)", n)
	}
	ts.errStamps["TRXUSDT|binance:perp"] = errStamp{msg: "different", at: time.Now().Add(-writeErrLogCooldown - time.Minute)}
	ts.logWriteErr("TRXUSDT|binance:perp", "different")
	if n := strings.Count(buf.String(), "write TRXUSDT"); n != 3 {
		t.Fatalf("log lines = %d, want 3 (过冷却窗再记)", n)
	}
}
