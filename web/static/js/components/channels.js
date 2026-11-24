// Messenger channels: icons, the generic provider field form and the
// channel editor. Forms are built from the field descriptions the server
// sends for each provider, so new messengers need no UI changes.
import { html, useState } from '../../vendor/preact-htm.js';
import { Icon } from './icons.js';
import { Button, Callout, Check, FormError, Modal, SelectField, Switch, TextField } from './ui.js';
import { GroupSelect } from './pickers.js';
import { post, put } from '../lib/api.js';
import { toast } from '../lib/store.js';
import { t } from '../lib/i18n.js';

const ICONS = { email: 'mail', telegram: 'telegram', slack: 'hash', webhook: 'webhook' };

export function channelIcon(type) {
  return ICONS[type] || 'message';
}

// Display name of a channel type ("email" or a provider type).
export function channelLabel(type, providers) {
  if (!type || type === 'email') return t('channels.email');
  return providers?.find((p) => p.type === type)?.label || type.charAt(0).toUpperCase() + type.slice(1);
}

export function ChannelTag({ type, label }) {
  return html`<span class="row" style=${{ gap: '6px', display: 'inline-flex' }}>
    <${Icon} name=${channelIcon(type)} size="14" /><span>${label}</span>
  </span>`;
}

// FieldsForm renders provider fields. Secret fields are write-only: a
// stored secret shows as "set" and stays unchanged while left empty.
export function FieldsForm({ fields, values, onChange, secretsSet = [], clear = [], onClear, errors = {} }) {
  const set = (name) => (v) => onChange({ ...values, [name]: v });
  return html`<div class="stack">${fields.map((f) => {
    const err = errors[f.name];
    switch (f.kind) {
      case 'bool':
        return html`<${Check} checked=${values[f.name] === 'true'} onChange=${(v) => set(f.name)(v ? 'true' : 'false')}
          label=${f.label} hint=${f.help} />`;
      case 'select':
        return html`<${SelectField} label=${f.label} value=${values[f.name] || f.default || ''} onChange=${set(f.name)}
          options=${f.options} hint=${f.help} error=${err} />`;
      case 'secret': {
        const stored = secretsSet.includes(f.name) && !clear.includes(f.name);
        return html`<div class="stack-sm">
          <${TextField} label=${f.label} type="password" autocomplete="new-password" inputClass="mono"
            value=${values[f.name] || ''} onInput=${set(f.name)} required=${f.required && !stored}
            placeholder=${stored ? t('channels.secret_stored') : f.placeholder} hint=${f.help} error=${err} />
          ${secretsSet.includes(f.name) && !f.required && onClear && html`<${Check} checked=${clear.includes(f.name)}
            onChange=${(v) => onClear(v ? [...clear, f.name] : clear.filter((n) => n !== f.name))}
            label=${t('channels.secret_clear', { field: f.label })} />`}
        </div>`;
      }
      default:
        return html`<${TextField} label=${f.label} textarea=${f.kind === 'textarea'} rows="3"
          inputClass=${f.kind === 'url' || f.name.endsWith('_id') ? 'mono' : ''}
          type=${f.kind === 'url' ? 'url' : 'text'} value=${values[f.name] ?? ''} onInput=${set(f.name)}
          required=${f.required} placeholder=${f.placeholder || f.default} hint=${f.help} error=${err} />`;
    }
  })}</div>`;
}

function TestResult({ result }) {
  if (!result) return null;
  return result.ok
    ? html`<${Callout} tone="ok" icon="checkCircle">${t('channels.test_ok', { ms: result.duration_ms })}<//>`
    : html`<${Callout} tone="expired" icon="xCircle"><b>${t('channels.test_failed')}</b> <span class="mono">${result.error}</span><//>`;
}

// ChannelModal creates (channel = null) or edits a channel.
export function ChannelModal({ channel, providers, groups, onClose, onSaved }) {
  const usable = providers.filter((p) => p.enabled || p.type === channel?.type);
  const [type, setType] = useState(channel?.type || usable[0]?.type || '');
  const [name, setName] = useState(channel?.name || '');
  const [enabled, setEnabled] = useState(channel ? channel.enabled : true);
  const [config, setConfig] = useState(channel?.config || {});
  const [clear, setClear] = useState([]);
  const [groupIds, setGroupIds] = useState(channel?.group_ids || []);
  const [busy, setBusy] = useState(false);
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState(null);
  const [error, setError] = useState(null);
  const provider = providers.find((p) => p.type === type);
  const body = () => ({ type, name, enabled, config, clear, group_ids: groupIds });

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      const c = channel ? await put('/channels/' + channel.id, body()) : await post('/channels', body());
      toast(channel ? t('common.saved') : t('channels.created', { name: c.name }));
      onSaved?.(c);
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  const test = async () => {
    setTesting(true);
    setTestResult(null);
    setError(null);
    try {
      setTestResult(await post('/channels/test', { ...body(), id: channel?.id || 0, name: name || 'Test' }));
    } catch (e) {
      setError(e);
    } finally {
      setTesting(false);
    }
  };

  if (!provider) {
    return html`<${Modal} title=${t('channels.new_title')} icon="message" onClose=${onClose}
      footer=${html`<${Button} onClick=${onClose}>${t('common.close')}<//>`}>
      <${Callout} tone="warning" icon="alert">${t('channels.no_providers')}<//>
    <//>`;
  }
  const fe = error?.fields || {};
  return html`<${Modal} title=${channel ? t('channels.edit_title') : t('channels.new_title')} sub=${provider.description}
    icon=${channelIcon(type)} wide onClose=${onClose} onSubmit=${submit}
    footer=${html`
      <${Button} icon="send" loading=${testing} onClick=${test}>${t('channels.send_test')}<//>
      <span class="spacer"></span>
      <${Button} onClick=${onClose}>${t('common.cancel')}<//>
      <${Button} kind="primary" type="submit" loading=${busy} disabled=${!name.trim()}>${channel ? t('common.save') : t('common.create')}<//>`}>
    <div class="stack">
      ${error && !Object.keys(fe).length && html`<${FormError} error=${error} />`}
      ${!channel && usable.length > 1 && html`<div class="field"><span class="label">${t('channels.type')}</span>
        <div class="segmented" role="tablist">${usable.map((p) => html`<button type="button" role="tab"
          aria-selected=${type === p.type} class=${type === p.type ? 'on' : ''}
          onClick=${() => { setType(p.type); setConfig({}); setClear([]); setTestResult(null); }}>
          <${Icon} name=${channelIcon(p.type)} size="14" />${p.label}</button>`)}</div></div>`}
      ${!provider.configured && html`<${Callout} tone="warning" icon="alert">${t('channels.provider_not_configured', { type: provider.label })}<//>`}
      ${!provider.enabled && html`<${Callout} tone="warning" icon="alert">${t('channels.provider_disabled', { type: provider.label })}<//>`}
      <${TextField} label=${t('common.name')} value=${name} onInput=${setName} required placeholder=${t('channels.name_ph')}
        hint=${t('channels.name_hint')} error=${fe.name} />
      <${FieldsForm} fields=${provider.target || []} values=${config} onChange=${setConfig} secretsSet=${channel?.secrets_set || []}
        clear=${clear} onClear=${setClear} errors=${fe} />
      <${GroupSelect} groups=${groups} value=${groupIds} onChange=${setGroupIds} label=${t('nav.groups')} hint=${t('channels.groups_hint')} />
      <${Switch} checked=${enabled} onChange=${setEnabled} label=${t('channels.enabled')} hint=${t('channels.enabled_hint')} />
      <${TestResult} result=${testResult} />
    </div>
  <//>`;
}

// ProviderModal edits the settings shared by all channels of a messenger.
export function ProviderModal({ provider, onClose, onSaved }) {
  const [values, setValues] = useState(provider.values || {});
  const [clear, setClear] = useState([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(null);
  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      const list = await put('/channel-providers/' + provider.type, { enabled: provider.enabled, settings: values, clear });
      toast(t('common.saved'));
      onSaved?.(list);
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  const fe = error?.fields || {};
  return html`<${Modal} title=${t('channels.provider_title', { type: provider.label })} sub=${provider.description}
    icon=${channelIcon(provider.type)} onClose=${onClose} onSubmit=${submit}
    footer=${html`<${Button} onClick=${onClose}>${t('common.cancel')}<//><${Button} kind="primary" type="submit" loading=${busy}>${t('common.save')}<//>`}>
    <div class="stack">
      ${error && !Object.keys(fe).length && html`<${FormError} error=${error} />`}
      <${FieldsForm} fields=${provider.settings || []} values=${values} onChange=${setValues} secretsSet=${provider.secrets_set}
        clear=${clear} onClear=${setClear} errors=${fe} />
    </div>
  <//>`;
}
