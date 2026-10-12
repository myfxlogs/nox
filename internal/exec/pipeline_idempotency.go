package exec

import (
	"context"
	"fmt"
	"log"
)

// checkRequestIdIdempotency D-195-B §4.1：request_id 幂等检查。
// 重复提交返回已有 position，不新建。requestID 为空时跳过（向后兼容）。
// 返回 (*ExecuteResult, true) 表示命中幂等（调用方直接返回）；
// 返回 (nil, false) 表示未命中（继续新建流程）。
func (p *Pipeline) checkRequestIdIdempotency(ctx context.Context, requestID string) (*ExecuteResult, bool, error) {
	if requestID == "" {
		return nil, false, nil
	}
	existing, err := p.store.GetPositionByRequestID(ctx, requestID)
	if err != nil {
		return nil, false, fmt.Errorf("request_id idempotency check: %w", err)
	}
	if existing != nil {
		log.Printf("[exec] request_id=%s already exists as position=%d, returning existing", requestID, existing.ID)
		return &ExecuteResult{PositionID: existing.ID, Status: existing.Status}, true, nil
	}
	return nil, false, nil
}
