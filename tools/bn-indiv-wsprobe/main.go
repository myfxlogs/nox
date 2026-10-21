// bn-indiv-wsprobe —— binance fstream 个体 bookTicker 小集合并行订阅侦查
// （D-531/E1 衍生票）：聚合流 !bookTicker 钦定形不动，本件只测「≤8 条
// <sym>@bookTicker 个体流单连接并行」的 venue 容忍度。零生产耦合——独立
// 进程、只读 venue 公开流、帧落日志（stdout 重定向），24h 观察窗。
// 用法：bn-indiv-wsprobe [-url wss://fstream.binance.com/ws] [-batch 4]
// [-combined] sym1 sym2 ...
// -batch N = 每帧 SUBSCRIBE 挂 N 条流（默认 4）；-combined = 走官方合并流
// URL `stream?streams=a/b/c`（不发 SUBSCRIBE 帧）。
// 10-21 定谳在案：每连接流数>1 即被 venue 强杀（阈值恰 1，帧形态/端点无关）；
// 本探针=复测/边界复核件，生产聚合流钦定形维持。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	url := flag.String("url", "wss://fstream.binance.com/ws", "ws endpoint")
	batch := flag.Int("batch", 4, "每帧 SUBSCRIBE 挂载流数")
	combined := flag.Bool("combined", false, "走官方合并流 URL stream?streams= 形态")
	flag.Parse()
	syms := flag.Args()
	if len(syms) == 0 || len(syms) > 8 {
		log.Fatal("需要 1-8 个符号（>8 违票面禁线）")
	}
	streams := make([]string, 0, len(syms))
	for _, s := range syms {
		streams = append(streams, strings.ToLower(s)+"@bookTicker")
	}
	dial := *url
	if *combined {
		dial = "wss://fstream.binance.com/stream?streams=" + strings.Join(streams, "/")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("probe start: url=%s streams=%v", dial, streams)
	frames, reconnects := 0, 0
	for ctx.Err() == nil {
		reconnects++
		if err := runOnce(ctx, dial, streams, *batch, *combined, &frames); err != nil {
			log.Printf("conn round %d end: %v", reconnects, err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
	log.Printf("probe stop: frames=%d reconnects=%d", frames, reconnects)
}

// runOnce 单连接一轮：dial→分批 SUBSCRIBE（每帧 batch 条流）
// →帧循环（bookTicker 帧计频采样日志+ack/err 帧原样记）。
// combined 模式 URL 已带流名，跳过 SUBSCRIBE 直进帧循环。
func runOnce(ctx context.Context, url string, streams []string, batch int, combined bool, frames *int) error {
	c, _, err := websocket.DefaultDialer.DialContext(ctx, url, http.Header{})
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer c.Close()
	c.SetReadLimit(1 << 20)
	for i := 0; !combined && i < len(streams); i += batch {
		end := i + batch
		if end > len(streams) {
			end = len(streams)
		}
		sub := fmt.Sprintf(`{"method":"SUBSCRIBE","params":[%s],"id":%d}`,
			`"`+strings.Join(streams[i:end], `","`)+`"`, i/batch+1)
		if err := c.WriteMessage(websocket.TextMessage, []byte(sub)); err != nil {
			return fmt.Errorf("subscribe: %w", err)
		}
		log.Printf("subscribe sent: %s", sub)
		time.Sleep(300 * time.Millisecond) // 批间节拍——venue 突发面纪律
	}
	start, lastBeat := time.Now(), time.Now()
	roundFrames := 0
	perSym := map[string]int{}
	for {
		c.SetReadDeadline(time.Now().Add(75 * time.Second)) // 帧静默>75s=疑似死流
		_, data, err := c.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		s := string(data)
		switch {
		case strings.Contains(s, `"result"`) || strings.Contains(s, `"error"`):
			log.Printf("ack/err frame: %s", s)
		case strings.Contains(s, `"bookTicker"`):
			*frames++
			roundFrames++
			// 帧上符号字段直取（"s":"BTCUSDT"）——免解析全帧开销
			if i := strings.Index(s, `"s":"`); i >= 0 {
				perSym[s[i+4:i+14]]++
			}
		}
		if time.Since(lastBeat) >= 10*time.Minute {
			log.Printf("beat: up=%s frames=%d (round %d) persym=%v",
				time.Since(start).Round(time.Second), *frames, roundFrames, perSym)
			lastBeat = time.Now()
		}
	}
}
