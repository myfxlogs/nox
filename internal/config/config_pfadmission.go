package config

// PfAdmitConfig D-491：PERP_FUNDING 证据准入评估器阈值族（嵌入 Config，字段访问不变）。
// 判据口径见 docs/D-491 设计 §2.2；默认值为设计定稿值。
type PfAdmitConfig struct {
	PfAdmitDirPct      float64 // 方向一致性下限（0..1，同 venue 持续高侧占比）
	PfAdmitMaxSd       float64 // pair 差 σ 上限（年化 pp）
	PfAdmitMinSamples  int     // 每腿窗内 funding 样本下限
	PfAdmitPinPct      float64 // 钉顶防护：任一腿 |均值| ≥ 即转 review
	PfAdmitMinDepthUsd float64 // 两腿顶档深度较小者下限（USD）
}
