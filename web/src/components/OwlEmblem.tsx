// OwlEmblem —— 夜枭 emblem（D-638 三层钉死②：名=NOX/神=夜枭猎手——
// 「谁在干活」象征面；纯 inline SVG 零新依赖零新构建件）。
export function OwlEmblem({ size = 28 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" aria-label="夜枭" role="img" style={{ verticalAlign: "-0.25em" }}>
      {/* 头+体（新月夜枭——两圆相减得肩线） */}
      <circle cx="16" cy="15" r="12" fill="currentColor" opacity="0.92" />
      <circle cx="16" cy="11.5" r="10" fill="var(--bg, #10141c)" opacity="0.55" />
      {/* 双目（枭眼=名册 beat_ts 心跳同源语义） */}
      <circle cx="11.5" cy="13.5" r="4.4" fill="#e8eefc" />
      <circle cx="20.5" cy="13.5" r="4.4" fill="#e8eefc" />
      <circle cx="11.5" cy="13.5" r="2" fill="currentColor" />
      <circle cx="20.5" cy="13.5" r="2" fill="currentColor" />
      {/* 喙 */}
      <path d="M16 17.8 L14.4 20.2 L17.6 20.2 Z" fill="#e8a13a" />
      {/* 耳羽 */}
      <path d="M7.5 8.5 L10 4.5 L11.5 8 Z" fill="currentColor" opacity="0.92" />
      <path d="M24.5 8.5 L22 4.5 L20.5 8 Z" fill="currentColor" opacity="0.92" />
    </svg>
  );
}
