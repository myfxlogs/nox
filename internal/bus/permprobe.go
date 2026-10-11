// permprobe.go —— D-193③：权限探针 boot 轮完成信号（非表非空判据——
// 零可探 venue 时表天然空，「等表非空」=首扫死等陷阱，票面钦定信号量径）。
package bus

import "sync"

var permProbeBootOnce sync.Once
var permProbeBootCh = make(chan struct{})

// MarkPermProbeBootDone cexscan 首轮扫描权限面落毕时调用（幂等——
// 每轮 scanOnce 均调，Once 保证只闭一次）。
func MarkPermProbeBootDone() { permProbeBootOnce.Do(func() { close(permProbeBootCh) }) }

// PermProbeBootDone 权限探针 boot 轮完成信号（oppmatch 首扫等待源）。
func PermProbeBootDone() <-chan struct{} { return permProbeBootCh }
