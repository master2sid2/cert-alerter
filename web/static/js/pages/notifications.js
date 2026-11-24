// Alert delivery history.
import { html, useState } from '../../vendor/preact-htm.js';
import { Icon } from '../components/icons.js';
import { Button, Card, Empty, ErrorState, Pager, Segmented, Skeleton, toastError } from '../components/ui.js';
import { PageHead } from '../components/layout.js';
import { notifTone } from './dashboard.js';
import { NotifTarget } from './groups.js';
import { channelIcon, channelLabel } from '../components/channels.js';
import { get, post } from '../lib/api.js';
import { app, toast } from '../lib/store.js';
import { Link, setQuery, useLocation } from '../lib/router.js';
import { useAsync, useInterval, useTitle } from '../lib/hooks.js';
import { t, fmtDateTime, fmtRelative, thresholdLabel } from '../lib/i18n.js';

const LIMIT = 50;

export function Notifications() {
  const me = app.get().me;
  const isAdmin = me.user.role === 'admin';
  const { query } = useLocation();
  const status = query.get('status') || '';
  const channel = query.get('channel') || '';
  const groupId = query.get('group_id') || '';
  const offset = Number(query.get('offset') || 0);
  const [q, setQ] = useState(query.get('q') || '');
  useTitle(t('nav.notifications'), me.app.name);
  const params = new URLSearchParams({ limit: LIMIT, offset });
  if (status) params.set('status', status);
  if (channel) params.set('channel', channel);
  if (groupId) params.set('group_id', groupId);
  if (query.get('q')) params.set('q', query.get('q'));
  const data = useAsync(() => get('/notifications?' + params), [params.toString()]);
  useInterval(() => data.reload(true), 30000);

  const retry = async (n) => {
    try {
      await post(`/notifications/${n.id}/retry`);
      toast(t('notif.retry_queued'));
      data.reload(true);
    } catch (e) {
      toastError(e);
    }
  };

  if (data.error) return html`<div class="page"><${ErrorState} error=${data.error} onRetry=${data.reload} /></div>`;
  return html`<div class="page stack">
    <${PageHead} title=${t('nav.notifications')} sub=${t('notif.sub')}
      actions=${html`<${Link} href=${'/mail-log' + (groupId ? '?group_id=' + groupId : '')} class="btn"><${Icon} name="send" />${t('nav.mail_log')}<//>`} />
    <div class="row-wrap reveal" style=${{ '--i': 1, gap: '10px' }}>
      <form class="input-group" style=${{ flex: '1 1 260px', maxWidth: '420px' }} onSubmit=${(e) => { e.preventDefault(); setQuery({ q, offset: '' }); }}>
        <${Icon} name="search" />
        <input class="input" type="search" placeholder=${t('notif.search')} value=${q} onInput=${(e) => setQ(e.target.value)}
          onBlur=${() => setQuery({ q, offset: '' })} aria-label=${t('common.search')} />
      </form>
      <${Segmented} value=${status} onChange=${(v) => setQuery({ status: v, offset: '' })} label=${t('notif.col.status')}
        options=${['', 'sent', 'pending', 'failed', 'cancelled'].map((s) => ({ value: s, label: s ? t('notif.status_' + s) : t('certs.filter_all') }))} />
      <${Segmented} value=${channel} onChange=${(v) => setQuery({ channel: v, offset: '' })} label=${t('channels.type')}
        options=${['', 'email', 'telegram', 'slack', 'webhook'].map((c) => ({ value: c, label: c ? channelLabel(c) : t('channels.all'), icon: c ? channelIcon(c) : undefined }))} />
      ${groupId && html`<${Button} size="sm" icon="x" onClick=${() => setQuery({ group_id: '', offset: '' })}>${t('notif.clear_group')}<//>`}
    </div>
    <${Card} class="reveal" style=${{ '--i': 2 }} flush>
      ${!data.data ? html`<${Skeleton} rows=${8} />`
        : data.data.items.length === 0 ? html`<${Empty} icon="bell" title=${t('notif.empty_title')}>${t('notif.empty_text')}<//>`
        : html`<div class="table-wrap"><table class="table"><thead><tr>
            <th>${t('notif.col.time')}</th><th>${t('notif.col.group')}</th><th>${t('notif.col.recipient')}</th>
            <th>${t('notif.col.certificate')}</th><th>${t('notif.col.threshold')}</th><th>${t('notif.col.status')}</th>
            ${isAdmin && html`<th></th>`}
          </tr></thead><tbody>${data.data.items.map((n) => html`<tr>
            <td class="nowrap"><div class="cell-main"><span>${fmtDateTime(n.sent_at || n.created_at)}</span>
              <span class="subtitle">${fmtRelative(n.sent_at || n.created_at)}</span></div></td>
            <td>${n.group_id ? html`<${Link} href=${'/groups/' + n.group_id}>${n.group_name}<//>` : html`<span class="muted">${n.group_name}</span>`}</td>
            <td><${NotifTarget} n=${n} /></td>
            <td class="endpoint" style=${{ maxWidth: '260px' }}>${n.certificate_id
              ? html`<${Link} href=${'/certificates/' + n.certificate_id} class="truncate" style=${{ display: 'block' }}>${n.certificate_url}<//>`
              : html`<span class="truncate" style=${{ display: 'block' }}>${n.certificate_url}</span>`}</td>
            <td class="nowrap">${thresholdLabel(n.threshold_days)}</td>
            <td><div class="cell-main">
              <span><span class=${'badge tone-' + notifTone(n.status)}>${t('notif.status_' + n.status)}</span>
                ${n.attempts > 1 && html` <span class="faint mono" style=${{ fontSize: '11px' }}>×${n.attempts}</span>`}</span>
              ${n.last_error && html`<span class="subtitle truncate" style=${{ maxWidth: '280px', color: n.status === 'failed' || n.status === 'pending' ? 'var(--exp)' : undefined }} title=${n.last_error}>${n.last_error}</span>`}
              ${n.status === 'pending' && n.attempts > 0 && html`<span class="subtitle">${t('notif.next_attempt', { time: fmtRelative(n.next_attempt_at) })}</span>`}
            </div></td>
            ${isAdmin && html`<td class="actions">${n.status === 'failed' && html`<${Button} size="xs" icon="refresh" onClick=${() => retry(n)}>${t('common.retry')}<//>`}</td>`}
          </tr>`)}</tbody></table></div>`}
      ${data.data && html`<${Pager} total=${data.data.total} offset=${offset} limit=${LIMIT} onChange=${(o) => setQuery({ offset: o || '' })} />`}
    <//>
  </div>`;
}
