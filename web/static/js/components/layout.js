// Application shell: sidebar navigation, mobile bar, user footer.
import { html, useEffect, useRef, useState } from '../../vendor/preact-htm.js';
import { Icon, BrandMark } from './icons.js';
import { Avatar, Button, Popover } from './ui.js';
import { Link, navigate, useLocation } from '../lib/router.js';
import { app, useStore, setPref } from '../lib/store.js';
import { t, setLang, lang, languages } from '../lib/i18n.js';
import { post } from '../lib/api.js';

const NAV = [
  { href: '/', icon: 'dashboard', key: 'nav.dashboard', exact: true },
  { href: '/certificates', icon: 'shieldCheck', key: 'nav.certificates' },
  { href: '/groups', icon: 'layers', key: 'nav.groups' },
  { href: '/recipients', icon: 'mail', key: 'nav.recipients' },
  { href: '/notifications', icon: 'bell', key: 'nav.notifications' },
  { href: '/mail-log', icon: 'send', key: 'nav.mail_log' },
];
const ADMIN_NAV = [
  { href: '/users', icon: 'users', key: 'nav.users' },
  { href: '/settings', icon: 'sliders', key: 'nav.settings' },
  { href: '/audit', icon: 'list', key: 'nav.audit' },
];

export function setTheme(theme) {
  document.documentElement.dataset.theme = theme;
  setPref('theme', theme);
  app.set({ theme });
}

export async function logout() {
  try { await post('/auth/logout'); } catch { /* ignore */ }
  app.set({ me: null });
  navigate('/login', { replace: true });
}

export function Brand({ sub = true }) {
  const { me } = app.get();
  return html`<${Link} href="/" class="brand">
    <${BrandMark} />
    <span><div class="brand-name">${me?.app?.name || 'Cert Alerter'}</div>${sub && html`<div class="brand-sub">${t('brand.tagline')}</div>`}</span>
  <//>`;
}

function NavLinks({ items, path, onNavigate }) {
  return items.map((it) => {
    const active = it.exact ? path === it.href : path === it.href || path.startsWith(it.href + '/');
    return html`<${Link} href=${it.href} class=${active ? 'active' : ''} aria-current=${active ? 'page' : undefined} onClick=${onNavigate}>
      <${Icon} name=${it.icon} />${t(it.key)}
    <//>`;
  });
}

export function Shell({ children }) {
  const state = useStore(app);
  const { path } = useLocation();
  const [open, setOpen] = useState(false);
  const me = state.me;
  const isAdmin = me?.user?.role === 'admin';
  useEffect(() => setOpen(false), [path]);
  const current = [...NAV, ...ADMIN_NAV].find((it) => (it.exact ? path === it.href : path.startsWith(it.href)));

  return html`<div class="app">
    <aside class=${'sidebar' + (open ? ' open' : '')} aria-label=${t('nav.main')}>
      <${Brand} />
      <nav class="nav">
        <${NavLinks} items=${NAV} path=${path} />
        ${isAdmin && html`<div class="nav-label">${t('nav.admin')}</div><${NavLinks} items=${ADMIN_NAV} path=${path} />`}
      </nav>
      <div class="sidebar-foot">
        <${Link} href="/profile" class="user-card">
          <${Avatar} name=${me?.user?.name} email=${me?.user?.email} />
          <span class="grow">
            <div class="name truncate">${me?.user?.name || me?.user?.email}</div>
            <div class="role">${t('role.' + me?.user?.role)}</div>
          </span>
        <//>
        <div class="foot-actions">
          <${Button} size="sm" kind="ghost" icon=${state.theme === 'dark' ? 'sun' : 'moon'}
            title=${t('nav.theme')} aria-label=${t('nav.theme')} onClick=${() => setTheme(state.theme === 'dark' ? 'light' : 'dark')} />
          <${LanguageSwitch} />
          <${Button} size="sm" kind="ghost" icon="logout" title=${t('nav.logout')} aria-label=${t('nav.logout')} onClick=${logout} />
        </div>
        ${me?.app?.version && html`<div class="app-version" title=${t('common.version')}>${me.app.name || 'Cert Alerter'} ${me.app.version}</div>`}
      </div>
    </aside>
    ${open && html`<div class="scrim" onClick=${() => setOpen(false)}></div>`}
    <div style=${{ minWidth: 0 }}>
      <div class="mobile-bar">
        <${Button} kind="ghost" size="sm" icon="menu" aria-label=${t('nav.menu')} onClick=${() => setOpen(true)} />
        <${BrandMark} class="brand-mark" />
        <b class="truncate">${current ? t(current.key) : me?.app?.name}</b>
      </div>
      <main class="main" id="main">${children}</main>
    </div>
  </div>`;
}

export function PageHead({ title, sub, crumbs, actions, children }) {
  return html`<header class="page-head reveal">
    <div class="grow">
      ${crumbs && html`<nav class="crumbs" aria-label=${t('nav.breadcrumbs')}>
        ${crumbs.map((c, i) => html`${i > 0 && html`<${Icon} name="chevronRight" size="12" />`}${c.href ? html`<${Link} href=${c.href}>${c.label}<//>` : html`<span>${c.label}</span>`}`)}
      </nav>`}
      <h1>${title}</h1>
      ${sub && html`<div class="sub">${sub}</div>`}
      ${children}
    </div>
    ${actions && html`<div class="actions">${actions}</div>`}
  </header>`;
}

// LanguageSwitch lists the interface languages. compact shows the language
// code (sidebar), otherwise the globe icon and the language name.
export function LanguageSwitch({ compact = true, size = 'sm' }) {
  const [open, setOpen] = useState(false);
  const anchor = useRef(null);
  const current = languages.find((l) => l.code === lang()) || languages[0];
  return html`<div class="menu-wrap" ref=${anchor}>
    <${Button} size=${size} kind="ghost" icon=${compact ? undefined : 'globe'} title=${t('nav.language')} aria-label=${t('nav.language')}
      aria-haspopup="menu" aria-expanded=${open} onClick=${() => setOpen(!open)}>
      ${compact ? html`<span class="mono" style=${{ fontSize: '11.5px', fontWeight: 700 }}>${current.code.toUpperCase()}</span>` : current.label}
    <//>
    ${open && html`<${Popover} anchor=${anchor} align="end" onClose=${() => setOpen(false)}>
      <div class="menu" role="menu">
        ${languages.map((l) => html`<button type="button" role="menuitemradio" aria-checked=${l.code === current.code}
          onClick=${() => { setOpen(false); setLang(l.code); }}>
          <${Icon} name=${l.code === current.code ? 'check' : 'globe'} />${l.label}
        </button>`)}
      </div>
    <//>`}
  </div>`;
}
