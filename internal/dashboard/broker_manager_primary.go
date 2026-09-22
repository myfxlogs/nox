// broker_manager_primary.go D-383 A2.3/A4：一 canonical 一主品种的告警下沉
// （从 broker_manager.go 拆出，保文件 ≤300 行——D-366 check-lines 纪律）。
package dashboard

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"arb/internal/alertcool"
	"arb/internal/bus"
	"arb/internal/exec"
	"arb/internal/store/pgstore"
)

// NewBrokerManager 创建管理器。symbolsTTL 由调用方从 config.SymbolsTTL 注入。
func NewBrokerManager(store *pgstore.Store, qbus *bus.QuoteBus, reg *exec.Registry, symbolsTTL time.Duration) *BrokerManager {
	return &BrokerManager{
		store:            store,
		qbus:             qbus,
		reg:              reg,
		running:          make(map[string]*running),
		commission:       make(map[string]float64),
		symbolGroupsTTL:  symbolsTTL,
		primaryAlertCool: alertcool.New(30 * time.Minute),
	}
}

// alertAmbiguousGroups D-383 A2.3/A4：多行零 primary 歧义组 → alerts 表告警行
// （(venue,canonical) 键 30min 冷却）。歧义态=canonical 事实源停供+执行解析 fail-closed，
// 补 is_primary 种子后自愈；nil AlertSink（测试构造）只耗冷却不落行。
func (m *BrokerManager) alertAmbiguousGroups(groups map[string]bool) {
	if len(groups) == 0 || m.AlertSink == nil {
		return
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !m.primaryAlertCool.Allow("warn", key) {
			continue
		}
		venue, canonical, _ := strings.Cut(key, "|")
		m.AlertSink("warn", fmt.Sprintf("[symbol-map] %s/%s 多行零 primary：canonical 事实源停供（歧义 fail-dead），执行解析 fail-closed——补 is_primary 种子后自愈", venue, canonical))
	}
}
