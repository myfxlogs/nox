// account_event_snapshot.go —— 3d-P1#1b：MT 账户帧事件 → fin_venue_account_snapshot 写口
// （src=mt_frame）。链路：MT5 OnOrderUpdate 余额帧 → AccountMonitor patch+flush →
// acctTopic.AccountEvent（flush 内已 proto.Equal 去重，事件即变化帧）→ 本文件落库。
// 业主令（2026-10-01）：所有账户拒绝快照延时信息——cfd/MT 键周期补写与 mt_event 订单
// 双写全部退役；margin_guard 经 snapHook 随帧评估（评估触发源=帧事件，输入口径不变）。
// 落库侧仍做 1s/venue 合流（帧率高于 DB 有意义粒度；终值由 pending 定时器补写不丢）
// 与值相同不写（节流内连续帧同值零写），CEX 键跳过（cex_event_cache 已有事件写口）。
package cexscan

import (
	"context"
	"fmt"
	"log"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/keydomain"
	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/store/pgstore"
)

// acctFrameWriteInterval 同 venue 快照落库合流窗（帧率风暴吸收；终值 pending 补写不丢）。
const acctFrameWriteInterval = 1 * time.Second

// orderEventStore 事件落库所需 store 面（*pgstore.Store 原生实现；D-383 B1 原命名保留）。
type orderEventStore interface {
	BrokerRealAccountKeys(ctx context.Context) (map[string]bool, error)
	InsertVenueAccountSnapshots(ctx context.Context, snaps []pgstore.VenueAccountSnapshot) error
}

// acctFrameState 单 venue 落库态（goroutine 私有，无锁）。
type acctFrameState struct {
	wrote       bool      // 是否已写过至少一行
	at          time.Time // 上次实际写入时刻
	bal, eq, fm float64   // 上次写入值（六字段快照列三值，值相同不写）
	timer       *time.Timer
}

// StartAccountEventSnapshots 订阅 acctTopic：cfd/MT AccountEvent → 该 venue 快照行即写
// （src=mt_frame，fetched_at=事件时刻）。合流规则：距上次写 <1s 置 pending 由定时器补写
// 终值；bal/eq/fm 与上次写相同则不写。CEX 键跳过。阻塞至 ctx 取消（goroutine 装配）。
func StartAccountEventSnapshots(ctx context.Context, topic *bus.Topic[*v1.AccountEvent], store orderEventStore, am AccountSnapshotter) {
	ch, cancel := topic.Subscribe()
	defer cancel()
	pend := make(chan string, 64) // per-venue 补写通知（buffer 远大于 venue 数，实际不丢）
	states := map[string]*acctFrameState{}
	writeVenue := func(venue string, st *acctFrameState) {
		rows, err := mtFrameRows(ctx, store, am, venue, time.Now())
		if err != nil {
			log.Printf("[acct-snapshot] %v", err)
			return
		}
		if len(rows) == 0 {
			return
		}
		r := rows[0]
		if st.wrote && st.bal == r.BalanceRaw && st.eq == r.EquityRaw && st.fm == r.FreeMarginRaw {
			return // 值相同不写（节流窗内连续同值帧零写）
		}
		if err := store.InsertVenueAccountSnapshots(ctx, rows); err != nil {
			log.Printf("[acct-snapshot] insert %s: %v", venue, err)
			return
		}
		st.wrote, st.at = true, time.Now()
		st.bal, st.eq, st.fm = r.BalanceRaw, r.EquityRaw, r.FreeMarginRaw
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			info := ev.GetAccount()
			vk := info.GetVenueKey()
			// 跳过判据：AssetType=="cex" 为权威（部署实证 18:08：IsCexVenueKey 只认
			// :perp/:spot，binance:funding / okx:unified/earn/funding 键被当 MT 写出
			// src=mt_frame 误行）；IsCexVenueKey 保留作第二道（AssetType 缺省时的键侧兜）。
			if vk == "" || info.GetAssetType() == "cex" || bus.IsCexVenueKey(vk) {
				continue
			}
			venue := keydomain.BareOf(vk)
			st := states[venue]
			if st == nil {
				st = &acctFrameState{}
				states[venue] = st
			}
			if st.wrote && time.Since(st.at) < acctFrameWriteInterval {
				// 合流窗内：补写定时器兜底终值（值相同由 writeVenue 内部去重短路）。
				if st.timer == nil {
					v := venue
					st.timer = time.AfterFunc(acctFrameWriteInterval-time.Since(st.at), func() {
						select {
						case pend <- v:
						default:
						}
					})
				}
				continue
			}
			writeVenue(venue, st)
		case venue := <-pend:
			st := states[venue]
			if st == nil || st.timer == nil {
				continue
			}
			st.timer = nil
			if d := acctFrameWriteInterval - time.Since(st.at); d > 0 {
				// 早到补写（lastWrite 被其他路径推进）→ 重排剩余窗。
				v := venue
				st.timer = time.AfterFunc(d, func() {
					select {
					case pend <- v:
					default:
					}
				})
				continue
			}
			writeVenue(venue, st)
		}
	}
}

// mtFrameRows 读 AccountMonitor 内存视图按 venue 过滤 → buildVenueSnapshotRows 同口径
// 构造（real 过滤+归一+去重），src=mt_frame + fetched_at=帧时刻。视图未含该 venue 或
// realKeys 失败 → 空/error（fail-closed 不落）。与退役的 writeVenueEventSnapshot 同口径。
func mtFrameRows(ctx context.Context, store orderEventStore, am AccountSnapshotter, venue string, ts time.Time) ([]pgstore.VenueAccountSnapshot, error) {
	realKeys, err := store.BrokerRealAccountKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("acct frame snapshot: broker real keys: %w（fail-closed 本轮不落）", err)
	}
	var mine []AccountSnapshot
	for _, sn := range ScanAccounts(am) {
		if sn.AssetType == "cex" { // 事件侧已拦，行构造双保险（bare venue 撞名防御）
			continue
		}
		if sn.Venue == venue {
			mine = append(mine, sn)
		}
	}
	rows := buildVenueSnapshotRows(mine, realKeys)
	out := rows[:0]
	for i := range rows {
		// D-609：零/负余额帧=断连 nil-acct 伪影（真实 MT 账户余额>0）——拒落防
		// 零↔真交替污染差分与 latest 行读面（belt；差分读径 balance_raw>0 同拦=suspenders）。
		if rows[i].BalanceRaw <= 0 {
			continue
		}
		rows[i].Src = "mt_frame"
		rows[i].FetchedAt = ts
		out = append(out, rows[i])
	}
	return out, nil
}
