package autotrade

// engine_csmiss.go —— CS-miss 聚合告警（D-140 S2），从 engine_opp.go 拆出（>300 行门禁）。

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// === D-140 S2：CS-miss 聚合告警 ===

// csMissAlertInterval CS-miss 告警 flush 间隔（D-140 S2）。
const csMissAlertInterval = 10 * time.Minute

// recordCSMiss 记一次 CS-miss（D-140 S2）。按 (venue,canonical) 聚合，
// 定期 flushCSMissAlerts 发 info 告警（同 key 去重 ≥1h）。
func (e *Engine) recordCSMiss(venue, canonical string) {
	key := venue + "|" + canonical
	e.csMissMu.Lock()
	e.csMissCounts[key]++
	e.csMissMu.Unlock()
}

// flushCSMissAlerts 发出聚合 CS-miss 告警（D-140 S2）。
// 由 positionLoop 定期调用（csMissAlertInterval）；同 key 去重 ≥1h。
func (e *Engine) flushCSMissAlerts() {
	e.csMissMu.Lock()
	if len(e.csMissCounts) == 0 {
		e.csMissMu.Unlock()
		return
	}
	counts := e.csMissCounts
	e.csMissCounts = make(map[string]int)
	now := time.Now()
	// 去重：跳过 1h 内已发过的 key
	var lines []string
	for key, n := range counts {
		if last, ok := e.csMissSent[key]; ok && now.Sub(last) < time.Hour {
			continue
		}
		e.csMissSent[key] = now
		lines = append(lines, fmt.Sprintf("%s count=%d", key, n))
	}
	e.csMissMu.Unlock()
	if len(lines) == 0 {
		return
	}
	msg := "unmapped-or-no-CS: " + strings.Join(lines, "; ")
	log.Printf("[autotrade] %s", msg)
	if e.AlertSink != nil {
		e.AlertSink("info", msg)
	}
}
