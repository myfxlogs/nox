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

// 首个 cexscan DCI 采集拍完成信号（提案断点续执等待源——事件式非裸 sleep）。
var dciBootOnce sync.Once
var dciBootCh = make(chan struct{})

// MarkDCIBootDone cexscan 首轮 DCI 采集入库毕调用（幂等；扫描器缺亦须兜底标）。
func MarkDCIBootDone() { dciBootOnce.Do(func() { close(dciBootCh) }) }

// DCIBootDone DCI 采集 boot 拍完成信号。
func DCIBootDone() <-chan struct{} { return dciBootCh }
