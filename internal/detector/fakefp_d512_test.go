// fakefp_d512_test.go —— D-512 T6：detector_test.go 退役（纯 CrossVenue 矩阵），共享测试 helper fakeFP 迁此保留（xasset 系列测试消费）。
package detector

type fakeFP struct {
	funding map[string]map[string]float64 // D-300：XASSET_CARRY 用
	swap    map[string]map[string][2]float64
	modes   map[string]map[string]int64
	isReal  map[string]*bool // D-178
	simKeys map[string]bool  // D-369-R2：sim venue 键集
}

func (f fakeFP) FundingFacts() map[string]map[string]float64 { return f.funding }
func (f fakeFP) SimVenueKeys() map[string]bool               { return f.simKeys } // D-369-R2
func (f fakeFP) SwapFacts() map[string]map[string][2]float64 { return f.swap }
func (f fakeFP) TradeModes() map[string]map[string]int64     { return f.modes }
func (f fakeFP) IsRealMap() map[string]*bool                 { return f.isReal } // D-178
