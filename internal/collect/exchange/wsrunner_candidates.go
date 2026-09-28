// wsrunner_candidates.go —— D-541 S3：RunWS 候选级拨号件+冷却降级调度。
// WSSpec.Candidates 非空时逐候选独立拨号件（直连兜底=裸 dialer+nil Header，
// token 不泄原生域）；冷却=同候选连续数据看门狗杀达阈→冷却期 dial 跳过，
// 到期自动回主（主端恒优先，兜底只在主端僵尸期顶位）。
package exchange

import (
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// WSCandidate 候选级拨号件（D-541 S3）：URL+Dialer+Header 三元组独立装配——
// 直连兜底候选须裸 dialer+nil Header（代理 token 不泄原生 venue 域，D-450/R3）。
type WSCandidate struct {
	URL    string
	Dialer *websocket.Dialer // nil=裸默认直连（与 spec.Dialer nil 同形态）
	Header http.Header       // nil=显式无鉴权头
}

// D-541 S3 冷却降级默认参数（WSSpec 字段零值时生效）。
const (
	wsCooldownKills  = 2
	wsCooldownWindow = 30 * time.Minute
)

// specCandidates 归一候选表：Candidates 优先；空则 URL/URLs+Dialer/Header 折成
// 同 dialer/header 的候选集（旧装配语义零变化）。
func specCandidates(spec WSSpec) []WSCandidate {
	if len(spec.Candidates) > 0 {
		return spec.Candidates
	}
	urls := spec.URLs
	if len(urls) == 0 {
		urls = []string{spec.URL}
	}
	cands := make([]WSCandidate, len(urls))
	for i, u := range urls {
		cands[i] = WSCandidate{URL: u, Dialer: spec.Dialer, Header: spec.Header}
	}
	return cands
}

// wsCandState 候选冷却状态——单 RunWS 持有，跨重连持久（冷却记忆不随连接重置）。
// 访问面：dial 序选/僵尸杀计数/回主判定，mu 护住 ping goroutine 与主流程交叉。
type wsCandState struct {
	mu      sync.Mutex
	kills   []int        // 每候选连续数据看门狗杀次数（非看门狗死因清零）
	cooled  []time.Time  // 每候选冷却截止（零值=未冷却）
	cur     atomic.Int32 // 当前连接候选序号（-1=未连接）
	dataKil atomic.Bool  // 本连接被数据看门狗杀标记
}

// dialOrder 冷却期候选跳过（全候选冷却中=不掐死数据面，忽略冷却全量按序试拨）。
func (st *wsCandState) dialOrder(n int) []int {
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.kills) != n {
		st.kills, st.cooled = make([]int, n), make([]time.Time, n)
	}
	now := time.Now()
	out := make([]int, 0, n)
	for i := 0; i < n; i++ {
		if now.Before(st.cooled[i]) {
			continue
		}
		out = append(out, i)
	}
	if len(out) == 0 {
		for i := 0; i < n; i++ {
			out = append(out, i)
		}
	}
	return out
}

// noteExit 连接收尾归因（runWSOnceSt defer，主流程单点写）：数据看门狗杀→
// 连续计数+1，达 CooldownKills→冷却 CooldownWindow 并清计数；其他死因→计数清零。
func (st *wsCandState) noteExit(spec WSSpec) {
	st.mu.Lock()
	defer st.mu.Unlock()
	cur := int(st.cur.Load())
	if cur < 0 || cur >= len(st.kills) {
		return
	}
	if !st.dataKil.Load() {
		st.kills[cur] = 0
		return
	}
	kills, window := spec.CooldownKills, spec.CooldownWindow
	if kills <= 0 {
		kills = wsCooldownKills
	}
	if window <= 0 {
		window = wsCooldownWindow
	}
	st.kills[cur]++
	if st.kills[cur] >= kills {
		st.cooled[cur] = time.Now().Add(window)
		log.Printf("[%s] 候选 #%d 连续 %d 次数据看门狗杀——冷却 %v", spec.Name, cur, st.kills[cur], window)
		st.kills[cur] = 0
	}
}

// earlierReady 早候选冷却到期（ping tick 判）：存在序号<cur 且未在冷却期的候选
// →主端可拨，兜底顶位使命结束（断连重拨回主）。
func (st *wsCandState) earlierReady() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now()
	for i := 0; i < int(st.cur.Load()) && i < len(st.cooled); i++ {
		if !now.Before(st.cooled[i]) {
			return true
		}
	}
	return false
}
