// earn.go —— D-286 金钱不眠：earn 族提案（cash_mgmt/earn_apr_lending）申购执行径。
// venue=binance simple-earn 活期：产品在售实查（APR/可购/min 三验）→坑表查→
// 敞口硬线（cex_exposure_pct）→现货可动全量申购→fin_dci_order opt_type='EARN'
// 同构账本行（settle 观察径 opt_type 过滤已剔除——earn 无交割终态）。
// 禁线：本金路径零 LLM；venue 返回实读零缓存决策；败=账本行+坑表留痕不吞。
package dciexec

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"nox/internal/platform/errclass"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// EarnProduct 活期在售品（binance /v1/simple-earn/flexible/list 行投影）。
type EarnProduct struct {
	ProductID   string
	Asset       string
	APR         float64 // latestAnnualPercentageRate 小数
	CanPurchase bool
	MinPurchase float64
}

// earnProducts 活期在售品实查（签名 GET；测试注 EarnProductsFn）。
func (e *Executor) earnProducts(ctx context.Context, ccy string) ([]EarnProduct, error) {
	if e.EarnProductsFn != nil {
		return e.EarnProductsFn(ctx, ccy)
	}
	q := fmt.Sprintf("asset=%s&current=1&size=50&timestamp=%d&recvWindow=5000",
		ccy, e.Now().UnixMilli())
	url := fmt.Sprintf("%s/v1/simple-earn/flexible/list?%s&signature=%s",
		strings.TrimRight(e.Sapi, "/"), q, sign(e.BnSec, q))
	b, err := e.get(ctx, url, true)
	if err != nil {
		return nil, err
	}
	var body struct {
		Rows []struct {
			ProductID   string `json:"productId"`
			Asset       string `json:"asset"`
			APR         string `json:"latestAnnualPercentageRate"`
			CanPurchase bool   `json:"canPurchase"`
			MinPurchase string `json:"minPurchaseAmount"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(b, &body); err != nil {
		return nil, fmt.Errorf("earn list 解析: %w", err)
	}
	var out []EarnProduct
	for _, r := range body.Rows {
		var p EarnProduct
		p.ProductID, p.Asset, p.CanPurchase = r.ProductID, r.Asset, r.CanPurchase
		fmt.Sscanf(r.APR, "%f", &p.APR)
		fmt.Sscanf(r.MinPurchase, "%f", &p.MinPurchase)
		out = append(out, p)
	}
	return out, nil
}

// earnSubscribe 活期申购写径（POST /v1/simple-earn/flexible/subscribe 签名；
// 测试注 EarnSubscribeFn）。返回 venue 回执原文。
func (e *Executor) earnSubscribe(ctx context.Context, productID string, amount float64) (string, error) {
	if e.EarnSubscribeFn != nil {
		return "", e.EarnSubscribeFn(ctx, productID, amount)
	}
	q := fmt.Sprintf("productId=%s&amount=%.8f&timestamp=%d&recvWindow=5000",
		productID, amount, e.Now().UnixMilli())
	url := fmt.Sprintf("%s/v1/simple-earn/flexible/subscribe?%s&signature=%s",
		strings.TrimRight(e.Sapi, "/"), q, sign(e.BnSec, q))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-MBX-APIKEY", e.BnKey)
	resp, err := e.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("subscribe HTTP %d: %s", resp.StatusCode, string(b))
	}
	return string(b), nil
}

// goEarn earn 提案执行：USDT 现货可动全量→活期申购（眠钱承接位）。
// 闸序：写径 venue/凭据 → 现货可动读（venue 实读）→ 产品在售+APR 复验 →
// 坑表抑单 → 幂等键申购。
// D-288：CEX 总占比敞口闸已撤（业主令「钱跟利走」）——改浓度透视 info 告警
// （单 venue≥50% 或 CEX 总≥70% 只标不拦）；其余硬线（CFD≥25%/−3%/事故仓/oz）不动。
func (e *Executor) goEarn(ctx context.Context, p *pgstore.Proposal) (string, error) {
	leg := gate.DCILeg{Name: "EARN", Tmpl: p.TemplateID}
	rail, ok := e.earnRail(p.Venue)
	if !ok {
		if _, has := e.EarnCreds[p.Venue]; !has && e.EarnRails == nil {
			return "", e.gap(ctx, leg, "credential", p.Venue+":earn",
				"请配置 %s 凭据（earn 申购写径）", p.Venue)
		}
		return "", fmt.Errorf("earn 写径未接 venue=%s（rail 缺席——如实不执行）", p.Venue)
	}
	// D-288 浓度透视（非闸）：单 venue≥50% 或 CEX 总≥70%→info 标。读败不阻塞申购。
	if pools, perr := e.Store.CapitalPoolSnapshots(ctx); perr != nil {
		log.Printf("[dciexec] 浓度透视读败（不阻塞）: %v", perr)
	} else {
		e.concentrationAlert(ctx, pools)
	}
	// 现货可动=申购额（D-192 venue 实读源；活期可赎=眠钱承接非锁仓）。
	// 余额源=rail 所在池（okx=unified 交易户 / gate=perp 户 / binance=spot）。
	spotFree, err := rail.FreeBalance(ctx, "USDT")
	if err != nil {
		return "", e.gap(ctx, leg, "runtime", "balance_read",
			"系统侧病灶：%s USDT 余额读败：%v", p.Venue, err)
	}
	if spotFree <= 0.00000001 {
		return "", fmt.Errorf("%s USDT 可动=0（无眠钱可申——如实不执行）", p.Venue)
	}
	// 产品在售+APR 复验：best flexible canPurchase && APR>0 && 量达 min。
	prods, err := rail.Products(ctx, "USDT")
	if err != nil {
		return "", e.gap(ctx, leg, "runtime", "earn_list",
			"系统侧病灶：%s 活期产品面读败：%v", p.Venue, err)
	}
	var pick *EarnProduct
	for i := range prods {
		pp := &prods[i]
		if !pp.CanPurchase || pp.APR <= 0 || pp.MinPurchase > spotFree {
			continue
		}
		if pick == nil || pp.APR > pick.APR {
			pick = pp
		}
	}
	if pick == nil {
		return "", fmt.Errorf("%s USDT 活期无可购档（在售面零达标——APR 复验如实拒）", p.Venue)
	}
	// 坑表抑单（op=earn_subscribe；读败 fail-closed 同申购例；venue 键按提案 venue）。
	pits, perr := e.Store.ListVenueRejects(ctx, p.Venue, "earn_subscribe")
	if perr != nil {
		return "", e.gap(ctx, leg, "runtime", "earn_subscribe",
			"系统侧病灶：earn 申购前坑表读败（fail-closed 不打 venue）：%v", perr)
	}
	for i := range pits {
		pp := &pits[i]
		if !pp.Active(e.Now()) || pp.Action == "fix" {
			continue
		}
		if aerr := e.Store.InsertAlertDedup(ctx, "critical", fmt.Sprintf(
			"venue 坑表抑单 %s:earn_subscribe:%s（earn 申购 hits=%d expires=%s）",
			p.Venue,
			pp.ErrorSignature, pp.Hits, pitExpiry(pp)), 24*time.Hour); aerr != nil {
			log.Printf("[dciexec] earn 坑表抑单 alert: %v", aerr)
		}
		return "", fmt.Errorf("earn 申购坑表抑单（%s 败因窗内——%s）", pp.ErrorSignature, pitExpiry(pp))
	}
	// 幂等键：earn|<ccy>|p<pid>|<attempt>——在途/已成同意图挡重发（D-255 同规）。
	reqPrefix := fmt.Sprintf("earn|USDT|p%d", p.ID)
	if open, oerr := e.Store.DCIOrderOpenIntent(ctx, reqPrefix); oerr != nil {
		return "", oerr
	} else if open {
		return "", fmt.Errorf("提案#%d earn 申购已受理（幂等防重）", p.ID)
	}
	attempt, err := e.Store.DCIOrderIntentCount(ctx, reqPrefix)
	if err != nil {
		return "", err
	}
	tenor := 0 // 活期无交割期——tenor=0 使 outstanding 谓词天然不命中
	oid, created, err := e.Store.CreateDCIOrder(ctx, &pgstore.DCIOrder{
		RequestID: fmt.Sprintf("%s|%d", reqPrefix, attempt+1),
		Venue:     p.Venue, TemplateID: p.TemplateID, ProductRef: pick.ProductID,
		Underlying: "USDT", OptType: "EARN", InvestCcy: "USDT",
		Qty: spotFree, TenorDays: &tenor, ProposalID: &p.ID,
	})
	if err != nil {
		return "", err
	}
	if !created {
		return "", fmt.Errorf("提案#%d earn 申购已受理（request_id 幂等命中）", p.ID)
	}
	body, err := rail.Subscribe(ctx, pick.ProductID, spotFree)
	if err != nil {
		e.finish(ctx, oid, "failed", err.Error())
		sig := errclass.VenueRejectSignature(err.Error())
		if lerr := e.Store.LearnVenueReject(ctx, p.Venue, "earn_subscribe",
			sig, 24*time.Hour); lerr != nil {
			log.Printf("[dciexec] earn 坑表落行败: %v", lerr)
		}
		kb := e.kbSuffix(sig) // 拒单消费闸：坑签名命中经验库→教训贴告警脸
		if venueErrKind(err) == "permission" {
			return "", e.gap(ctx, leg, "permission", "earn_subscribe:"+p.Venue,
				"请开通 %s 赚币活期申购权限（申购败：%v）%s", p.Venue, err, kb)
		}
		return "", e.gap(ctx, leg, "runtime", "earn_subscribe",
			"系统侧病灶：earn 申购 venue 拒单：%v%s", err, kb)
	}
	e.finish(ctx, oid, "filled", body)
	if err := e.Store.SetProposalStatus(ctx, p.ID, "executed"); err != nil {
		return "", fmt.Errorf("提案态机推进: %w", err)
	}
	// 成交回执升 warn 同 DCI 腿径——info 级被 feishu 滤噪闸拦不推（earn 成交必达）。
	if err := e.Store.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
		"EARN %s 申购成交：%.4f USDT→%s APR %.2f%%（单号 %d）",
		p.TemplateID, spotFree, pick.ProductID, pick.APR*100, oid)); err != nil {
		log.Printf("[dciexec] earn 成交回执 alert 落行败: %v", err)
	}
	return fmt.Sprintf("%s %s 申购 %.4f USDT→%s（APR %.2f%%）",
		p.TemplateID, pick.Asset, spotFree, pick.ProductID, pick.APR*100), nil
}

// concentrationAlert D-288 浓度透视（非闸）：单 venue 权益≥50% 或 CEX 总≥70%→
// info alert 如实标，零拦截。阈值=D-288 字面量（业主令裁撤硬闸的保留透视面）。
func (e *Executor) concentrationAlert(ctx context.Context, pools []pgstore.CapitalPoolRow) {
	var totEq float64
	venueEq := map[string]float64{}
	cexEq := 0.0
	var stalePools []string
	for _, r := range pools {
		if r.Stale && !r.Ghost {
			stalePools = append(stalePools, r.PoolKey()) // 分母剔陈旧池——不拿陈旧值当现值
			continue
		}
		if r.Ghost || r.Equity <= 0 {
			continue
		}
		totEq += r.Equity
		venueEq[r.Venue] += r.Equity
		if r.VenueType != "mt4" && r.VenueType != "mt5" {
			cexEq += r.Equity
		}
	}
	if len(stalePools) > 0 {
		sort.Strings(stalePools)
		if aerr := e.Store.InsertAlertDedup(ctx, "info", fmt.Sprintf(
			"分母池鲜度失：浓度透视分母缺 %s（陈旧快照剔计，恢复自动销）",
			strings.Join(stalePools, ",")), 24*time.Hour); aerr != nil {
			log.Printf("[dciexec] 分母鲜度 alert: %v", aerr)
		}
	}
	if totEq <= 0 {
		return
	}
	var hot []string
	for v, eq := range venueEq {
		if pct := eq / totEq * 100; pct >= 50 {
			hot = append(hot, fmt.Sprintf("%s=%.1f%%", v, pct))
		}
	}
	if pct := cexEq / totEq * 100; pct >= 70 {
		hot = append(hot, fmt.Sprintf("CEX总=%.1f%%", pct))
	}
	if len(hot) == 0 {
		return
	}
	sort.Strings(hot)
	missNote := ""
	if len(stalePools) > 0 {
		missNote = "｜分母缺池 " + strings.Join(stalePools, ",")
	}
	if err := e.Store.InsertAlertDedup(ctx, "info", fmt.Sprintf(
		"浓度透视：%s%s（D-288 透视面——只标不拦）", strings.Join(hot, " "), missNote), 24*time.Hour); err != nil {
		log.Printf("[dciexec] 浓度透视 alert: %v", err)
	}
}
