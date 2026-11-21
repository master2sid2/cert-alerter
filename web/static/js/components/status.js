// Certificate status presentation: badges, day counters, lifetime gauges.
import { html } from '../../vendor/preact-htm.js';
import { Icon } from './icons.js';
import { t, tp, fmtDaysLeft, fmtDate, formatNumber } from '../lib/i18n.js';

export const STATUSES = ['ok', 'warning', 'critical', 'expired', 'error', 'pending'];

export function toneOf(cert) {
  if (cert && cert.enabled === false) return 'disabled';
  return cert?.status || 'pending';
}

export function StatusBadge({ status, enabled = true, compact }) {
  const st = enabled ? status : 'disabled';
  const label = t('status.' + st);
  return html`<span class=${'badge tone-' + st} title=${compact ? label : undefined}>
    <span class="dot dot-live"></span>${compact ? null : label}
  </span>`;
}

export function TrustBadge({ cert }) {
  if (!cert.not_after) return null;
  return cert.trusted
    ? html`<span class="badge tone-ok square"><${Icon} name="shieldCheck" size="12" />${t('cert.trusted')}</span>`
    : html`<span class="badge tone-error square" title=${cert.verify_error}><${Icon} name="shieldAlert" size="12" />${t('cert.untrusted')}</span>`;
}

export function DaysLeft({ cert, big }) {
  if (cert.days_left === null || cert.days_left === undefined) return html`<span class="faint">—</span>`;
  const d = cert.days_left;
  const tone = 'tone-' + toneOf(cert);
  if (d < 0) {
    const ago = -d - 1;
    return html`<span class=${'days ' + tone} title=${fmtDaysLeft(d)}>
      <span class="num" style=${big ? { fontSize: '22px' } : undefined}>−${ago}</span><span class="unit">${tp('unit.dw', ago)}</span>
    </span>`;
  }
  return html`<span class=${'days ' + tone} title=${fmtDaysLeft(d)}>
    <span class="num" style=${big ? { fontSize: '22px' } : undefined}>${formatNumber(d)}</span><span class="unit">${tp('unit.dw', d)}</span>
  </span>`;
}

// remainingFraction returns the share of the validity period that is left.
export function remainingFraction(cert) {
  if (!cert.not_before || !cert.not_after) return null;
  const nb = new Date(cert.not_before).getTime();
  const na = new Date(cert.not_after).getTime();
  const total = na - nb;
  if (total <= 0) return 0;
  return Math.min(1, Math.max(0, (na - Date.now()) / total));
}

export function LifetimeRing({ cert, size = 26 }) {
  const f = remainingFraction(cert);
  const stroke = 3.2;
  const r = (size - stroke) / 2;
  const c = 2 * Math.PI * r;
  const tone = 'tone-' + toneOf(cert);
  return html`<svg class=${'ring ' + tone} width=${size} height=${size} viewBox=${`0 0 ${size} ${size}`} aria-hidden="true">
    <circle class="track" cx=${size / 2} cy=${size / 2} r=${r} fill="none" stroke-width=${stroke} />
    ${f !== null && html`<circle class="bar" cx=${size / 2} cy=${size / 2} r=${r} fill="none" stroke-width=${stroke}
      stroke-linecap="round" stroke-dasharray=${c} stroke-dashoffset=${c * (1 - Math.max(f, 0.02))}
      transform=${`rotate(-90 ${size / 2} ${size / 2})`} />`}
  </svg>`;
}

// LifetimeBar shows issue → now → expiry with alert threshold markers.
export function LifetimeBar({ cert, thresholds = [] }) {
  if (!cert.not_before || !cert.not_after) return null;
  const nb = new Date(cert.not_before).getTime();
  const na = new Date(cert.not_after).getTime();
  const total = Math.max(na - nb, 1);
  const pos = (ms) => Math.min(100, Math.max(0, ((ms - nb) / total) * 100));
  const now = pos(Date.now());
  const tone = 'tone-' + toneOf(cert);
  const day = 86400000;
  const marks = thresholds
    .map((d) => ({ d, x: pos(na - d * day) }))
    .filter((m) => m.x > 2 && m.x < 98);
  return html`<div class=${tone}>
    <div class="lifetime" style=${{ marginTop: '22px' }}>
      <div class="fill" style=${{ width: now + '%' }}></div>
      ${marks.map((m) => html`<div class="mark" style=${{ left: m.x + '%' }}><span>${m.d}d</span></div>`)}
      <div class="now" style=${{ left: now + '%' }} title=${t('cert.now')}></div>
    </div>
    <div class="row" style=${{ marginTop: '10px', fontSize: '12px' }}>
      <span class="muted">${t('cert.issued')} <b class="mono" style=${{ color: 'var(--text-2)' }}>${fmtDate(cert.not_before)}</b></span>
      <span class="spacer"></span>
      <span class="muted">${t('cert.expires')} <b class="mono" style=${{ color: 'var(--text-2)' }}>${fmtDate(cert.not_after)}</b></span>
    </div>
  </div>`;
}

export function certTitle(c) {
  return c.name || c.host || c.url;
}

export function GroupChips({ ids, groups, max = 3, onRemove }) {
  if (!ids?.length) return html`<span class="faint">—</span>`;
  const byId = new Map((groups || []).map((g) => [g.id, g]));
  const shown = ids.slice(0, max);
  const rest = ids.length - shown.length;
  return html`<div class="row-wrap">
    ${shown.map((id) => {
      const g = byId.get(id);
      return html`<span class="chip group" style=${{ '--chip-color': groupColor(id) }} title=${g?.name}>
        <span>${g?.name || '#' + id}</span>
        ${onRemove && html`<button class="x" aria-label=${t('common.remove')} onClick=${(e) => { e.stopPropagation(); onRemove(id); }}><${Icon} name="x" /></button>`}
      </span>`;
    })}
    ${rest > 0 && html`<span class="chip">+${rest}</span>`}
  </div>`;
}

const palette = ['#7390ff', '#3ad29c', '#f6c045', '#ff8a52', '#ab90ff', '#4cc9f0', '#f472b6', '#a3e635', '#fb7185', '#2dd4bf'];
export function groupColor(id) {
  return palette[(Number(id) * 7) % palette.length];
}
