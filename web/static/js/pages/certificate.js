// Certificate detail.
import { html, useState } from '../../vendor/preact-htm.js';
import { Icon } from '../components/icons.js';
import { Button, Card, Callout, CopyButton, Empty, ErrorState, Menu, PageLoader, confirmDialog, toastError } from '../components/ui.js';
import { PageHead } from '../components/layout.js';
import { GroupSelect } from '../components/pickers.js';
import { GroupChips, LifetimeBar, StatusBadge, TrustBadge, certTitle, toneOf } from '../components/status.js';
import { EditCertificateModal } from './certificates.js';
import { notifTone } from './dashboard.js';
import { del, get, post, put } from '../lib/api.js';
import { app, toast } from '../lib/store.js';
import { Link, navigate } from '../lib/router.js';
import { useAsync, useTitle } from '../lib/hooks.js';
import { t, tp, fmtDate, fmtDateTime, fmtDaysLeft, fmtRelative, formatNumber, thresholdLabel } from '../lib/i18n.js';

function fingerprint(hex) {
  return (hex || '').toUpperCase().match(/.{1,2}/g)?.join(':') || '';
}

export function CertificateDetail({ params }) {
  const me = app.get().me;
  const isAdmin = me.user.role === 'admin';
  const id = Number(params.id);
  const data = useAsync(() => get('/certificates/' + id), [id]);
  const groups = useAsync(() => get('/groups'), []);
  const [checking, setChecking] = useState(false);
  const [editing, setEditing] = useState(false);
  const [addGroups, setAddGroups] = useState([]);
  const [savingGroups, setSavingGroups] = useState(false);
  const cert = data.data?.certificate;
  useTitle(cert ? certTitle(cert) : t('nav.certificates'), me.app.name);

  if (data.error) return html`<div class="page"><${ErrorState} error=${data.error} onRetry=${data.reload} /></div>`;
  if (!cert) return html`<${PageLoader} />`;

  const setCert = (c) => data.setData((d) => ({ ...d, certificate: c }));
  const check = async () => {
    setChecking(true);
    try {
      const c = await post(`/certificates/${id}/check`);
      setCert(c);
      toast(c.check_failed ? t('cert.check_failed_toast', { error: c.last_error }) : t('cert.checked_toast'),
        { tone: c.check_failed ? 'warning' : 'ok' });
    } catch (e) {
      toastError(e);
    } finally {
      setChecking(false);
    }
  };
  const remove = async () => {
    if (!(await confirmDialog({ title: t('cert.delete_title'), message: t('cert.delete_text', { name: certTitle(cert) }), confirmText: t('common.delete') }))) return;
    try {
      await del('/certificates/' + id);
      toast(t('cert.deleted'));
      navigate('/certificates');
    } catch (e) {
      toastError(e);
    }
  };
  const toggleEnabled = async () => {
    try {
      const c = await put('/certificates/' + id, { url: cert.url, name: cert.name, notes: cert.notes, enabled: !cert.enabled });
      setCert(c);
      toast(c.enabled ? t('cert.resumed') : t('cert.paused'));
    } catch (e) {
      toastError(e);
    }
  };
  const saveGroups = async () => {
    setSavingGroups(true);
    try {
      const c = await post(`/certificates/${id}/groups`, { group_ids: addGroups });
      setCert(c);
      setAddGroups([]);
      toast(t('cert.groups_added'));
      groups.reload(true);
    } catch (e) {
      toastError(e);
    } finally {
      setSavingGroups(false);
    }
  };
  const removeGroup = async (gid) => {
    const g = groups.data?.find((x) => x.id === gid);
    if (!(await confirmDialog({ title: t('cert.remove_group_title'), message: t('cert.remove_group_text', { group: g?.name || gid }), confirmText: t('common.remove') }))) return;
    try {
      await del(`/groups/${gid}/certificates/${id}`);
      setCert({ ...cert, group_ids: cert.group_ids.filter((x) => x !== gid) });
      toast(t('cert.group_removed'));
    } catch (e) {
      toastError(e);
    }
  };

  const tone = toneOf(cert);
  const leaf = cert.chain?.[0];
  const earlyChain = (cert.chain || []).slice(1).filter((c) => cert.not_after && new Date(c.not_after) < new Date(cert.not_after));
  const notifs = data.data.notifications || [];
  const freeGroups = (groups.data || []).filter((g) => !cert.group_ids.includes(g.id));

  return html`<div class="page stack">
    <${PageHead} title=${certTitle(cert)}
      crumbs=${[{ label: t('nav.certificates'), href: '/certificates' }, { label: cert.host }]}
      actions=${html`
        <a class="btn" href=${cert.url.startsWith('https://') ? cert.url : 'https://' + cert.host + ':' + cert.port} target="_blank" rel="noopener noreferrer"><${Icon} name="external" />${t('cert.open')}</a>
        <${Button} icon="refresh" loading=${checking} onClick=${check}>${t('cert.check_now')}<//>
        ${isAdmin && html`<${Button} icon="edit" onClick=${() => setEditing(true)}>${t('common.edit')}<//>
          <${Menu} items=${[
            { icon: cert.enabled ? 'pause' : 'play', label: cert.enabled ? t('cert.pause') : t('cert.resume'), onClick: toggleEnabled },
            '-',
            { icon: 'trash', label: t('common.delete'), danger: true, onClick: remove },
          ]} size="" />`}`}>
      <div class="row-wrap" style=${{ marginTop: '10px' }}>
        <${StatusBadge} status=${cert.status} enabled=${cert.enabled} />
        <${TrustBadge} cert=${cert} />
        <span class="endpoint">${cert.url}</span>
        ${cert.resolved_ip && html`<span class="badge outline square mono">${cert.resolved_ip}</span>`}
      </div>
    <//>

    ${cert.check_failed && html`<${Callout} tone="error" icon="wifiOff">
      <b>${t('cert.last_check_failed')}</b> <span class="mono">${cert.last_error}</span>
      ${cert.last_success_at && html`<div class="muted" style=${{ marginTop: '4px' }}>${t('cert.data_from', { time: fmtDateTime(cert.last_success_at) })}</div>`}
    <//>`}
    ${cert.not_after && !cert.trusted && html`<${Callout} tone="expired" icon="shieldAlert">
      <b>${t('cert.untrusted_title')}</b> <span class="mono">${cert.verify_error}</span>
    <//>`}
    ${!cert.enabled && html`<${Callout} tone="pending" icon="pause">${t('cert.paused_note')}<//>`}

    <div class="grid grid-main">
      <div class="stack">
        <${Card} class="reveal" style=${{ '--i': 1 }} title=${t('cert.validity')} sub=${cert.not_after ? fmtDaysLeft(cert.days_left) : ''}>
          ${cert.not_after ? html`<div class=${'stack tone-' + tone}>
              <div class="countdown">
                <span class="big">${formatNumber(cert.days_left < 0 ? -cert.days_left - 1 : cert.days_left)}</span>
                <span class="unit">${cert.days_left < 0
                  ? tp('unit.dw', -cert.days_left - 1) + ' ' + t('cert.since_expiry')
                  : tp('unit.dw', cert.days_left) + ' ' + t('cert.left_suffix')}</span>
                <span class="spacer"></span>
                <div style=${{ textAlign: 'right' }}>
                  <div class="section-title" style=${{ justifyContent: 'flex-end' }}>${t('cert.expires')}</div>
                  <div class="mono" style=${{ fontSize: '15px', fontWeight: 650 }}>${fmtDateTime(cert.not_after)}</div>
                </div>
              </div>
              <${LifetimeBar} cert=${cert} thresholds=${me.app.thresholds} />
              <div class="thresholds">
                <span class="muted" style=${{ fontSize: '12.5px' }}>${t('cert.alerts_at')}</span>
                ${me.app.thresholds.map((d) => html`<span class=${'threshold-chip tone-' + (cert.days_left !== null && cert.days_left <= d ? (d <= me.app.critical_days ? 'critical' : 'warning') : 'pending')}>${d}d</span>`)}
                ${me.app.notify_expired && html`<span class=${'threshold-chip tone-' + (cert.days_left < 0 ? 'expired' : 'pending')}>${t('threshold.expired')}</span>`}
              </div>
            </div>`
          : html`<${Empty} icon="hourglass" title=${t('cert.no_data')}>${cert.check_failed ? cert.last_error : t('cert.no_data_text')}<//>`}
        <//>

        ${cert.not_after && html`<${Card} class="reveal" style=${{ '--i': 2 }} title=${t('cert.details')}>
          <dl class="kv">
            <dt>${t('cert.subject')}</dt><dd><b>${cert.subject_cn}</b>${cert.subject_org && html` <span class="muted">· ${cert.subject_org}</span>`}</dd>
            <dt>${t('cert.issuer')}</dt><dd>${cert.issuer_cn}${cert.issuer_org && html` <span class="muted">· ${cert.issuer_org}</span>`}</dd>
            <dt>${t('cert.sans')}</dt><dd><div class="sans">${(cert.sans || []).slice(0, 60).map((s) => html`<code>${s}</code>`)}
              ${(cert.sans || []).length > 60 && html`<span class="muted">+${cert.sans.length - 60}</span>`}</div></dd>
            <dt>${t('cert.valid_from')}</dt><dd class="mono">${fmtDateTime(cert.not_before)}</dd>
            <dt>${t('cert.valid_to')}</dt><dd class="mono">${fmtDateTime(cert.not_after)}</dd>
            <dt>${t('cert.key')}</dt><dd>${cert.key_algorithm}${cert.key_bits ? ` · ${cert.key_bits} bit` : ''}</dd>
            <dt>${t('cert.signature')}</dt><dd>${cert.signature_algorithm}</dd>
            <dt>${t('cert.tls')}</dt><dd>${cert.tls_version}</dd>
            <dt>${t('cert.serial')}</dt><dd><span class="copyable"><code>${cert.serial_number}</code><${CopyButton} text=${cert.serial_number} /></span></dd>
            <dt>SHA-256</dt><dd><span class="copyable"><code style=${{ fontSize: '11.5px' }}>${fingerprint(cert.fingerprint_sha256)}</code><${CopyButton} text=${fingerprint(cert.fingerprint_sha256)} /></span></dd>
            <dt>${t('cert.last_checked')}</dt><dd>${fmtDateTime(cert.last_checked_at)} <span class="muted">(${fmtRelative(cert.last_checked_at)})</span></dd>
          </dl>
        <//>`}

        ${cert.chain?.length > 0 && html`<${Card} class="reveal" style=${{ '--i': 3 }} title=${t('cert.chain')} sub=${tp('unit.certs', cert.chain.length)}>
          ${earlyChain.length > 0 && html`<div style=${{ marginBottom: '12px' }}><${Callout} tone="warning" icon="alert">${t('cert.chain_early')}<//></div>`}
          <div class="chain">${cert.chain.map((c, i) => {
            const early = i > 0 && earlyChain.includes(c);
            return html`<div class=${'chain-item' + (i === 0 ? ' leaf' : '')}>
              <span class="node"><${Icon} name=${i === 0 ? 'award' : 'shield'} /></span>
              <div class="cell-main"><span class="title">${c.subject}</span>
                <span class="subtitle">${t('cert.issued_by', { issuer: c.issuer })}</span></div>
              <div style=${{ textAlign: 'right' }}>
                <div class="mono" style=${{ fontSize: '12px', color: early ? 'var(--warn)' : 'var(--text-2)' }}>${fmtDate(c.not_after)}</div>
                <div class="faint" style=${{ fontSize: '11px' }}>${i === 0 ? t('cert.leaf') : c.is_ca ? t('cert.intermediate') : ''}</div>
              </div>
            </div>`;
          })}</div>
        <//>`}
      </div>

      <div class="stack">
        <${Card} class="reveal" style=${{ '--i': 2 }} title=${t('nav.groups')} sub=${t('cert.groups_sub')}>
          <div class="stack-sm">
            <${GroupChips} ids=${cert.group_ids} groups=${groups.data} max=${50} onRemove=${isAdmin ? removeGroup : null} />
            ${cert.group_ids.length === 0 && html`<${Callout} tone="warning" icon="bell">${t('cert.no_groups_warning')}<//>`}
            ${freeGroups.length > 0 && html`<div class="divider"></div>
              <${GroupSelect} groups=${freeGroups} value=${addGroups} onChange=${setAddGroups} label=${t('cert.add_to_groups')} />
              <div class="form-actions"><${Button} kind="primary" size="sm" icon="plus" disabled=${!addGroups.length} loading=${savingGroups} onClick=${saveGroups}>${t('common.add')}<//></div>`}
          </div>
        <//>

        ${cert.notes && html`<${Card} class="reveal" style=${{ '--i': 3 }} title=${t('certs.field_notes')}>
          <p style=${{ whiteSpace: 'pre-wrap' }}>${cert.notes}</p>
        <//>`}

        <${Card} class="reveal" style=${{ '--i': 4 }} flush title=${t('cert.notifications')} sub=${t('cert.notifications_sub')}>
          ${notifs.length === 0 ? html`<${Empty} icon="bell" title=${t('notif.empty_title')}>${t('cert.notifications_empty')}<//>`
            : html`<div class="list">${notifs.map((n) => html`<div class="list-item">
                <span class=${'badge tone-' + notifTone(n.status)}>${t('notif.status_' + n.status)}</span>
                <div class="cell-main grow"><span class="truncate" style=${{ fontWeight: 550 }}>${n.recipient_email}</span>
                  <span class="meta truncate">${n.group_name} · ${thresholdLabel(n.threshold_days)}</span></div>
                <span class="meta nowrap" title=${fmtDateTime(n.sent_at || n.created_at)}>${fmtRelative(n.sent_at || n.created_at)}</span>
              </div>`)}</div>`}
        <//>

        <${Card} class="reveal" style=${{ '--i': 5 }} title=${t('cert.meta')}>
          <dl class="kv">
            <dt>${t('cert.endpoint')}</dt><dd class="mono">${cert.host}:${cert.port}</dd>
            <dt>${t('cert.added')}</dt><dd>${fmtDateTime(cert.created_at)}</dd>
            <dt>${t('cert.monitoring')}</dt><dd>${cert.enabled ? t('common.enabled') : t('common.paused')}</dd>
          </dl>
        <//>
      </div>
    </div>

    ${editing && html`<${EditCertificateModal} cert=${cert} onClose=${() => setEditing(false)} onSaved=${(c) => setCert(c)} />`}
  </div>`;
}

export { Link };
