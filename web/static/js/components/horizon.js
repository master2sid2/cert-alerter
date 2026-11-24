// Expiry horizon: every certificate as a dot on a "days left" axis with the
// alert thresholds drawn as zones, so one glance shows what is coming.
//
// Dots stack upwards in columns. Certificates past the axis ("expired",
// "90+") and those without data ("no data": never checked, or every check
// failed) are packed into grids inside their side zones. The chart never
// grows past MAX_H: dots shrink first; a stack that still does not fit is
// broken in the middle (a vertical ellipsis with the stack's count) and
// ends with its top rows. A failing last check draws an error ring.
import { html, useEffect, useMemo, useRef, useState } from '../../vendor/preact-htm.js';
import { navigate } from '../lib/router.js';
import { t, tp, fmtDaysLeft } from '../lib/i18n.js';
import { certTitle, toneOf } from './status.js';

const toneVar = {
  ok: 'var(--ok)', warning: 'var(--warn)', critical: 'var(--crit)', expired: 'var(--exp)',
  error: 'var(--err)', pending: 'var(--pending)', disabled: 'var(--pending)',
};

const MAX_H = 260;
const MIN_H = 120;
const CHROME = 64; // axis labels below the base line, threshold labels on top
const RADII = [5.5, 4.5, 3.5];
const GAP = 36; // height of the break in a broken stack, px
const TOP_ROWS = 2; // rows drawn above the break

const hasDays = (c) => c.days_left !== null && c.days_left !== undefined;

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

  const view = useMemo(() => {
    const active = certs.filter((c) => c.enabled !== false);
    const byDays = (a, b) => a.days_left - b.days_left;
    const groups = {
      nodata: active.filter((c) => !hasDays(c)),
      expired: active.filter((c) => hasDays(c) && c.days_left < 0).sort((a, b) => b.days_left - a.days_left),
      beyond: active.filter((c) => hasDays(c) && c.days_left > domain).sort(byDays),
    };
    const inside = active.filter((c) => hasDays(c) && c.days_left >= 0 && c.days_left <= domain).sort(byDays);

    const pad = { l: 8, r: 8 };
    const zoneW = Math.max(36, Math.min(64, width * 0.07));
    const zone = {};
    let x = pad.l;
    if (groups.nodata.length) {
      zone.nodata = { x, w: zoneW };
      x += zoneW;
    }
    zone.expired = { x, w: zoneW };
    const x0 = x + zoneW;
    const x1 = width - pad.r - zoneW;
    zone.beyond = { x: x1, w: zoneW };
    const xOf = (d) => x0 + (Math.min(Math.max(d, 0), domain) / domain) * (x1 - x0);

    // Levels a radius needs: the highest axis column or zone grid.
    const layout = (r) => {
      const step = r * 2 + 1.5;
      const cols = Math.max(1, Math.floor((zoneW - 10) / step));
      const columns = new Map();
      for (const c of inside) {
        const k = Math.round(xOf(c.days_left) / step);
        columns.set(k, (columns.get(k) || 0) + 1);
      }
      const rows = Object.values(groups).map((g) => Math.ceil(g.length / cols));
      return { r, step, cols, need: Math.max(1, ...columns.values(), ...rows) };
    };
    const fits = (l) => l.need * l.step + CHROME <= MAX_H;
    const L = RADII.map(layout).find(fits) || layout(RADII[RADII.length - 1]);
    const { r, step, cols } = L;
    const cap = Math.floor((MAX_H - CHROME) / step); // levels that fit
    const gapRows = Math.ceil(GAP / step);
    const bottomRows = cap - gapRows - TOP_ROWS;
    const height = Math.max(MIN_H, Math.min(MAX_H, Math.min(L.need, cap) * step + CHROME));
    const base = height - 34;
    const yOf = (level) => base - r - 4 - level * step;

    const points = [];
    const breaks = []; // {cx, y, xs, n}
    // place lays a stack out in rows of `width` dots centred on cx, bottom-up.
    const place = (list, width, cx) => {
      const rowsOf = (items, firstRow) => items.forEach((c, i) => {
        const row = Math.floor(i / width);
        const inRow = Math.min(width, items.length - row * width);
        points.push({ c, x: cx + ((i % width) - (inRow - 1) / 2) * step, y: yOf(firstRow + row) });
      });
      if (Math.ceil(list.length / width) <= cap) {
        rowsOf(list, 0);
        return;
      }
      rowsOf(list.slice(0, bottomRows * width), 0);
      rowsOf(list.slice(list.length - TOP_ROWS * width), bottomRows + gapRows);
      const xs = Array.from({ length: width }, (_, i) => cx + (i - (width - 1) / 2) * step);
      const y = (yOf(bottomRows) + yOf(bottomRows + gapRows - 1)) / 2;
      breaks.push({ cx, y, xs, n: list.length });
    };

    // Axis columns, then the zone grids.
    const columns = new Map();
    for (const c of inside) {
      const k = Math.round(xOf(c.days_left) / step);
      if (!columns.has(k)) columns.set(k, []);
      columns.get(k).push(c);
    }
    for (const [k, list] of columns) place(list, 1, k * step);
    for (const [name, list] of Object.entries(groups)) {
      if (!list.length) continue;
      const z = zone[name];
      place(list, cols, z.x + z.w / 2 + (name === 'beyond' ? 2 : -2));
      z.count = list.length;
    }

    return { points, breaks, height, base, r, zone, zoneW, x0, x1, xOf };
  }, [certs, width, domain]);

  const { points, breaks, height, base, r, zone, zoneW, x0, x1, xOf } = view;
  const sorted = [...thresholds].sort((a, b) => b - a);
  const bands = sorted.map((d, i) => {
    const next = sorted[i + 1] ?? 0;
    const tone = d <= criticalDays ? 'var(--crit)' : 'var(--warn)';
    return { from: next, to: d, tone };
  });
  // Short zone labels and fewer threshold labels where space is tight.
  const narrow = zoneW < 52;
  let lastX = Infinity;
  const thrLabels = new Set(sorted.filter((d) => {
    const x = xOf(d);
    if (lastX - x < 24) return false;
    lastX = x;
    return true;
  }));
  const ticks = [];
  const tickStep = (domain > 120 ? 30 : domain > 60 ? 15 : 10) * (x1 - x0 < 300 ? 2 : 1);
  for (let d = 0; d <= domain; d += tickStep) ticks.push(d);

  const sideZone = (z, fill, label, title, shift) => z && html`<g>
    <rect x=${z.x + (shift > 0 ? 4 : 0)} y="10" width=${zoneW - 4} height=${base - 10} rx="8" fill=${fill}>
      ${z.count ? html`<title>${title} · ${z.count}</title>` : null}
    </rect>
    <text class="zone-label" x=${z.x + zoneW / 2 + shift} y=${base + 22} text-anchor="middle">${label}</text>
  </g>`;

  return html`<div class="horizon" ref=${ref}>
    <svg height=${height} viewBox=${`0 0 ${width} ${height}`} role="img" aria-label=${t('dash.horizon')}>
      ${sideZone(zone.nodata, 'var(--err-soft)', t(narrow ? 'dash.zone_nodata_short' : 'dash.zone_nodata'), t('dash.zone_nodata_title'), -2)}
      ${sideZone(zone.expired, 'var(--exp-soft)', t(narrow ? 'dash.zone_expired_short' : 'dash.zone_expired'), t('status.expired'), -2)}
      ${sideZone(zone.beyond, 'var(--ok-soft)', `${domain}+`, `${domain}+`, 2)}
      ${bands.map((z) => html`<rect x=${xOf(z.from)} y="10" width=${Math.max(0, xOf(z.to) - xOf(z.from))} height=${base - 10}
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
          ${thrLabels.has(d) && html`<text x=${xOf(d) + 4} y="24" fill=${color}>${d}d</text>`}
        </g>`;
      })}
      ${points.map((p) => html`<circle class=${'pt' + (p.c.check_failed ? ' failed' : '')} cx=${p.x} cy=${p.y}
        r=${p.c.check_failed ? r - 0.75 : r} fill=${toneVar[toneOf(p.c)]}
        onMouseEnter=${() => setHover(p)} onMouseLeave=${() => setHover(null)}
        onClick=${() => navigate('/certificates/' + p.c.id)} />`)}
      ${breaks.map((b) => html`<g class="gap"><title>${tp('unit.certs', b.n)}</title>
        ${b.xs.map((x) => [-15, -10, 10, 15].map((dy) => html`<circle cx=${x} cy=${b.y + dy} r="1.3" />`))}
        <text x=${b.cx} y=${b.y + 3.5} text-anchor="middle">${b.n}</text>
      </g>`)}
    </svg>
    ${hover && html`<div class="tip" style=${{ left: hover.x + 'px', top: hover.y + 'px' }}>
      <b>${certTitle(hover.c)}</b>
      <span>${hasDays(hover.c) ? fmtDaysLeft(hover.c.days_left) : t('status.' + toneOf(hover.c))}</span>
      ${hover.c.check_failed && html`<span class="err">${t('dash.check_failed')}</span>`}
    </div>`}
  </div>`;
}
