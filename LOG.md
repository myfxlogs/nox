
## MT4 退役（尸件清扫）@7d6fe99a

**分类清单（逐文件三档）**

①写边界→显式拒：`pgstore/brokers.go InsertBrokerAccount`（platform!=1 拒——RPC 层 D-470 已拒、store 层纵深堵）；`service_xasset_combos.go` comboVenueTypes 撤 mt4（写白名单拒 :mt4）；`service_backfill.go` splitVenueKey 拒 venue:mt4；`broker_automap_sweep.go` seedAssetEquivVenue platform!=1 fail-closed；`scan/graduate_resolve.go` platform!=1 fail-closed（不再产 mt4 venueKey）。

②mt4 专属死径→删：`errclass.ClassifyMT4` 码表+mt4 分支撤（mt4 键→Abort 兜底）；`engine_swaparb_rate` 后缀候选撤 mt4；combo_allow 注释域值对齐。

③枚举/CHECK/历史读面→保留：`keydomain.IsMT`/IsMTKey；`brokerVenueType`（存量 platform 行如实渲染）；`legMarket`/`cfdComboLegs`/`mtVenueTypeOf`/`slipForLeg` 等存量键域读径；`migrations/1000_baseline` `venue_type_chk('mt4','mt5')` 不动；proto/生成件枚举注释；数据适配层共享 MT 网关件。白名单：残余 mt4 grep 命中全为上述③类+测试夹具。

**验收锚**：`TestMT4Retire_InsertRejects`（store 层 platform 0/2 全拒）+ `TestMT4Retire_ParseRejects`（backfill 键+组合后缀双拒 mt4/过 mt5）+ D-470 RPC 三态原锚全绿。机检：build/vet/gofmt 净，`go test ./...` 全绿（dashboard 一例 bad file descriptor 共享测试库压力型 flake，隔离复跑绿），-race errclass/scan/exec/autotrade/pgstore 绿，check-lines 零警告，import 方向 221/222，env-reads/迁移契约（零迁移）过。
