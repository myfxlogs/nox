package bus

// OrderEvent 订单状态变更通知（D-107 P1）：OnOrderUpdate 流事件最小化透传。
// 只含通知不含数据——消费端收到后全量回读（前端零信任）。
// Ticket=0 表示无法从帧提取 ticket 的通用通知（消费端仍应全量回读）。
type OrderEvent struct {
	Venue  string
	Ticket int64
}
