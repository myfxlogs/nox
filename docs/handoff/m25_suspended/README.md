# M2.5 回补挂起件（D-217③/D-215③）

M2.3 搬迁时因 import dashboard（未搬）而挂起的 autotrade 测试。`.go.txt` 后缀
防 `go build ./...` 误编；M2.5 dashboard 落地后去后缀回补 `internal/trading/autotrade/`。

- 钦定 10 件（D-217③清单）：d311_xasset / d328_carry_regime / d330_xasset_allow /
  d335_entry_gate / d406_rotate / d473_funding_hedge / engine_exit_events /
  engine_kick / engine_swaparb_margin_budget / funding_need
- 连带挂起（夹具在 dashboard 文件中，随 M2.5 零改回补）：
  - `d563_live_gate_test.go`（引用 d330/d335 夹具）
  - `d311_preintercept_part.txt`（d311 内 dashboard 依赖段切出，回补时并入原文件）
  - `d423_engine_part.txt`（d423 内 2 个引 d335 夹具的测试，回补时并入原文件）

## 回补完成（D-226）

- M2.5 已回补：58 函数对拍闭合（归档 autotrade 测试名集合 − 新仓
  trading/autotrade+risk/gate+portfolio/lifecycle = ∅）。函数级取件：
  - `TestD423_EntryGateFailClosed`/`TestD423_ReverseComboRejected` → `entry_gate_fail_closed_test.go`
  - `gateDecision` 纯函数段（非导出）→ `internal/risk/gate/xasset_skip_notional_test.go`
  - `d006LiftCanonicalCap`+preIntercept → `preintercept_gate_test.go`
  - fixture 独有件（d404SeedMonitorRowAt/d014FeeAny）→ `fixture_helpers_test.go`
  - `TestD311_VenueIsRealGate_CEXAndMT` 已在 gate 包（M2.3 拆搬时归位），免重复回补
- 差异项 1：新增 `TestD330_GateDecisionXAssetSkipNotional`（gate 包伴随件，原段在
  TestD330_GateSkipNotionalXAsset 内联——拆分固有名，文档在件）
- 本目录自此转纯史料存档，不再作为回补源。
