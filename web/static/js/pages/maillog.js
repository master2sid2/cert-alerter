// Mail journal: every e-mail the application tried to send.
import { html, useState } from '../../vendor/preact-htm.js';
import { Icon } from '../components/icons.js';
import { Button, Card, Callout, Empty, ErrorState, Pager, Segmented, Skeleton } from '../components/ui.js';
import { PageHead } from '../components/layout.js';
import { groupColor } from '../components/status.js';
import { get } from '../lib/api.js';
import { app } from '../lib/store.js';
import { Link, setQuery, useLocation } from '../lib/router.js';
import { useAsync, useInterval, useTitle } from '../lib/hooks.js';
import { t, tp, fmtDateISO, fmtTime, fmtRelative, formatNumber } from '../lib/i18n.js';

const LIMIT = 50;
const KIND_ICON = { alert: 'bell', group_test: 'send', test: 'zap', invite: 'userPlus', reset: 'key', other: 'mail' };
const KIND_TONE = { alert: 'warning', group_test: 'accent', test: 'accent', invite: 'ok', reset: 'pending', other: 'pending' };

export function MailLog() {
  const me = app.get().me;
  const isAdmin = me.user.role === 'admin';
  const { query } = useLocation();
  const kind = query.get('kind') || '';
  const status = query.get('status') || '';
  const groupId = query.get('group_id') || '';
  const from = query.get('from') || '';
  const to = query.get('to') || '';
  const offset = Number(query.get('offset') || 0);
  const [q, setQ] = useState(query.get('q') || '');
  const [open, setOpen] = useState(null);
  useTitle(t('nav.mail_log'), me.app.name);

  const params = new URLSearchParams();
  for (const [k, v] of Object.entries({ kind, status, group_id: groupId, from, to, q: query.get('q') || '' })) if (v) params.set(k, v);
  const listParams = new URLSearchParams(params);
  listParams.set('limit', LIMIT);
  listParams.set('offset', offset);
  const data = useAsync(() => get('/mail-log?' + listParams), [listParams.toString()]);
  const groups = useAsync(() => get('/groups'), []);
  useInterval(() => data.reload(true), 30000);

  const kinds = ['', 'alert', 'group_test', 'test', ...(isAdmin ? ['invite', 'reset'] : [])];
  const c30 = data.data?.counts_30d;
  const total30 = c30 ? (c30.sent || 0) + (c30.failed || 0) : 0;
  const rate = total30 ? Math.round(((c30.sent || 0) / total30) * 1000) / 10 : null;

  if (data.error) return html`<div class="page"><${ErrorState} error=${data.error} onRetry=${data.reload} /></div>`;
  return html`<div class="page stack">
    <${PageHead} title=${t('nav.mail_log')} sub=${t('mail.sub')}
      actions=${html`<a class="btn" href=${'/api/mail-log/export?' + params} download><${Icon} name="upload" />${t('mail.export')}</a>`} />

    <div class="kpis" style=${{ gridTemplateColumns: 'repeat(auto-fit, minmax(190px, 1fr))' }}>
      <div class="kpi tone-ok reveal" style=${{ '--i': 0 }}><div class="label"><${Icon} name="checkCircle" />${t('mail.sent_30d')}</div>
        <div class="value">${c30 ? formatNumber(c30.sent || 0) : '—'}</div><div class="meta">${t('mail.accepted_short')}</div></div>
      <div class=${'kpi reveal tone-exp' + (c30?.failed ? ' alive' : '')} style=${{ '--i': 1 }}><div class="label"><${Icon} name="xCircle" />${t('mail.failed_30d')}</div>
        <div class="value">${c30 ? formatNumber(c30.failed || 0) : '—'}</div><div class="meta">${t('mail.failed_meta')}</div></div>
      <div class="kpi tone-accent reveal" style=${{ '--i': 2 }}><div class="label"><${Icon} name="activity" />${t('mail.rate_30d')}</div>
        <div class="value">${rate === null ? '—' : rate + '%'}</div><div class="meta">${tp('mail.emails', total30)}</div></div>
    </div>

    <div class="stack-sm reveal" style=${{ '--i': 3 }}>
      <div class="row-wrap" style=${{ gap: '10px' }}>
        <form class="input-group" style=${{ flex: '1 1 260px', maxWidth: '380px' }} onSubmit=${(e) => { e.preventDefault(); setQuery({ q, offset: '' }); }}>
          <${Icon} name="search" />
          <input class="input" type="search" placeholder=${t('mail.search')} value=${q} onInput=${(e) => setQ(e.target.value)}
            onBlur=${() => setQuery({ q, offset: '' })} aria-label=${t('common.search')} />
        </form>
        <select class="select" style=${{ width: 'auto', minWidth: '170px' }} value=${groupId} aria-label=${t('nav.groups')}
          onChange=${(e) => setQuery({ group_id: e.target.value, offset: '' })}>
          <option value="">${t('certs.all_groups')}</option>
          ${(groups.data || []).map((g) => html`<option value=${g.id}>${g.name}</option>`)}
        </select>
        <label class="row" style=${{ gap: '6px' }}><span class="muted" style=${{ fontSize: '12.5px' }}>${t('mail.from')}</span>
          <input class="input" type="date" style=${{ width: 'auto' }} value=${from} onChange=${(e) => setQuery({ from: e.target.value, offset: '' })} /></label>
        <label class="row" style=${{ gap: '6px' }}><span class="muted" style=${{ fontSize: '12.5px' }}>${t('mail.to')}</span>
          <input class="input" type="date" style=${{ width: 'auto' }} value=${to} onChange=${(e) => setQuery({ to: e.target.value, offset: '' })} /></label>
        ${(from || to || groupId || kind || status || query.get('q')) && html`<${Button} kind="ghost" size="sm" icon="x"
          onClick=${() => { setQ(''); setQuery({ kind: '', status: '', group_id: '', from: '', to: '', q: '', offset: '' }); }}>${t('mail.reset_filters')}<//>`}
      </div>
      <div class="row-wrap hscroll" style=${{ gap: '10px' }}>
        <${Segmented} value=${kind} onChange=${(v) => setQuery({ kind: v, offset: '' })} label=${t('mail.col.kind')}
          options=${kinds.map((k) => ({ value: k, label: k ? t('mail.kind.' + k) : t('mail.kind_all'), icon: k ? KIND_ICON[k] : undefined }))} />
        <${Segmented} value=${status} onChange=${(v) => setQuery({ status: v, offset: '' })} label=${t('mail.col.status')}
          options=${['', 'sent', 'failed'].map((s) => ({ value: s, label: s ? t('mail.status.' + s) : t('mail.status_all') }))} />
      </div>
    </div>

    <${Card} class="reveal" style=${{ '--i': 4 }} flush>
      ${!data.data ? html`<${Skeleton} rows=${8} />`
        : data.data.items.length === 0 ? html`<${Empty} icon="send" title=${t('mail.empty_title')}>${t('mail.empty_text')}<//>`
        : html`<div class="table-wrap"><table class="table"><thead><tr>
            <th>${t('mail.col.time')}</th><th>${t('mail.col.kind')}</th><th>${t('mail.col.recipient')}</th>
            <th>${t('mail.col.subject')}</th><th>${t('mail.col.status')}</th>
          </tr></thead><tbody>${data.data.items.map((e) => html`
            <tr class="click" onClick=${() => setOpen(open === e.id ? null : e.id)} aria-expanded=${open === e.id}>
              <td class="nowrap"><div class="cell-main"><span class="mono" style=${{ fontSize: '12.5px' }}>${fmtDateISO(e.created_at)} ${fmtTime(e.created_at)}</span>
                <span class="subtitle">${fmtRelative(e.created_at)}</span></div></td>
              <td><span class=${'badge square tone-' + (KIND_TONE[e.kind] || 'pending')}><${Icon} name=${KIND_ICON[e.kind] || 'mail'} size="12" />${t('mail.kind.' + (KIND_ICON[e.kind] ? e.kind : 'other'))}</span></td>
              <td><b style=${{ fontWeight: 550 }}>${e.recipient}</b></td>
              <td style=${{ maxWidth: '380px' }}><div class="cell-main">
                <span class="truncate">${e.subject}</span>
                <span class="row-wrap" style=${{ gap: '6px' }}>
                  ${e.group_name && html`<span class="chip group" style=${{ '--chip-color': groupColor(e.group_id || 0), height: '20px', fontSize: '11px' }}><span>${e.group_name}</span></span>`}
                  ${e.cert_count > 0 && html`<span class="subtitle">${tp('unit.certs', e.cert_count)}</span>`}
                </span></div></td>
              <td>${e.status === 'sent'
                ? html`<span class="badge tone-ok" title=${t('mail.accepted_hint')}><${Icon} name="check" size="12" />${t('mail.status.sent')}</span>`
                : html`<div class="cell-main"><span><span class="badge tone-expired"><${Icon} name="x" size="12" />${t('mail.status.failed')}</span></span>
                    <span class="subtitle truncate" style=${{ maxWidth: '260px', color: 'var(--exp)' }} title=${e.error}>${e.error}</span></div>`}</td>
            </tr>
            ${open === e.id && html`<tr><td colspan="5" style=${{ background: 'var(--surface-2)' }}>
              <dl class="kv" style=${{ padding: '4px 4px 8px' }}>
                <dt>${t('mail.col.subject')}</dt><dd>${e.subject}</dd>
                <dt>${t('mail.col.recipient')}</dt><dd>${e.recipient}</dd>
                <dt>${t('mail.col.time')}</dt><dd class="mono">${new Date(e.created_at).toISOString()}</dd>
                ${e.group_name && html`<dt>${t('notif.col.group')}</dt><dd>${e.group_id ? html`<${Link} href=${'/groups/' + e.group_id}>${e.group_name}<//>` : e.group_name}</dd>`}
                ${e.certificates.length > 0 && html`<dt>${t('nav.certificates')}</dt><dd><div class="sans">${e.certificates.map((u) => html`<code>${u}</code>`)}</div></dd>`}
                <dt>${t('mail.col.status')}</dt><dd>${e.status === 'sent' ? t('mail.accepted_hint') : html`<span class="mono" style=${{ color: 'var(--exp)' }}>${e.error}</span>`}</dd>
                <dt>${t('mail.col.server')}</dt><dd class="mono">${e.smtp_server || '—'} · ${e.duration_ms} ${t('mail.ms')}</dd>
              </dl>
            </td></tr>`}`)}</tbody></table></div>`}
      ${data.data && html`<${Pager} total=${data.data.total} offset=${offset} limit=${LIMIT} onChange=${(o) => setQuery({ offset: o || '' })} />`}
    <//>
    <${Callout} tone="accent" icon="info">${t('mail.accepted_note')}<//>
  </div>`;
}
