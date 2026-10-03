package adapter

import "log"

// logSessionErr 会话采集错误日志（包级拆出防 import 环——log 单点）。
func logSessionErr(msg string) { log.Printf("[mt:sessions] %s", msg) }
