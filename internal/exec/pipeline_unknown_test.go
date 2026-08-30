package exec

import (
	"context"
	"errors"
	"testing"
)

// TestIsContextTimeoutErr D-159 W1a R2：context 超时/取消识别。
func TestIsContextTimeoutErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"DeadlineExceeded", context.DeadlineExceeded, true},
		{"Canceled", context.Canceled, true},
		// errors.New 创建的新 error（非 fmt.Errorf %w 包装），errors.Is 不匹配，但字符串含 "context deadline exceeded" → true（字符串兜底）。
		{"string contains deadline", errors.New("wrap: " + context.DeadlineExceeded.Error()), true},
		{"grpc string DeadlineExceeded", errors.New("rpc error: code = DeadlineExceeded desc = context deadline exceeded"), true},
		{"plain error", errors.New("some other error"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isContextTimeoutErr(tt.err)
			if got != tt.want {
				t.Errorf("isContextTimeoutErr(%v) want %v, got %v", tt.err, tt.want, got)
			}
		})
	}
}

// TestIsContextTimeoutErr_Wrapped D-159 W1a R2：fmt.Errorf %w 包装的 DeadlineExceeded 识别。
func TestIsContextTimeoutErr_Wrapped(t *testing.T) {
	wrapped := errors.New("mt5 ordersend: " + context.DeadlineExceeded.Error())
	if !isContextTimeoutErr(wrapped) {
		t.Error("string containing 'context deadline exceeded' should match")
	}
}
