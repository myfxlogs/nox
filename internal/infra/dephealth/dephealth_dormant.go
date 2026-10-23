package dephealth

// StateDormant 显式停采休眠态（停闸 venue 行情面——非故障非等待，透出文案即存储值）。
const StateDormant = "dormant"

// dormantPreds 休眠谓词表（停闸 venue 行情面键集——注册后 Snapshot 透出
// dormant，聚合段不当事故事件算；私面监护键不入表〔gate:ws-private/
// binance:*-user 铁边界〕）。
var dormantPreds []func(string) bool

// Dormant 注册休眠谓词（停闸装配点调用——命中键透出 dormant 态）。
func Dormant(pred func(string) bool) {
	mu.Lock()
	dormantPreds = append(dormantPreds, pred)
	mu.Unlock()
}

func isDormant(key string) bool {
	for _, p := range dormantPreds {
		if p(key) {
			return true
		}
	}
	return false
}
