// Settings → Messengers: messenger providers (on/off, shared settings) and
// the channels that receive group alerts.
import { html, useState } from '../../vendor/preact-htm.js';
import { Icon } from '../components/icons.js';
import { Button, Callout, Card, Empty, ErrorState, Menu, Skeleton, confirmDialog, toastError } from '../components/ui.js';
import { ChannelModal, ProviderModal, channelIcon } from '../components/channels.js';
import { GroupChips } from '../components/status.js';
import { del, get, post, put } from '../lib/api.js';
import { toast } from '../lib/store.js';
import { useAsync } from '../lib/hooks.js';
import { t, tp } from '../lib/i18n.js';

function ProviderRow({ p, onChanged, onConfigure }) {
  const [busy, setBusy] = useState(false);
  const toggle = async (enabled) => {
    if (enabled && !p.configured && (p.settings || []).length) {
      onConfigure();
      return;
    }
    setBusy(true);
    try {
      onChanged(await put('/channel-providers/' + p.type, { enabled, settings: p.values }));
      toast(enabled ? t('channels.provider_on', { type: p.label }) : t('channels.provider_off', { type: p.label }));
    } catch (e) {
      toastError(e);
    } finally {
      setBusy(false);
    }
  };
  return html`<div class="list-item" style=${{ alignItems: 'flex-start', gap: '14px' }}>
    <span class="modal-icon" style=${{ width: '34px', height: '34px', flex: 'none' }}><${Icon} name=${channelIcon(p.type)} /></span>
    <div class="cell-main grow">
      <span class="row-wrap" style=${{ gap: '8px' }}>
        <b style=${{ fontWeight: 600 }}>${p.label}</b>
        ${p.enabled && !p.configured
          ? html`<span class="badge tone-warning">${t('channels.needs_setup')}</span>`
          : html`<span class=${'badge tone-' + (p.enabled ? 'ok' : 'pending')}>${p.enabled ? t('channels.on') : t('channels.off')}</span>`}
        ${p.channels > 0 && html`<span class="subtitle">${tp('unit.channels', p.channels)}</span>`}
      </span>
      <span class="meta">${p.description}</span>
    </div>
    ${(p.settings || []).length > 0 && html`<${Button} size="sm" icon="sliders" onClick=${onConfigure}>${t('channels.configure')}<//>`}
    <label class="switch" style=${{ marginTop: '4px' }} title=${p.enabled ? t('channels.disable') : t('channels.enable')}>
      <input type="checkbox" role="switch" checked=${p.enabled} disabled=${busy} aria-label=${p.label}
        onChange=${(e) => toggle(e.target.checked)} />
      <span class="track"></span>
    </label>
  </div>`;
}

export function Messengers() {
  const providers = useAsync(() => get('/channel-providers'), []);
  const channels = useAsync(() => get('/channels'), []);
  const groups = useAsync(() => get('/groups'), []);
  const [editing, setEditing] = useState(null); // {} = new channel
  const [configuring, setConfiguring] = useState(null);
  const [testing, setTesting] = useState(0);
  if (providers.error || channels.error) {
    return html`<${ErrorState} error=${providers.error || channels.error} onRetry=${() => { providers.reload(); channels.reload(); }} />`;
  }
  if (!providers.data || !channels.data) return html`<div class="card"><${Skeleton} rows=${6} /></div>`;
  const plist = providers.data;
  const label = (type) => plist.find((p) => p.type === type)?.label || type;
  const reloadAll = () => { providers.reload(true); channels.reload(true); };

  const setEnabled = async (c, enabled) => {
    try {
      await put('/channels/' + c.id, { name: c.name, enabled });
      channels.reload(true);
    } catch (e) {
      toastError(e);
    }
  };
  const test = async (c) => {
    setTesting(c.id);
    try {
      const r = await post('/channels/test', { id: c.id });
      if (r.ok) toast(t('channels.test_ok', { ms: r.duration_ms }));
      else toastError({ message: r.error }, t('channels.test_failed'));
    } catch (e) {
      toastError(e);
    } finally {
      setTesting(0);
    }
  };
  const remove = async (c) => {
    if (!(await confirmDialog({ title: t('channels.delete_title'), message: t('channels.delete_text', { name: c.name }), confirmText: t('common.delete') }))) return;
    try {
      await del('/channels/' + c.id);
      toast(t('channels.deleted'));
      reloadAll();
    } catch (e) {
      toastError(e);
    }
  };

  return html`<div class="stack">
    <${Card} title=${t('channels.providers')} sub=${t('channels.providers_sub')} flush>
      <div class="list">${plist.map((p) => html`<${ProviderRow} key=${p.type} p=${p}
        onChanged=${(list) => { providers.setData(list); channels.reload(true); }} onConfigure=${() => setConfiguring(p)} />`)}</div>
    <//>

    <${Card} title=${t('channels.title')} sub=${t('channels.sub')} flush
      tools=${html`<${Button} kind="primary" size="sm" icon="plus" onClick=${() => setEditing({})}>${t('channels.new')}<//>`}>
      ${channels.data.length === 0
        ? html`<${Empty} icon="message" title=${t('channels.empty_title')}
            action=${html`<${Button} kind="primary" icon="plus" onClick=${() => setEditing({})}>${t('channels.new')}<//>`}>${t('channels.empty_text')}<//>`
        : html`<div class="table-wrap"><table class="table"><thead><tr>
            <th>${t('common.name')}</th><th>${t('channels.type')}</th><th>${t('nav.groups')}</th><th>${t('channels.state')}</th><th></th>
          </tr></thead><tbody>${channels.data.map((c) => html`<tr class="click" onClick=${() => setEditing(c)}>
            <td><div class="cell-main"><b style=${{ fontWeight: 550 }}>${c.name}</b>
              ${c.summary && html`<span class="subtitle mono truncate" style=${{ maxWidth: '260px' }}>${c.summary}</span>`}</div></td>
            <td class="nowrap"><span class="row" style=${{ gap: '6px' }}><${Icon} name=${channelIcon(c.type)} size="15" />${label(c.type)}</span></td>
            <td>${c.group_ids.length
              ? html`<${GroupChips} ids=${c.group_ids} groups=${groups.data} max=${3} />`
              : html`<span class="faint">${t('channels.no_groups')}</span>`}</td>
            <td>${!c.enabled ? html`<span class="badge tone-pending">${t('channels.paused')}</span>`
              : !c.available ? html`<span class="badge tone-warning" title=${t('channels.unavailable_hint')}>${t('channels.unavailable')}</span>`
              : html`<span class="badge tone-ok">${t('channels.active')}</span>`}</td>
            <td class="actions" onClick=${(e) => e.stopPropagation()}>
              <${Button} kind="ghost" size="xs" icon="send" loading=${testing === c.id} title=${t('channels.send_test')}
                aria-label=${t('channels.send_test')} onClick=${() => test(c)} />
              <${Menu} items=${[
                { icon: 'edit', label: t('common.edit'), onClick: () => setEditing(c) },
                c.enabled
                  ? { icon: 'pause', label: t('channels.pause'), onClick: () => setEnabled(c, false) }
                  : { icon: 'play', label: t('channels.resume'), onClick: () => setEnabled(c, true) },
                '-',
                { icon: 'trash', label: t('common.delete'), danger: true, onClick: () => remove(c) },
              ]} />
            </td>
          </tr>`)}</tbody></table></div>`}
    <//>
    <${Callout} tone="accent" icon="info">${t('channels.note')}<//>

    ${editing && html`<${ChannelModal} channel=${editing.id ? editing : null} providers=${plist} groups=${groups.data || []}
      onClose=${() => setEditing(null)} onSaved=${reloadAll} />`}
    ${configuring && html`<${ProviderModal} provider=${configuring} onClose=${() => setConfiguring(null)}
      onSaved=${(list) => {
        const p = list.find((x) => x.type === configuring.type);
        // Enabling a messenger that needed its settings first.
        if (p && !p.enabled && p.configured) {
          put('/channel-providers/' + p.type, { enabled: true, settings: p.values }).then(providers.setData).catch(toastError);
        } else providers.setData(list);
        channels.reload(true);
      }} />`}
  </div>`;
}
