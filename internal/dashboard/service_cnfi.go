package dashboard

// CN-FI 固收雷达快照（D-226/D-237；规格包 v0.2 §2）。
// 服务端计算（D-043：前端只渲染）；快照语义非实时 WS；响应零金额字段。

import (
	"context"

	"arb/internal/store/pgstore"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// cnfiExpect 期望点全集（P1 范围三曲线×六期限；缺行=missing，不虚构）。
var cnfiExpect = func() [][2]string {
	var out [][2]string
	for _, s := range []string{"cgb", "ig_aaa", "bank_aaa"} {
		for _, t := range []string{"3m", "1y", "3y", "5y", "10y", "30y"} {
			out = append(out, [2]string{s, t})
		}
	}
	return out
}()

func (s *Service) GetCNFIRadarSnapshot(ctx context.Context, req *connect.Request[v1.GetCNFIRadarSnapshotRequest]) (*connect.Response[v1.GetCNFIRadarSnapshotResponse], error) {
	resp := &v1.GetCNFIRadarSnapshotResponse{
		GeneratedAt: timestamppb.Now(),
	}
	// 最新点：每个 (series,tenor) 取最新一行（D-234 索引 cnfi_series_lookup_idx 命中）。
	type latestRow struct {
		Series, Tenor, AsOf, Source string
		ValueBP                     float64
	}
	rows := make(map[string]latestRow)
	rs, err := s.store.Pool().Query(ctx, `
		SELECT DISTINCT ON (series, tenor) series, tenor, value_bp::float8, as_of::text, source
		FROM cnfi_series WHERE tenor <> ''
		ORDER BY series, tenor, as_of DESC`)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	for rs.Next() {
		var r latestRow
		if err := rs.Scan(&r.Series, &r.Tenor, &r.ValueBP, &r.AsOf, &r.Source); err != nil {
			return nil, err
		}
		rows[r.Series+"|"+r.Tenor] = r
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}

	snapshotAsOf := ""
	get := func(series, tenor string) (latestRow, bool) {
		r, ok := rows[series+"|"+tenor]
		return r, ok
	}
	for _, k := range cnfiExpect {
		r, ok := get(k[0], k[1])
		if ok && (snapshotAsOf == "" || r.AsOf > snapshotAsOf) {
			snapshotAsOf = r.AsOf
		}
	}
	// 单遍填点（snapshot_as_of 已定）：有行=ok/stale，无行=missing。
	for _, k := range cnfiExpect {
		r, ok := get(k[0], k[1])
		if !ok {
			resp.Points = append(resp.Points, &v1.CnfiCurvePoint{
				Series: k[0], Tenor: k[1], Quality: "missing",
			})
			continue
		}
		q := "ok"
		if r.AsOf < snapshotAsOf {
			q = "stale"
		}
		resp.Points = append(resp.Points, &v1.CnfiCurvePoint{
			Series: k[0], Tenor: k[1], ValueBp: r.ValueBP, AsOf: r.AsOf, Source: r.Source, Quality: q,
		})
	}
	resp.SnapshotAsOf = snapshotAsOf

	// 派生指标（同日对齐：任一腿缺/非同日 → 当日指标缺失，不插值不前向填充，规格 §3.2）。
	cgb2, ok2 := get("cgb", "2y")
	cgb10, ok10 := get("cgb", "10y")
	if ok2 && ok10 && cgb2.AsOf == cgb10.AsOf {
		resp.Metrics = append(resp.Metrics, &v1.CnfiDerivedMetric{
			Key: "term_spread_10y2y", Value: cgb10.ValueBP - cgb2.ValueBP, Unit: "bp",
			SampleN: cnfiDerivedSampleN(ctx, s.store, "cgb", "2y", "cgb", "10y"),
			AsOf:    cgb10.AsOf, Quality: "ok",
		})
	}
	ig3, okIG := get("ig_aaa", "3y")
	cgb3, okC3 := get("cgb", "3y")
	if okIG && okC3 && ig3.AsOf == cgb3.AsOf {
		resp.Metrics = append(resp.Metrics, &v1.CnfiDerivedMetric{
			Key: "credit_spread_3y", Value: ig3.ValueBP - cgb3.ValueBP, Unit: "bp",
			SampleN: cnfiDerivedSampleN(ctx, s.store, "ig_aaa", "3y", "cgb", "3y"),
			AsOf:    ig3.AsOf, Quality: "ok",
		})
	}

	// 信号状态（P1 只读透传；P2 评估器写入）。message 列不在 state 表（规格 C3：
	// message 归 alerts）；observe-only 期统一模板文案。
	// pct-v1 分位值同步透出为派生指标（observe-only 值仅供参考，quality 随 sample_n 三档）。
	sigRs, err := s.store.Pool().Query(ctx,
		`SELECT key, observed_state, last_value::float8, sample_n, COALESCE(observed_as_of::text,''), COALESCE(evidence_ref,'')
		 FROM cnfi_signal_state ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer sigRs.Close()
	for sigRs.Next() {
		var key, state, asOf, evidence string
		var lastValue float64
		var sampleN int32
		if err := sigRs.Scan(&key, &state, &lastValue, &sampleN, &asOf, &evidence); err != nil {
			return nil, err
		}
		resp.Signals = append(resp.Signals, &v1.CnfiSignalState{
			Key: key, ObservedState: state, AsOf: asOf, EvidenceRef: evidence,
			Message: "观察条件监控中（observe-only，未启用告警）",
		})
		if key == "credit_spread_3y_pctile_pctv1" {
			resp.Metrics = append(resp.Metrics, &v1.CnfiDerivedMetric{
				Key: "credit_spread_3y_pctile_pctv1", Value: lastValue, Unit: "pctile",
				SampleN: sampleN, AsOf: asOf, Quality: cnfiTier(sampleN),
			})
		}
	}
	return connect.NewResponse(resp), nil
}

// cnfiTier mirrors collect/cnfi.QualityTier three-tier table (F5 唯一行为表).
func cnfiTier(sampleN int32) string {
	switch {
	case sampleN >= 250:
		return "ok"
	case sampleN >= 120:
		return "reduced"
	default:
		return "N"
	}
}

// cnfiDerivedSampleN counts days in the trailing window where both legs exist
// (approximation of pct-v1 trailing 250 business days until calendar seeds land).
func cnfiDerivedSampleN(ctx context.Context, st *pgstore.Store, s1, t1, s2, t2 string) int32 {
	var n int32
	err := st.Pool().QueryRow(ctx, `
		SELECT count(*)::int FROM (
		  SELECT a.as_of FROM cnfi_series a
		  JOIN cnfi_series b ON b.as_of = a.as_of
		  WHERE a.series=$1 AND a.tenor=$2 AND b.series=$3 AND b.tenor=$4
		  ORDER BY a.as_of DESC LIMIT 250) w`, s1, t1, s2, t2).Scan(&n)
	if err != nil {
		return 0
	}
	return n
}
