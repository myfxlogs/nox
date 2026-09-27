// fetch_cot.go —— D-528 S2：CFTC COT 周报采集（移植 ark cot/fetcher.go，stdlib 重写）。
// 主路=Socrata futures-only JSON（TFF=gpe5-46if，DISAGGREGATED=72hh-3qpy），$where 合约集
// 过滤+report_date 降序取每合约最新周；主路失败回退 CFTC bulk CSV（deafut.txt，legacy
// combined 口径：noncomm/comm 两类）。
// 行语义：category 级净额行——net_position=多-空，net_change=API WoW 变化（多-空同口径相减），
// direction 按净额符号；pct_index/momentum/conviction v1 暂空（原始字段先落地，合成后续票）。
package macro

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"arb/internal/store/pgstore"
)

// 端点三件（var=测试 httptest 注入点；生产零改动）。
var (
	socrataTFF = "https://publicreporting.cftc.gov/resource/gpe5-46if.json"
	socrataDIS = "https://publicreporting.cftc.gov/resource/72hh-3qpy.json"
	cftcCSV    = "https://www.cftc.gov/dea/newcot/deafut.txt"
)

// socrataRow Socrata futures-only 端点行（json tag=CFTC 数据集列名真值，照 ark 实测结构；
// swap short 列双下划线 swap__positions_short_all 为上游 API quirk 原样保留）。
type socrataRow struct {
	ReportDate   string `json:"report_date_as_yyyy_mm_dd"`
	ContractCode string `json:"cftc_contract_market_code"`
	OpenInterest string `json:"open_interest_all"`

	DealerPositionsLong  string `json:"dealer_positions_long_all"`
	DealerPositionsShort string `json:"dealer_positions_short_all"`
	AssetMgrLong         string `json:"asset_mgr_positions_long"`
	AssetMgrShort        string `json:"asset_mgr_positions_short"`
	LevMoneyLong         string `json:"lev_money_positions_long"`
	LevMoneyShort        string `json:"lev_money_positions_short"`

	ChangeDealerLong    string `json:"change_in_dealer_long_all"`
	ChangeDealerShort   string `json:"change_in_dealer_short_all"`
	ChangeAssetMgrLong  string `json:"change_in_asset_mgr_long"`
	ChangeAssetMgrShort string `json:"change_in_asset_mgr_short"`
	ChangeLevMoneyLong  string `json:"change_in_lev_money_long"`
	ChangeLevMoneyShort string `json:"change_in_lev_money_short"`

	ProdMercLong      string `json:"prod_merc_positions_long"`
	ProdMercShort     string `json:"prod_merc_positions_short"`
	SwapLong          string `json:"swap_positions_long_all"`
	SwapShort         string `json:"swap__positions_short_all"`
	MMoneyLong        string `json:"m_money_positions_long_all"`
	MMoneyShort       string `json:"m_money_positions_short_all"`
	ChangeProdMercLon string `json:"change_in_prod_merc_long"`
	ChangeProdMercSh  string `json:"change_in_prod_merc_short"`
	ChangeSwapLong    string `json:"change_in_swap_long_all"`
	ChangeSwapShort   string `json:"change_in_swap_short_all"`
	ChangeMMoneyLong  string `json:"change_in_m_money_long_all"`
	ChangeMMoneyShort string `json:"change_in_m_money_short_all"`
}

// FetchCot 拉全品种最新周 COT 净持产行（Socrata 主路，失败回退 CSV）。
func FetchCot(ctx context.Context, client *http.Client) ([]pgstore.CotPosition, error) {
	rows, sErr := fetchCotSocrata(ctx, client)
	if sErr == nil && len(rows) > 0 {
		return rows, nil
	}
	log.Printf("[macro:cot] socrata failed (err=%v rows=%d), fallback to CFTC CSV", sErr, len(rows))
	csvRows, cErr := fetchCotCSV(ctx, client)
	if cErr != nil {
		return nil, fmt.Errorf("cot: both socrata (%v) and csv (%v) failed", sErr, cErr)
	}
	return csvRows, nil
}

// fetchCotSocrata TFF+DISAGGREGATED 两报告各拉一次，每合约取 report_date 最新行。
func fetchCotSocrata(ctx context.Context, client *http.Client) ([]pgstore.CotPosition, error) {
	byReport := map[string][]CotContract{}
	for _, c := range cotContracts {
		byReport[c.Report] = append(byReport[c.Report], c)
	}
	endpoints := map[string]string{"TFF": socrataTFF, "DISAGGREGATED": socrataDIS}

	var out []pgstore.CotPosition
	var errs []error
	for report, contracts := range byReport {
		rows, err := fetchCotReport(ctx, client, endpoints[report], contracts)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", report, err))
			continue
		}
		out = append(out, rows...)
	}
	if len(out) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("all socrata reports failed: %v", errs)
	}
	return out, nil
}

func fetchCotReport(ctx context.Context, client *http.Client, endpoint string, contracts []CotContract) ([]pgstore.CotPosition, error) {
	codes := make([]string, len(contracts))
	byCode := make(map[string]CotContract, len(contracts))
	for i, c := range contracts {
		codes[i] = fmt.Sprintf("'%s'", c.Code)
		byCode[c.Code] = c
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Add("$where", fmt.Sprintf("cftc_contract_market_code in(%s)", strings.Join(codes, ",")))
	q.Add("$order", "report_date_as_yyyy_mm_dd DESC")
	q.Add("$limit", strconv.Itoa(len(contracts)*2))
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var raw []socrataRow
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	var out []pgstore.CotPosition
	for _, r := range raw {
		c, ok := byCode[r.ContractCode]
		if !ok || seen[r.ContractCode] {
			continue // 降序去重：首见=该合约最新周
		}
		date, err := time.Parse("2006-01-02", r.ReportDate)
		if err != nil {
			log.Printf("[macro:cot] skip %s: bad report_date %q", r.ContractCode, r.ReportDate)
			continue
		}
		seen[r.ContractCode] = true
		out = append(out, socrataToRows(date, c, r)...)
	}
	return out, nil
}

// socrataToRows 单合约→分类净额行（TFF 三类/DISAGG 三类；raw 存原行 JSON）。
func socrataToRows(date time.Time, c CotContract, r socrataRow) []pgstore.CotPosition {
	rawJSON, _ := json.Marshal(r)
	row := func(category string, long, short, chgLong, chgShort string) pgstore.CotPosition {
		net := socrataI(long) - socrataI(short)
		chg := socrataI(chgLong) - socrataI(chgShort)
		return pgstore.CotPosition{
			ReportDate: date, Instrument: c.Symbol, Category: category,
			Direction: directionOf(net), NetPosition: net, NetChange: chg, Raw: rawJSON,
		}
	}
	if c.Report == "TFF" {
		return []pgstore.CotPosition{
			row("lev_fund", r.LevMoneyLong, r.LevMoneyShort, r.ChangeLevMoneyLong, r.ChangeLevMoneyShort),
			row("asset_mgr", r.AssetMgrLong, r.AssetMgrShort, r.ChangeAssetMgrLong, r.ChangeAssetMgrShort),
			row("dealer", r.DealerPositionsLong, r.DealerPositionsShort, r.ChangeDealerLong, r.ChangeDealerShort),
		}
	}
	return []pgstore.CotPosition{
		row("managed_money", r.MMoneyLong, r.MMoneyShort, r.ChangeMMoneyLong, r.ChangeMMoneyShort),
		row("prod_merc", r.ProdMercLong, r.ProdMercShort, r.ChangeProdMercLon, r.ChangeProdMercSh),
		row("swap", r.SwapLong, r.SwapShort, r.ChangeSwapLong, r.ChangeSwapShort),
	}
}

// fetchCotCSV CFTC bulk CSV 兜底（deafut.txt：legacy 无表头固定位格式）。
// 列位真值经 socrata 同周 JPY 行交叉校准（2026-09-22 对拍：TotRept_L=NC_L+NC_Spr+C_L 精确成立）：
// [0]市场名 [1]YYMMDD [2]YYYY-MM-DD [3]合约码 [4-6]市场/区域/商品码 [7]OI
// [8]NC_L [9]NC_S [10]NC_Spr [11]C_L [12]C_S [13]TotRept_L [14]TotRept_S [15]NR_L [16]NR_S
// [17-26]重复块（OI+九位） [27-36]零块 [37]OI变动 [38-46]九类变动（NC_L/NC_S/NC_Spr/C_L/C_S/TotL/TotS/NR_L/NR_S）。
// 列位锚：chg_OI@[37]=-164101 与 socrata change_in_open_interest_all 同周精确相等。
// 注意：本文件 NONCOMM/COMM 为 CFTC legacy 自有归类（非 TFF 类别算术映射），v1 原样落地。
func fetchCotCSV(ctx context.Context, client *http.Client) ([]pgstore.CotPosition, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cftcCSV, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("csv status %d", resp.StatusCode)
	}
	reader := csv.NewReader(resp.Body)
	reader.LazyQuotes = true
	reader.FieldsPerRecord = -1 // 行内列数在末尾引号段前后有抖动，按行内可用列防御取位
	var out []pgstore.CotPosition
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(row) < 47 {
			continue
		}
		c, ok := cotByCode[strings.TrimSpace(row[3])]
		if !ok {
			continue
		}
		date, err := time.Parse("2006-01-02", strings.TrimSpace(row[2]))
		if err != nil {
			continue
		}
		rawJSON, _ := json.Marshal(row)
		cat := func(category string, l, s, cl, cs int) pgstore.CotPosition {
			net := csvI(row[l]) - csvI(row[s])
			chg := csvI(row[cl]) - csvI(row[cs])
			return pgstore.CotPosition{
				ReportDate: date, Instrument: c.Symbol, Category: category,
				Direction: directionOf(net), NetPosition: net, NetChange: chg, Raw: rawJSON,
			}
		}
		out = append(out,
			cat("noncomm", 8, 9, 38, 39),
			cat("comm", 11, 12, 41, 42),
			cat("nonrept", 15, 16, 45, 46))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("csv parsed 0 rows for tracked contracts")
	}
	return out, nil
}

// socrataI/csvI 数值解析（去千分位；脏值=0 不中断，KB 静默冻结教训由上游状态码/空结果响铃兜底）。
func socrataI(s string) int64 {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

func csvI(s string) int64 { return socrataI(s) }

// directionOf 净额符号→方向。
func directionOf(net int64) string {
	switch {
	case net > 0:
		return "LONG"
	case net < 0:
		return "SHORT"
	default:
		return "FLAT"
	}
}
