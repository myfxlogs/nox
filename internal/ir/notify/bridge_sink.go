// bridge_sink.go —— 告警→AI 分诊桥 sink（设计稿 §8.1 后端件）
// 与 FeishuClient 并列写出层：critical 全级+warn 窄白名单触发 POST 本机桥；
// 旁路零阻塞（fail-open：桥败不影响飞书主径——§7 硬线）；24h 同文案去重；
// O1 计数预留（Stats() 供 digest 活性回执消费）。
package notify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// bridgePayload S1 载荷 schema 定死——v1/v2 漂移源头锁死。
type bridgePayload struct {
	AlertID   int64  `json:"alert_id"`
	Level     string `json:"level"`
	Message   string `json:"message"`
	TsUTC     string `json:"ts_utc"`
	DedupHash string `json:"dedup_hash"`
}

// AgentBridgeSink 告警桥 sink（worker 池投递——非阻塞满丢弃留痕同 feishu 径）。
type AgentBridgeSink struct {
	url      func() string   // app_config 热读——缺=功能静默关闭 fail-closed
	token    func() string   // app_secrets 热读（secRes）
	patterns func() []string // warn 白名单前缀（app_config 逗号分）
	client   *http.Client
	jobs     chan bridgePayload
	mu       sync.Mutex
	seen     map[string]time.Time // dedup_hash→最近推桥戳（24h 去重）
	// O1 桥活性计数（digest 06:10 批消费面）
	Posted, Failed, Dropped uint64
}

// NewAgentBridgeSink 装配：三个读函数闭包——url/token/patterns 皆热读源。
// jobs 上限=桥侧队列同径（满丢弃计数防 sink 堵主径）。
func NewAgentBridgeSink(url, token func() string, patterns func() []string) *AgentBridgeSink {
	s := &AgentBridgeSink{
		url: url, token: token, patterns: patterns,
		client: &http.Client{Timeout: 10 * time.Second},
		jobs:   make(chan bridgePayload, 64),
		seen:   map[string]time.Time{},
	}
	return s
}

// Push 告警入桥径——critical 全级+warn 白名单；24h 同文案去重；fail-open。
func (s *AgentBridgeSink) Push(id int64, level, message string) {
	lvl := strings.ToLower(level)
	if lvl != "critical" {
		if lvl != "warn" || !s.warnAllowed(message) {
			return
		}
	}
	if s.url() == "" {
		return // fail-closed：桥地址缺=静默关（飞书主径无损）
	}
	sum := sha256.Sum256([]byte(message))
	hash := hex.EncodeToString(sum[:16])
	s.mu.Lock()
	if ts, ok := s.seen[hash]; ok && time.Since(ts) < 24*time.Hour {
		s.mu.Unlock()
		s.Dropped++
		return
	}
	s.seen[hash] = time.Now()
	// 24h 去重窗清理（entries 超 4096 时全表扫删——防长期运行内存膨胀）
	if len(s.seen) > 4096 {
		for k, ts := range s.seen {
			if time.Since(ts) > 24*time.Hour {
				delete(s.seen, k)
			}
		}
	}
	s.mu.Unlock()
	p := bridgePayload{
		AlertID: id, Level: lvl, Message: message,
		TsUTC: time.Now().UTC().Format(time.RFC3339), DedupHash: hash,
	}
	select {
	case s.jobs <- p:
	default:
		s.Dropped++ // 满丢弃留痕——同 feishu worker 池语义
	}
}

// warnAllowed warn 级白名单前缀命中（逗号分前缀枚举起手三族）。
func (s *AgentBridgeSink) warnAllowed(msg string) bool {
	for _, p := range s.patterns() {
		if p != "" && strings.HasPrefix(msg, p) {
			return true
		}
	}
	return false
}

// Run worker——单协程串行消费（桥侧已串行，sink 再串行=双闸对拍）。
func (s *AgentBridgeSink) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case p := <-s.jobs:
			if err := s.post(ctx, p); err != nil {
				s.Failed++
				fmt.Printf("[bridge] post fail（静默计数不阻塞飞书主径）: %v\n", err)
			} else {
				s.Posted++
			}
		}
	}
}

func (s *AgentBridgeSink) post(ctx context.Context, p bridgePayload) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", s.url()+"/alert", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Bridge-Token", s.token())
	res, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("bridge status %d", res.StatusCode)
	}
	return nil
}
