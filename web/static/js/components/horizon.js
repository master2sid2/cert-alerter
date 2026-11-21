// Expiry horizon: every certificate as a dot on a "days left" axis with the
// alert thresholds drawn as zones, so one glance shows what is coming.
import { html, useEffect, useMemo, useRef, useState } from '../../vendor/preact-htm.js';
import { navigate } from '../lib/router.js';
import { t, fmtDaysLeft } from '../lib/i18n.js';
import { certTitle, toneOf } from './status.js';

const toneVar = {
  ok: 'var(--ok)', warning: 'var(--warn)', critical: 'var(--crit)', expired: 'var(--exp)',
  error: 'var(--err)', pending: 'var(--pending)', disabled: 'var(--pending)',
};

export function Horizon({ certs, thresholds = [30, 14, 7, 3], criticalDays = 7 }) {
  const ref = useRef(null);
  const [width, setWidth] = useState(900);
  const [hover, setHover] = useState(null);

  useEffect(() => {
    const el = ref.current;
    if (!el) return undefined;
    const ro = new ResizeObserver((entries) => setWidth(Math.max(320, entries[0].contentRect.width)));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const maxT = Math.max(...thresholds, 30);
  const domain = Math.max(90, Math.ceil((maxT * 2) / 10) * 10);
  const pad = { l: 8, r: 8 };
  const zoneW = Math.min(64, width * 0.07); // "expired" and "beyond" zones
  const x0 = pad.l + zoneW;
  const x1 = width - pad.r - zoneW;
  const xOf = (d) => {
    if (d < 0) return pad.l + zoneW / 2;
    if (d > domain) return x1 + zoneW / 2;
    return x0 + (d / domain) * (x1 - x0);
  };

  const { points, height, base, r } = useMemo(() => {
    const items = certs
      .filter((c) => c.enabled !== false && c.days_left !== null && c.days_left !== undefined)
      .map((c) => ({ c, x: xOf(c.days_left) }))
      .sort((a, b) => a.x - b.x || a.c.days_left - b.c.days_left);
    // dot plot: stack dots upwards in columns of width 2r+1
    const radius = items.length > 120 ? 3.5 : items.length > 50 ? 4.5 : 5.5;
    const step = radius * 2 + 1.5;
    const columns = new Map();
    let maxLevel = 0;
    const placed = items.map((it) => {
      const col = Math.round(it.x / step);
      const level = columns.get(col) || 0;
      columns.set(col, level + 1);
      maxLevel = Math.max(maxLevel, level + 1);
      return { ...it, x: col * step, level };
    });
    const h = Math.max(120, Math.min(260, maxLevel * step + 64));
    const b = h - 34;
    return { points: placed.map((p) => ({ ...p, y: b - radius - 4 - p.level * step })), height: h, base: b, r: radius };
  }, [certs, width, domain]); // eslint-disable-line react-hooks/exhaustive-deps

  const sorted = [...thresholds].sort((a, b) => b - a);
  const zones = sorted.map((d, i) => {
    const next = sorted[i + 1] ?? 0;
    const tone = d <= criticalDays ? 'var(--crit)' : 'var(--warn)';
    return { from: next, to: d, tone };
  });
  const ticks = [];
  const tickStep = domain > 120 ? 30 : domain > 60 ? 15 : 10;
  for (let d = 0; d <= domain; d += tickStep) ticks.push(d);

  return html`<div class="horizon" ref=${ref}>
    <svg height=${height} viewBox=${`0 0 ${width} ${height}`} role="img" aria-label=${t('dash.horizon')}>
      <rect x=${pad.l} y="10" width=${zoneW - 4} height=${base - 10} rx="8" fill="var(--exp-soft)" />
      <text class="zone-label" x=${pad.l + zoneW / 2 - 2} y=${base + 22} text-anchor="middle">${t('dash.zone_expired')}</text>
      <rect x=${x1 + 4} y="10" width=${zoneW - 4} height=${base - 10} rx="8" fill="var(--ok-soft)" />
      <text class="zone-label" x=${x1 + zoneW / 2 + 2} y=${base + 22} text-anchor="middle">${domain}+</text>
      ${zones.map((z) => html`<rect x=${xOf(z.from)} y="10" width=${Math.max(0, xOf(z.to) - xOf(z.from))} height=${base - 10}
        fill=${z.tone} opacity=${z.to <= criticalDays ? 0.1 : 0.07} />`)}
      <line class="axis" x1=${x0} x2=${x1} y1=${base} y2=${base} />
      ${ticks.map((d) => html`<g class="tick">
        <line x1=${xOf(d)} x2=${xOf(d)} y1=${base} y2=${base + 5} />
        <text x=${xOf(d)} y=${base + 22} text-anchor="middle">${d}</text>
      </g>`)}
      ${sorted.map((d) => {
        const color = d <= criticalDays ? 'var(--crit)' : 'var(--warn)';
        return html`<g class="thr">
          <line x1=${xOf(d)} x2=${xOf(d)} y1="14" y2=${base} stroke=${color} />
          <text x=${xOf(d) + 4} y="24" fill=${color}>${d}d</text>
        </g>`;
      })}
      ${points.map((p) => html`<circle class="pt" cx=${p.x} cy=${p.y} r=${r} fill=${toneVar[toneOf(p.c)]}
        onMouseEnter=${() => setHover(p)} onMouseLeave=${() => setHover(null)}
        onClick=${() => navigate('/certificates/' + p.c.id)} />`)}
    </svg>
    ${hover && html`<div class="tip" style=${{ left: hover.x + 'px', top: hover.y + 'px' }}>
      <b>${certTitle(hover.c)}</b><span>${fmtDaysLeft(hover.c.days_left)}</span>
    </div>`}
  </div>`;
}
