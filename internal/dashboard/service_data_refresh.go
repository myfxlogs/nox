// service_data_refresh.go —— D-508 S9：域数据刷新通知（StreamUpdates case 14）。
// 写源侧调 PublishDataRefresh(topic)；消费面=StreamUpdates→前端按 topic 集重取。
package dashboard

import (
	"arb/internal/bus"
)

// D-508 S9 域 topic 常量——写源与前端订阅同名。
const (
	RefreshAllowRules  = "allow_rules"            // 白名单/敞口 CAP 写路径
	RefreshGraduation  = "carry_graduation"       // 毕业链建议/审批
	RefreshPaperCarry  = "paper_carry"            // 纸面轨开平
	RefreshIntraTrack  = "intra_track"            // 日内轨步进活动
	RefreshPFAdmission = "perp_funding_admission" // 永续资金费准入评估/覆盖
	RefreshFinance     = "finance"                // 台账/投资记录写路径
	RefreshTrx         = "trx"                    // TRX 质押/余额/收益率变化（D-231 同源扩展）
)

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

// subscribeRefreshTopic StreamUpdates 订阅口（镜像 subscribeCMCWatchTopic；nil=不订阅）。
func (s *Service) subscribeRefreshTopic() (<-chan string, func()) {
	if s.refreshTopic == nil {
		return nil, func() {}
	}
	return s.refreshTopic.Subscribe()
}
