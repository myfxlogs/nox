-- D-424：收敛型退出腿序修正——两策略「不收敛」死刑归因作废注记。
-- 退出函数引擎腿序反（buyBid−sellAsk 清算侧量误用为剩余价差）→ 首轮 tick clip 0 判平，
-- strategy_stats 归因「不收敛」建立在 bug 上；disabled 标志不改（复活=业主资金议程）。
UPDATE strategy_stats
SET disabled_reason = disabled_reason || ' [归因更正D-424：退出腿序bug首轮即平（非"不收敛"）；复审取证期望edge本身亦不成立——MEANREVERT均1.1bps<往返摩擦，CROSS_VENUE 132-311bps系跨合约类型基差/零价腿幻影（UKOILm-UKOIL/BRENT3%价差、price_actual=0实证），复活需入场信号质量修复+业主议程]'
WHERE strategy IN ('MEANREVERT', 'CROSS_VENUE');
