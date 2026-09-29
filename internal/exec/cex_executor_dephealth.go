// cex_executor_dephealth.go —— D-579 S3：CEX REST 平面 dephealth 记账挂点。
// e.get/e.post/各处 e.client.Do 全经同一 http.Client.Transport——包装
// RoundTripper 一处收口（err→fail 事实记账，HTTP 响应到达=ok 不论状态码）。
package exec

import (
	"net/http"

	"arb/internal/dephealth"
)

// depRoundTripper 透传 RT + dephealth 记账（venue:venueType:rest 键）。
type depRoundTripper struct {
	key string
	rt  http.RoundTripper
}

func (d depRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := d.rt.RoundTrip(req)
	dephealth.Mark(d.key, err == nil, err)
	return resp, err
}

// wrapDepHealth 构造点挂记账 RT（幂等——重复包装只叠记账同键零害，不取）。
func wrapDepHealth(venue, venueType string, c *http.Client) *http.Client {
	rt := c.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	cp := *c
	cp.Transport = depRoundTripper{key: venue + ":" + venueType + ":" + dephealth.PlaneREST, rt: rt}
	return &cp
}
