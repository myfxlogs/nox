// state.go 水位文件 .digest_state（S3.4，幂等重跑零重复）。
// 记两表最大 id；JSON 格式；git 跟踪。
package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// digestState 水位状态（两表最大已处理 id）。
type digestState struct {
	AutotradeLogsMaxID int64 `json:"autotrade_logs_max_id"`
	AlertsMaxID        int64 `json:"alerts_max_id"`
}

// loadState 读取水位文件；不存在 → 零值（首次运行处理全量）。
func loadState(path string) (*digestState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &digestState{}, nil // 首次运行，零水位
		}
		return nil, fmt.Errorf("read state %s: %w", path, err)
	}
	var s digestState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("unmarshal state: %w", err)
	}
	return &s, nil
}

// saveState 写水位文件（幂等：重跑零重复）。
func saveState(path string, s *digestState) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}
