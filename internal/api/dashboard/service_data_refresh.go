// service_data_refresh.go —— D-508 S9：域数据刷新通知（StreamUpdates case 14）。
// 写源侧调 PublishDataRefresh(topic)；消费面=StreamUpdates→前端按 topic 集重取。
package dashboard

import (
	"sync"
	"time"

	"nox/internal/data/bus"
)

// D-508 S9 域 topic 常量——写源与前端订阅同名。
const (
	RefreshAllowRules     = "allow_rules"      // 白名单/敞口 CAP 写路径
	RefreshGraduation     = "carry_graduation" // 毕业链建议/审批
	RefreshPaperCarry     = "paper_carry"      // 纸面轨开平
	RefreshIntraTrack     = "intra_track"      // 日内轨步进活动
	RefreshFinance        = "finance"          // 台账/投资记录写路径
	RefreshTrx            = "trx"              // TRX 质押/余额/收益率变化（D-231 同源扩展）
	RefreshFunds          = "funds"            // D-568：账户资金变更（CEX 活账本/MT5 流帧/monitor 轮询三源）
	RefreshPendingEntries = "pending_entries"  // D-007 S1：待入场闸态翻转（diff 器翻转驱动 emit）
	RefreshCexScan        = "cex_scan"         // D-007 S3：cexscan 每轮快照落库毕（earn APR+spreads+judgment）
	RefreshEmployees      = "employees"        // D-230①：员工心跳写点（roster EmployeeBeat 接线=M2.6）
)

// fundsNotifyInterval D-568：funds 通知尾沿合批窗口。
const fundsNotifyInterval = 2 * time.Second

// fundsNotifier D-568：funds 通知节流态（mu 守 last+pending；Publish 恒在锁外）。
type fundsNotifier struct {
	mu      sync.Mutex
	last    time.Time
	pending bool
}

// NotifyFunds D-568：三源写入口（BalanceLedger.Put / MT5 流帧 / poll changed）统一通知口。
// 语义=「账户资金可能变了」的轻量提示，收到方整表静默重取——不传值不增量防双轨。
// 2s 尾沿合批：距上次 ≥2s 即发；窗口内置 pending + AfterFunc 单次冲刷。尾沿必须存在——
// 末帧掉通知=页面停在旧值，与「写完零通知」同病灶复发。
func (s *Service) NotifyFunds() {
	s.funds.mu.Lock()
	since := time.Since(s.funds.last)
	if since >= fundsNotifyInterval {
		s.funds.last = time.Now()
		s.funds.mu.Unlock()
		s.PublishDataRefresh(RefreshFunds)
		return
	}
	if !s.funds.pending {
		s.funds.pending = true
		time.AfterFunc(fundsNotifyInterval-since, s.flushFunds)
	}
	s.funds.mu.Unlock()
}

// flushFunds 尾沿冲刷（清 pending、记 last、再 Publish；锁外发）。
func (s *Service) flushFunds() {
	s.funds.mu.Lock()
	s.funds.pending = false
	s.funds.last = time.Now()
	s.funds.mu.Unlock()
	s.PublishDataRefresh(RefreshFunds)
}

// SetRefreshTopic 装配注入（cmd/core；nil=不订阅 case 14）。
func (s *Service) SetRefreshTopic(t *bus.Topic[string]) { s.refreshTopic = t }

// PublishDataRefresh 写源发通知（nil-safe：未接线静默；可变 topic——一处写源可刷多域）。
func (s *Service) PublishDataRefresh(topics ...string) {
	if s.refreshTopic == nil {
		return
	}
	for _, t := range topics {
		s.refreshTopic.Publish(t)
	}
}

// subscribeRefreshTopic StreamUpdates 订阅口（nil=不订阅）。
func (s *Service) subscribeRefreshTopic() (<-chan string, func()) {
	if s.refreshTopic == nil {
		return nil, func() {}
	}
	return s.refreshTopic.Subscribe()
}
