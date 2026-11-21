// Overview of the whole fleet.
import { html, useState } from '../../vendor/preact-htm.js';
import { Icon } from '../components/icons.js';
import { Button, Card, Callout, ErrorState, Empty, Skeleton, toastError } from '../components/ui.js';
import { PageHead } from '../components/layout.js';
import { Horizon } from '../components/horizon.js';
import { DaysLeft, LifetimeRing, StatusBadge, certTitle, groupColor } from '../components/status.js';
import { AddCertificateModal } from './certificates.js';
import { get, post } from '../lib/api.js';
import { app, toast } from '../lib/store.js';
import { Link, navigate } from '../lib/router.js';
import { useAsync, useInterval, useTitle } from '../lib/hooks.js';
import { t, tp, fmtRelative, fmtDateTime, fmtDaysLeft, formatNumber, thresholdLabel } from '../lib/i18n.js';

function Kpi({ tone, icon, label, value, meta, href, i }) {
  const alive = value > 0 && tone !== 'accent' && tone !== 'ok';
  return html`<${Link} href=${href} class=${`kpi reveal tone-${tone}${alive ? ' alive' : ''}`} style=${{ '--i': i }}>
    <div class="label"><${Icon} name=${icon} />${label}</div>
    <div class="value">${formatNumber(value)}</div>
    <div class="meta">${meta}</div>
    <svg class="glyph" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true">
      <circle cx="12" cy="12" r="10" /><path d="M12 6v6l4 2" />
    </svg>
  <//>`;
}

export function Dashboard() {
  const me = app.get().me;
  const isAdmin = me.user.role === 'admin';
  const [adding, setAdding] = useState(false);
  const [checking, setChecking] = useState(false);
  useTitle(t('nav.dashboard'), me.app.name);
  const dash = useAsync(() => get('/dashboard'), []);
  const certs = useAsync(() => get('/certificates'), []);
  const running = dash.data?.scheduler?.check_running;
  useInterval(() => { dash.reload(true); certs.reload(true); }, running || checking ? 3000 : 60000);

  const checkAll = async () => {
    setChecking(true);
    try {
      const r = await post('/certificates/check');
      toast(r.started ? t('certs.check_started') : t('certs.check_running'), { tone: 'accent' });
      dash.reload(true);
    } catch (e) {
      toastError(e);
    } finally {
      setTimeout(() => setChecking(false), 3000);
    }
  };

  if (dash.error) return html`<div class="page"><${ErrorState} error=${dash.error} onRetry=${dash.reload} /></div>`;
  const d = dash.data;
  const tot = d?.totals || {};
  const expiring = (tot.warning || 0) + (tot.critical || 0);
  const sched = d?.scheduler;

  return html`<div class="page stack">
    <${PageHead} title=${t('dash.title')}
      sub=${d ? t('dash.sub', { n: tp('unit.certs', tot.certificates || 0), when: sched?.last_full_check ? fmtRelative(sched.last_full_check) : t('common.never') }) : ' '}
      actions=${html`
        <${Button} icon="refresh" loading=${running || checking} onClick=${checkAll}>${t('certs.check_all')}<//>
        <${Button} kind="primary" icon="plus" onClick=${() => setAdding(true)}>${t('certs.add')}<//>`} />

    ${d && !d.smtp_configured && html`<${Callout} tone="warning" icon="mail"
      action=${isAdmin && html`<${Button} size="sm" onClick=${() => navigate('/settings/email')}>${t('dash.configure_smtp')}<//>`}>
      <b>${t('dash.smtp_missing_title')}</b> ${t('dash.smtp_missing_text', { n: d.notifications_pending })}
    <//>`}
    ${d && tot.ungrouped > 0 && html`<${Callout} tone="accent" icon="layers"
      action=${html`<${Button} size="sm" onClick=${() => navigate('/certificates?group=none')}>${t('common.show')}<//>`}>
      ${t('dash.ungrouped', { n: tp('unit.certs', tot.ungrouped) })}
    <//>`}

    <div class="kpis">
      ${d ? html`
        <${Kpi} i=${0} tone="accent" icon="shieldCheck" label=${t('dash.kpi_total')} value=${tot.certificates}
          meta=${t('dash.kpi_total_meta', { ok: tot.ok || 0, paused: tot.disabled || 0 })} href="/certificates" />
        <${Kpi} i=${1} tone="warn" icon="hourglass" label=${t('dash.kpi_expiring', { days: me.app.warning_days })} value=${expiring}
          meta=${t('dash.kpi_expiring_meta')} href="/certificates?status=expiring" />
        <${Kpi} i=${2} tone="crit" icon="alert" label=${t('dash.kpi_critical', { days: me.app.critical_days })} value=${tot.critical || 0}
          meta=${t('dash.kpi_critical_meta')} href="/certificates?status=critical" />
        <${Kpi} i=${3} tone="exp" icon="shieldX" label=${t('dash.kpi_expired')} value=${tot.expired || 0}
          meta=${t('dash.kpi_expired_meta')} href="/certificates?status=expired" />
        <${Kpi} i=${4} tone="err" icon="wifiOff" label=${t('dash.kpi_errors')} value=${tot.check_failed || 0}
          meta=${t('dash.kpi_errors_meta', { n: tot.untrusted || 0 })} href="/certificates?status=problems" />`
      : [0, 1, 2, 3, 4].map(() => html`<div class="kpi"><div class="skeleton" style=${{ height: '70px' }}></div></div>`)}
    </div>

    <${Card} class="reveal" style=${{ '--i': 5 }} title=${t('dash.horizon')} sub=${t('dash.horizon_sub', { thresholds: me.app.thresholds.join(' · ') })}
      tools=${html`<div class="legend">
        ${['ok', 'warning', 'critical', 'expired', 'error'].map((s) => html`<span class=${'tone-' + s}><i></i>${t('status.' + s)}</span>`)}
      </div>`}>
      ${certs.data
        ? certs.data.length
          ? html`<${Horizon} certs=${certs.data} thresholds=${me.app.thresholds} criticalDays=${me.app.critical_days} />`
          : html`<${Empty} icon="shieldCheck" title=${t('certs.empty_title')}
              action=${html`<${Button} kind="primary" icon="plus" onClick=${() => setAdding(true)}>${t('certs.add_first')}<//>`}>${t('certs.empty_text')}<//>`
        : html`<div class="skeleton" style=${{ height: '150px' }}></div>`}
    <//>

    <div class="grid grid-main">
      <${Card} class="reveal" style=${{ '--i': 6 }} flush title=${t('dash.expiring_soon')} sub=${t('dash.expiring_soon_sub', { days: me.app.warning_days })}
        tools=${html`<${Link} href="/certificates?sort=expiry" class="btn btn-ghost btn-sm">${t('common.view_all')}<${Icon} name="arrowRight" /><//>`}>
        ${!d ? html`<${Skeleton} rows=${5} />`
          : d.expiring.length === 0 ? html`<${Empty} icon="checkCircle" title=${t('dash.nothing_expiring')}>${t('dash.nothing_expiring_text', { days: me.app.warning_days })}<//>`
          : html`<div class="list">${d.expiring.map((c) => html`<${Link} href=${'/certificates/' + c.id} class="list-item">
              <${LifetimeRing} cert=${c} size=${30} />
              <div class="cell-main grow"><span class="title truncate">${certTitle(c)}</span>
                <span class="endpoint truncate">${c.url}</span></div>
              <div style=${{ textAlign: 'right' }}><${DaysLeft} cert=${c} big /><div class="meta">${fmtDateTime(c.not_after)}</div></div>
            <//>`)}</div>`}
      <//>
      <${Card} class="reveal" style=${{ '--i': 7 }} flush title=${t('nav.groups')} sub=${d ? tp('unit.groups', d.groups_count) : ''}
        tools=${html`<${Link} href="/groups" class="btn btn-ghost btn-sm">${t('common.view_all')}<${Icon} name="arrowRight" /><//>`}>
        ${!d ? html`<${Skeleton} rows=${4} />`
          : d.groups.length === 0 ? html`<${Empty} icon="layers" title=${t('groups.empty_title')}>${t('groups.empty_text')}<//>`
          : html`<div class="list">${d.groups.map((g) => html`<${Link} href=${'/groups/' + g.id} class="list-item">
              <span class="chip group" style=${{ '--chip-color': groupColor(g.id), border: 0, background: 'none', padding: 0, maxWidth: 'none' }}></span>
              <div class="cell-main grow"><span class="title truncate">${g.name}</span>
                <span class="meta">${tp('unit.certs', g.certificate_count)} · ${tp('unit.recipients', g.recipient_count)}</span></div>
              ${g.health.worst ? html`<${StatusBadge} status=${g.health.worst} />` : html`<span class="faint">—</span>`}
            <//>`)}</div>`}
      <//>
    </div>

    <div class="grid grid-2">
      <${Card} class="reveal" style=${{ '--i': 8 }} flush title=${t('dash.problems')} sub=${t('dash.problems_sub')}>
        ${!d ? html`<${Skeleton} rows=${3} />`
          : d.problems.length === 0 ? html`<${Empty} icon="shieldCheck" title=${t('dash.no_problems')}>${t('dash.no_problems_text')}<//>`
          : html`<div class="list">${d.problems.map((c) => html`<${Link} href=${'/certificates/' + c.id} class="list-item">
              <span class=${'badge square tone-' + (c.check_failed ? 'error' : 'expired')}><${Icon} name=${c.check_failed ? 'wifiOff' : 'shieldAlert'} size="12" /></span>
              <div class="cell-main grow"><span class="title truncate">${certTitle(c)}</span>
                <span class="meta truncate">${c.check_failed ? c.last_error : c.verify_error}</span></div>
            <//>`)}</div>`}
      <//>
      <${Card} class="reveal" style=${{ '--i': 9 }} flush title=${t('dash.recent_notifications')}
        sub=${d ? t('dash.mail_30d', { sent: d.mail_30d.sent || 0, failed: d.mail_30d.failed || 0 }) : ''}
        tools=${html`<${Link} href="/mail-log" class="btn btn-ghost btn-sm"><${Icon} name="send" />${t('nav.mail_log')}<//>
          <${Link} href="/notifications" class="btn btn-ghost btn-sm">${t('common.view_all')}<${Icon} name="arrowRight" /><//>`}>
        ${!d ? html`<${Skeleton} rows=${3} />`
          : d.recent_notifications.length === 0 ? html`<${Empty} icon="bell" title=${t('notif.empty_title')}>${t('notif.empty_text')}<//>`
          : html`<div class="list">${d.recent_notifications.map((n) => html`<div class="list-item">
              <span class=${'badge square tone-' + notifTone(n.status)}><${Icon} name=${notifIcon(n.status)} size="12" /></span>
              <div class="cell-main grow"><span class="title truncate" style=${{ fontWeight: 550 }}>${n.recipient_email}</span>
                <span class="meta truncate">${n.group_name} · ${n.certificate_url} · ${thresholdLabel(n.threshold_days)}</span></div>
              <span class="meta nowrap">${fmtRelative(n.sent_at || n.created_at)}</span>
            </div>`)}</div>`}
      <//>
    </div>

    ${sched && html`<div class="statusline reveal" style=${{ '--i': 10 }}>
      <span><${Icon} name="clock" />${t('dash.last_check')}: ${sched.last_full_check ? fmtDateTime(sched.last_full_check) : t('common.never')}</span>
      ${sched.next_full_check && html`<span><${Icon} name="calendar" />${t('dash.next_check')}: ${fmtRelative(sched.next_full_check)}</span>`}
      ${sched.check_running && html`<span><${Icon} name="refresh" class="spin" />${t('dash.checking', { done: sched.check_done, total: sched.check_total })}</span>`}
      ${sched.last_alert_run && html`<span><${Icon} name="bell" />${t('dash.alerts_run')}: ${fmtRelative(sched.last_alert_run)}</span>`}
      ${sched.last_alert_error && html`<span style=${{ color: 'var(--exp)' }}><${Icon} name="alertCircle" />${sched.last_alert_error}</span>`}
      ${!sched.scheduler && html`<span><${Icon} name="pause" />${t('dash.scheduler_off')}</span>`}
    </div>`}

    ${adding && html`<${AddCertificateModal} onClose=${() => setAdding(false)} onDone=${() => { dash.reload(true); certs.reload(true); }} />`}
  </div>`;
}

export function notifTone(status) {
  return { sent: 'ok', pending: 'warning', failed: 'expired', cancelled: 'pending' }[status] || 'pending';
}
export function notifIcon(status) {
  return { sent: 'check', pending: 'clock', failed: 'x', cancelled: 'minus' }[status] || 'info';
}
export { fmtDaysLeft };
