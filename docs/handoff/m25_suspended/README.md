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
