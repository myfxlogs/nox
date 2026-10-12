package pgstore

import (
	"errors"
	"net"
	"strings"
)

// isTransientConnErr 连接层瞬断判定（D-244）——仅网络/conn 类真瞬断：
// *net.OpError 全覆盖（read tcp ...: bad file descriptor / connection reset /
// broken pipe 均属此型）；外加 pgx/服务端 conn kill 文案。凭据、schema、SQL 真错
// 一律 false 不重试不误掩。
func isTransientConnErr(err error) bool {
	if err == nil {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	msg := err.Error()
	for _, s := range []string{
		"bad file descriptor",
		"connection reset",
		"broken pipe",
		"unexpected EOF",
		"server closed the connection",
		"terminating connection",
		"too many clients",
		"the database system is starting up",
		"the database system is shutting down",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
