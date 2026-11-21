// Groups: list and detail (certificates + recipients of a group).
import { html, useState } from '../../vendor/preact-htm.js';
import { Icon } from '../components/icons.js';
import {
  Button, Callout, Card, Empty, ErrorState, FormError, Menu, Modal, PageLoader, Skeleton, TextField, confirmDialog, toastError,
} from '../components/ui.js';
import { PageHead } from '../components/layout.js';
import { CertPicker } from '../components/pickers.js';
import { DaysLeft, LifetimeRing, StatusBadge, certTitle, groupColor } from '../components/status.js';
import { notifTone } from './dashboard.js';
import { del, get, post, put } from '../lib/api.js';
import { app, toast } from '../lib/store.js';
import { Link, navigate } from '../lib/router.js';
import { useAsync, useTitle } from '../lib/hooks.js';
import { t, tp, fmtDate, fmtDateISO, fmtRelative, fmtDateTime, thresholdLabel } from '../lib/i18n.js';

const ORDER = ['expired', 'critical', 'error', 'warning', 'pending', 'ok', 'disabled'];

function HealthBar({ counts }) {
  const total = ORDER.reduce((s, k) => s + (counts[k] || 0), 0);
  if (!total) return html`<div class="healthbar"></div>`;
  return html`<div class="healthbar" role="img" aria-label=${ORDER.filter((k) => counts[k]).map((k) => `${t('status.' + k)}: ${counts[k]}`).join(', ')}>
    ${ORDER.filter((k) => counts[k]).map((k) => html`<span class=${'tone-' + k} style=${{ flex: counts[k] }} title=${`${t('status.' + k)}: ${counts[k]}`}></span>`)}
  </div>`;
}

export function GroupModal({ group, onClose, onSaved }) {
  const [name, setName] = useState(group?.name || '');
  const [description, setDescription] = useState(group?.description || '');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);
  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      const g = group ? await put('/groups/' + group.id, { name, description }) : await post('/groups', { name, description });
      toast(group ? t('common.saved') : t('groups.created', { name: g.name }));
      onSaved?.(g);
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return html`<${Modal} title=${group ? t('groups.edit_title') : t('groups.new_title')} sub=${group ? '' : t('groups.new_sub')}
    icon="layers" onClose=${onClose} onSubmit=${submit}
    footer=${html`<${Button} onClick=${onClose}>${t('common.cancel')}<//><${Button} kind="primary" type="submit" loading=${busy} disabled=${!name.trim()}>${group ? t('common.save') : t('common.create')}<//>`}>
    <div class="stack">
      ${error && !error.fields && html`<${FormError} error=${error} />`}
      <${TextField} label=${t('common.name')} value=${name} onInput=${setName} required placeholder=${t('groups.name_ph')} error=${error?.fields?.name} />
      <${TextField} textarea label=${t('common.description')} value=${description} onInput=${setDescription} rows="3"
        placeholder=${t('groups.description_ph')} error=${error?.fields?.description} hint=${t('common.optional')} />
    </div>
  <//>`;
}

export function Groups() {
  const me = app.get().me;
  const isAdmin = me.user.role === 'admin';
  const [creating, setCreating] = useState(false);
  const groups = useAsync(() => get('/groups'), []);
  useTitle(t('nav.groups'), me.app.name);
  if (groups.error) return html`<div class="page"><${ErrorState} error=${groups.error} onRetry=${groups.reload} /></div>`;
  return html`<div class="page stack">
    <${PageHead} title=${t('nav.groups')} sub=${t('groups.sub')}
      actions=${isAdmin && html`<${Button} kind="primary" icon="plus" onClick=${() => setCreating(true)}>${t('groups.new')}<//>`} />
    ${!groups.data ? html`<div class="grid grid-3">${[0, 1, 2].map(() => html`<div class="card"><${Skeleton} rows=${4} /></div>`)}</div>`
      : groups.data.length === 0 ? html`<div class="card"><${Empty} icon="layers" title=${t('groups.empty_title')}
          action=${isAdmin && html`<${Button} kind="primary" icon="plus" onClick=${() => setCreating(true)}>${t('groups.new')}<//>`}>
          ${isAdmin ? t('groups.empty_text') : t('groups.empty_text_user')}<//></div>`
      : html`<div class="grid grid-3">${groups.data.map((g, i) => html`<${Link} href=${'/groups/' + g.id} class="card reveal"
          style=${{ '--i': i, color: 'var(--text)', display: 'flex', flexDirection: 'column' }}>
          <div class="card-body stack-sm" style=${{ flex: 1 }}>
            <div class="row">
              <span style=${{ width: '10px', height: '10px', borderRadius: '3px', background: groupColor(g.id), flex: 'none' }}></span>
              <h3 class="truncate grow" style=${{ fontSize: '15.5px', fontWeight: 650 }}>${g.name}</h3>
              ${g.health.worst && html`<${StatusBadge} status=${g.health.worst} />`}
            </div>
            <p class="muted" style=${{ minHeight: '21px', fontSize: '13px', display: '-webkit-box', WebkitLineClamp: 2, WebkitBoxOrient: 'vertical', overflow: 'hidden' }}>
              ${g.description || t('groups.no_description')}</p>
            <${HealthBar} counts=${g.health.counts} />
          </div>
          <div class="card-foot" style=${{ fontSize: '12.5px', color: 'var(--muted)', gap: '16px' }}>
            <span class="row" style=${{ gap: '6px' }}><${Icon} name="shieldCheck" size="14" />${tp('unit.certs', g.certificate_count)}</span>
            <span class="row" style=${{ gap: '6px' }}><${Icon} name="mail" size="14" />${tp('unit.recipients', g.recipient_count)}</span>
            <span class="spacer"></span>
            ${g.health.min_days_left !== null && html`<span class="mono" title=${t('groups.min_days')}>${g.health.min_days_left < 0 ? t('status.expired') : '≤ ' + tp('unit.days', g.health.min_days_left)}</span>`}
          </div>
        <//>`)}</div>`}
    ${creating && html`<${GroupModal} onClose=${() => setCreating(false)} onSaved=${(g) => navigate('/groups/' + g.id)} />`}
  </div>`;
}

export function GroupDetail({ params }) {
  const me = app.get().me;
  const isAdmin = me.user.role === 'admin';
  const id = Number(params.id);
  const data = useAsync(() => get('/groups/' + id), [id]);
  const [editing, setEditing] = useState(false);
  const [picking, setPicking] = useState(false);
  const [allCerts, setAllCerts] = useState(null);
  const [emails, setEmails] = useState('');
  const [rcptError, setRcptError] = useState(null);
  const [addingRcpt, setAddingRcpt] = useState(false);
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState(null);
  const g = data.data?.group;
  useTitle(g?.name || t('nav.groups'), me.app.name);
  if (data.error) return html`<div class="page"><${ErrorState} error=${data.error} onRetry=${data.reload} /></div>`;
  if (!g) return html`<${PageLoader} />`;
  const { certificates, recipients, notifications } = data.data;

  const openPicker = async () => {
    try {
      setAllCerts(await get('/certificates'));
      setPicking(true);
    } catch (e) {
      toastError(e);
    }
  };
  const addCerts = async (ids) => {
    try {
      const r = await post(`/groups/${id}/certificates`, { certificate_ids: ids });
      toast(t('group.certs_added', { n: tp('unit.certs', r.added) }));
      setPicking(false);
      data.reload(true);
    } catch (e) {
      toastError(e);
    }
  };
  const removeCert = async (c) => {
    if (!(await confirmDialog({ title: t('group.remove_cert_title'), message: t('group.remove_cert_text', { name: certTitle(c), group: g.name }), confirmText: t('common.remove') }))) return;
    try {
      await del(`/groups/${id}/certificates/${c.id}`);
      data.reload(true);
    } catch (e) {
      toastError(e);
    }
  };
  const addRecipients = async (e) => {
    e.preventDefault();
    setAddingRcpt(true);
    setRcptError(null);
    try {
      const r = await post(`/groups/${id}/recipients`, { text: emails });
      toast(t('group.rcpt_added', { n: tp('unit.recipients', r.added) }));
      setEmails('');
      data.reload(true);
    } catch (err) {
      setRcptError(err);
    } finally {
      setAddingRcpt(false);
    }
  };
  const removeRecipient = async (r) => {
    if (!(await confirmDialog({ title: t('group.remove_rcpt_title'), message: t('group.remove_rcpt_text', { email: r.email, group: g.name }), confirmText: t('common.remove') }))) return;
    try {
      await del(`/groups/${id}/recipients/${r.id}`);
      data.reload(true);
    } catch (e) {
      toastError(e);
    }
  };
  const removeGroup = async () => {
    if (!(await confirmDialog({ title: t('groups.delete_title'), message: t('groups.delete_text', { name: g.name }), confirmText: t('common.delete') }))) return;
    try {
      await del('/groups/' + id);
      toast(t('groups.deleted'));
      navigate('/groups');
    } catch (e) {
      toastError(e);
    }
  };
  const sendTest = async () => {
    setTesting(true);
    try {
      setTestResult(await post(`/groups/${id}/test`));
    } catch (e) {
      toastError(e);
    } finally {
      setTesting(false);
    }
  };

  const minDays = g.health.min_days_left;
  return html`<div class="page stack">
    <${PageHead} title=${g.name} sub=${g.description}
      crumbs=${[{ label: t('nav.groups'), href: '/groups' }, { label: g.name }]}
      actions=${html`
        ${isAdmin && html`<${Button} icon="send" loading=${testing} onClick=${sendTest} disabled=${!recipients.length}>${t('group.send_test')}<//>`}
        <${Button} kind="primary" icon="plus" onClick=${openPicker}>${t('group.add_certs')}<//>
        ${isAdmin && html`<${Menu} size="" items=${[
          { icon: 'edit', label: t('common.edit'), onClick: () => setEditing(true) },
          '-',
          { icon: 'trash', label: t('groups.delete'), danger: true, onClick: removeGroup },
        ]} />`}`} />

    <div class="kpis" style=${{ gridTemplateColumns: 'repeat(auto-fit, minmax(170px, 1fr))' }}>
      <div class="kpi tone-accent reveal" style=${{ '--i': 0 }}><div class="label"><${Icon} name="shieldCheck" />${t('nav.certificates')}</div><div class="value">${certificates.length}</div></div>
      <div class="kpi tone-accent reveal" style=${{ '--i': 1 }}><div class="label"><${Icon} name="mail" />${t('nav.recipients')}</div><div class="value">${recipients.length}</div></div>
      <div class=${'kpi reveal tone-' + ({ expired: 'exp', critical: 'crit', warning: 'warn', error: 'err' }[g.health.worst] || 'ok')} style=${{ '--i': 2 }}>
        <div class="label"><${Icon} name="hourglass" />${t('groups.min_days')}</div>
        <div class="value">${minDays === null || minDays === undefined ? '—' : minDays < 0 ? '−' + (-minDays - 1) : minDays}</div>
        <div class="meta">${minDays === null || minDays === undefined ? t('groups.no_data') : minDays < 0 ? t('status.expired') + ' · ' + tp('unit.days', -minDays - 1) : tp('unit.dw', minDays)}</div>
      </div>
      <div class="kpi reveal" style=${{ '--i': 3 }}><div class="label"><${Icon} name="activity" />${t('groups.health')}</div>
        <div style=${{ marginTop: '18px' }}><${HealthBar} counts=${g.health.counts} /></div>
        <div class="meta">${g.health.worst ? t('status.' + g.health.worst) : t('groups.no_data')}</div></div>
    </div>

    <div class="grid grid-main">
      <${Card} class="reveal" style=${{ '--i': 4 }} flush title=${t('nav.certificates')} sub=${t('group.certs_sub')}>
        ${certificates.length === 0 ? html`<${Empty} icon="shieldCheck" title=${t('group.no_certs')}
            action=${html`<${Button} icon="plus" onClick=${openPicker}>${t('group.add_certs')}<//>`}>${t('group.no_certs_text')}<//>`
          : html`<div class="table-wrap"><table class="table"><thead><tr>
              <th>${t('certs.col.endpoint')}</th><th>${t('certs.col.status')}</th><th>${t('certs.col.expires')}</th>
              <th class="num">${t('certs.col.left')}</th>${isAdmin && html`<th></th>`}
            </tr></thead><tbody>${certificates.map((c) => html`<tr class="click" onClick=${() => navigate('/certificates/' + c.id)}>
              <td style=${{ maxWidth: '300px' }}><div class="row"><${LifetimeRing} cert=${c} />
                <div class="cell-main"><span class="title truncate">${certTitle(c)}</span><span class="endpoint truncate">${c.url}</span></div></div></td>
              <td><${StatusBadge} status=${c.status} enabled=${c.enabled} /></td>
              <td class="mono nowrap" style=${{ fontSize: '12.5px' }} title=${fmtDate(c.not_after)}>${fmtDateISO(c.not_after)}</td>
              <td class="num"><${DaysLeft} cert=${c} /></td>
              ${isAdmin && html`<td class="actions"><${Button} kind="ghost" size="xs" icon="x" title=${t('common.remove')}
                aria-label=${t('common.remove')} onClick=${(e) => { e.stopPropagation(); removeCert(c); }} /></td>`}
            </tr>`)}</tbody></table></div>`}
      <//>

      <div class="stack">
        <${Card} class="reveal" style=${{ '--i': 5 }} flush title=${t('nav.recipients')} sub=${t('group.rcpt_sub')}>
          ${recipients.length === 0 ? html`<${Empty} icon="mail" title=${t('group.no_rcpt')}>${isAdmin ? t('group.no_rcpt_text') : t('group.no_rcpt_text_user')}<//>`
            : html`<div class="list">${recipients.map((r) => html`<div class="list-item">
                <span class="avatar" style=${{ width: '28px', height: '28px', fontSize: '11px' }}>${(r.name || r.email)[0].toUpperCase()}</span>
                <div class="cell-main grow"><span class="truncate" style=${{ fontWeight: 550 }}>${r.email}</span>
                  ${r.name && html`<span class="meta truncate">${r.name}</span>`}</div>
                ${isAdmin && html`<${Button} kind="ghost" size="xs" icon="x" title=${t('common.remove')} aria-label=${t('common.remove')} onClick=${() => removeRecipient(r)} />`}
              </div>`)}</div>`}
          ${isAdmin && html`<form class="card-foot" style=${{ flexDirection: 'column', alignItems: 'stretch' }} onSubmit=${addRecipients}>
            <${TextField} textarea rows="2" label=${t('group.add_rcpt')} value=${emails} onInput=${setEmails} inputClass="mono"
              placeholder="ops@example.com, oncall@example.com" hint=${t('group.add_rcpt_hint')}
              error=${rcptError?.fields?.emails || rcptError?.fields?.recipient_ids} />
            ${rcptError?.details?.invalid && html`<div class="error" style=${{ fontSize: '12px', color: 'var(--exp)' }}>${rcptError.details.invalid.join(', ')}</div>`}
            <div class="form-actions"><${Button} type="submit" kind="primary" size="sm" icon="plus" loading=${addingRcpt} disabled=${!emails.trim()}>${t('common.add')}<//></div>
          </form>`}
        <//>
        <${Callout} tone="accent" icon="lock">${t('group.isolation_note')}<//>
      </div>
    </div>

    <${Card} class="reveal" style=${{ '--i': 6 }} flush title=${t('group.history')}
      tools=${html`<${Link} href=${'/mail-log?group_id=' + id} class="btn btn-ghost btn-sm"><${Icon} name="send" />${t('nav.mail_log')}<//>
        <${Link} href=${'/notifications?group_id=' + id} class="btn btn-ghost btn-sm">${t('common.view_all')}<${Icon} name="arrowRight" /><//>`}>
      ${notifications.length === 0 ? html`<${Empty} icon="bell" title=${t('notif.empty_title')}>${t('group.history_empty')}<//>`
        : html`<div class="table-wrap"><table class="table"><thead><tr>
            <th>${t('notif.col.time')}</th><th>${t('notif.col.recipient')}</th><th>${t('notif.col.certificate')}</th>
            <th>${t('notif.col.threshold')}</th><th>${t('notif.col.status')}</th></tr></thead>
          <tbody>${notifications.map((n) => html`<tr>
            <td class="nowrap" title=${fmtDateTime(n.created_at)}>${fmtRelative(n.sent_at || n.created_at)}</td>
            <td>${n.recipient_email}</td><td class="endpoint">${n.certificate_url}</td>
            <td>${thresholdLabel(n.threshold_days)}</td>
            <td><span class=${'badge tone-' + notifTone(n.status)} title=${n.last_error}>${t('notif.status_' + n.status)}</span></td>
          </tr>`)}</tbody></table></div>`}
    <//>

    ${editing && html`<${GroupModal} group=${g} onClose=${() => setEditing(false)} onSaved=${() => data.reload(true)} />`}
    ${picking && html`<${CertPicker} title=${t('group.add_certs_title', { group: g.name })} certs=${allCerts || []}
      exclude=${certificates.map((c) => c.id)} onClose=${() => setPicking(false)} onPick=${addCerts} />`}
    ${testResult && html`<${Modal} title=${t('group.test_result')} icon="send" onClose=${() => setTestResult(null)}
      footer=${html`<${Button} kind="primary" onClick=${() => setTestResult(null)}>${t('common.done')}<//>`}>
      <div class="list card" style=${{ boxShadow: 'none' }}>${testResult.results.map((r) => html`<div class="list-item">
        <${Icon} name=${r.error ? 'xCircle' : 'checkCircle'} size="18" />
        <div class="cell-main grow"><span>${r.email}</span>${r.error && html`<span class="meta" style=${{ color: 'var(--exp)' }}>${r.error}</span>`}</div>
        <span class=${'badge tone-' + (r.error ? 'expired' : 'ok')}>${r.error ? t('notif.status_failed') : t('notif.status_sent')}</span>
      </div>`)}</div>
    <//>`}
  </div>`;
}
