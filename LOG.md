
## 能力账本凭据归一病灶 @7e143a26

- #6 根因修：`capability.go` broker 凭据行 `VenueType` 原硬归一 `"cfd"` → 改注册真值 `mt5`（`ledgerView` 由真值产 `type:mt5` 键，`credential:mt5` 命中）；`asset_class=cfd` 语义经 `Tags` 保留。grep 全库确认零 `subject:cfd`/`type:cfd` 消费面。
- #11 判非病灶：`term_basis` 模板语义=现货×远月期（交割期货腿），`okx:futures` subject 正确——okx 在册仅 perp 市场线，缺口如实非误报。注册 `okx:futures` 市场线属委员会/venue 配置决策，票面「裁定不明先留痕」照办未改。
- 锚测：`TestCredTypeTrueValue`（5434 实库 broker 行 venue_type 真值+cfd tag 保留）+ `TestCredMT5_TypeKey`（ledgerView 三级键+credential:mt5 端到端命中）。
- 机检：build/vet/gofmt 净；pgstore+oppmatch 全包绿、oppmatch race 绿；check-lines 零警告；import 方向 221/222；env-reads/迁移契约（零迁移）过。
- 部署验收（SG）：下拍 reconcile 后请求箱 #6 自动收口（fulfilled 出集）；#11 保持 requested 如实缺口。
