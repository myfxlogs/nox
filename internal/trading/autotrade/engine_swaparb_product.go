package autotrade

// sameProductClass D-558：产品维度配对校验纯函数（等价键=canonical 产品类）。
// 血统：事故 470（FxPro BRENT cfd_spot × PXBT BRENT cfd_leverage，价基差 ~270bps
// 假套利）证明配对需产品维度闸；D-173 首版拿 broker 本地 CalcMode 组名当产品身份
// ——SG 实拍同品跨所异组（XAUUSD=fx/cfd_spot/cfd_leverage 三归法，110660304 连
// BTCUSDT 现货都归 cfd_leverage）致跨所配对全灭，组名=房规目录非产品身份。
// 现锚 canonical 产品类（fin_canonical_product_class 表，DB 判据）：
// 两腿类同 → 放行（同 canonical 异组放行）；任一腿未归类/类异 → 拒
// （fail-closed：未裁定产品面禁配对，禁粗到全放行；类异=真异品拒，470 防护语义保留）。
func sameProductClass(longClass, shortClass string) bool {
	return longClass != "" && longClass == shortClass
}
