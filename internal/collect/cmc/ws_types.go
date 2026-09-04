// CMC WebSocket 帧类型定义（D-193-A/R6 返修：从 ws.go 提取，控行数 <450）。
package cmc

import "encoding/json"

// wsEnvelope D-193-A/R6 返修：CMC ws 推送帧全部 {"<kind>":{...}} 信封包装。
// 只 msg 进事件分发；auth/init/heartbeat/subscribe/subscriptionChanged 为控制帧按需 log。
// 实证 docs/cmc/证据/connectapi-ws-帧实证-2026-09-04.log：
//
//	{"auth":{"status":"ok"}} | {"init":{heartbeatInterval,...}} | {"msg":{type,subscriptionId,data,partial}}
//	{"heartbeat":{"status":"ok"}} | {"subscribe":{"status":...}} | {"subscriptionChanged":{event,type}}
type wsEnvelope struct {
	Msg       *wsMessage `json:"msg,omitempty"`       // 事件帧（5/6/7）
	Auth      *wsAck     `json:"auth,omitempty"`      // auth ack
	Init      *wsInit    `json:"init,omitempty"`      // init 帧（心跳参数）
	Heartbeat *wsAck     `json:"heartbeat,omitempty"` // 心跳 ack
	Subscribe *wsAck     `json:"subscribe,omitempty"` // subscribe ack
}

// wsMessage msg 信封内层（type 5/6/7 事件）。
type wsMessage struct {
	Type           int             `json:"type"`
	SubscriptionID string          `json:"subscriptionId"`
	Data           json.RawMessage `json:"data"`
	Partial        []wsPatch       `json:"partial"`
}

// wsAck 通用 ack 帧（auth/heartbeat/subscribe 共用 status 字段）。
type wsAck struct {
	Status string `json:"status"`
}

// wsInit init 帧（心跳参数+订阅数）。
type wsInit struct {
	HeartbeatInterval  int `json:"heartbeatInterval"`
	HeartbeatTimeout   int `json:"heartbeatTimeout"`
	SubscriptionsCount int `json:"subscriptionsCount"`
}

type wsPatch struct {
	T string          `json:"t"`
	P string          `json:"p"`
	D json.RawMessage `json:"d"`
}
