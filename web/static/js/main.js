// Application entry point.
import { html, render, useEffect } from '../vendor/preact-htm.js';
import { app, on, pref, useStore } from './lib/store.js';
import { get } from './lib/api.js';
import { match, navigate, useLocation } from './lib/router.js';
import { detectLang, setLang, t } from './lib/i18n.js';
import { Shell } from './components/layout.js';
import { ConfirmHost, PageLoader, Toasts } from './components/ui.js';
import { ForcePasswordChange, Forgot, Login, Setup, TokenPage } from './pages/auth.js';
import { Dashboard } from './pages/dashboard.js';
import { Certificates } from './pages/certificates.js';
import { CertificateDetail } from './pages/certificate.js';
import { GroupDetail, Groups } from './pages/groups.js';
import { Recipients } from './pages/recipients.js';
import { Notifications } from './pages/notifications.js';
import { Users } from './pages/users.js';
import { Settings } from './pages/settings.js';
import { Audit, NotFound, Profile } from './pages/misc.js';
import { MailLog } from './pages/maillog.js';

const PUBLIC = [
  ['/login', Login],
  ['/forgot', Forgot],
  ['/invite/:token', TokenPage],
  ['/reset/:token', TokenPage],
  ['/setup', Setup],
];

const ROUTES = [
  ['/', Dashboard],
  ['/certificates', Certificates],
  ['/certificates/:id', CertificateDetail],
  ['/groups', Groups],
  ['/groups/:id', GroupDetail],
  ['/recipients', Recipients],
  ['/notifications', Notifications],
  ['/mail-log', MailLog],
  ['/profile', Profile],
  ['/users', Users, true],
  ['/settings', Settings, true],
  ['/settings/:tab', Settings, true],
  ['/audit', Audit, true],
];

function resolve(table, path) {
  for (const [pattern, component, admin] of table) {
    const params = match(pattern, path);
    if (params) return { component, params, admin };
  }
  return null;
}

async function boot() {
  try {
    const me = await get('/me');
    app.set({ me, ready: true });
  } catch (e) {
    if (e.status === 401) {
      app.set({ me: null, ready: true });
      const path = location.pathname;
      if (!resolve(PUBLIC, path)) {
        try {
          const s = await get('/setup');
          if (s.setup_required) {
            navigate('/setup' + location.search, { replace: true });
            return;
          }
        } catch { /* ignore */ }
        navigate('/login' + (path !== '/' ? '?next=' + encodeURIComponent(path + location.search) : ''), { replace: true });
      }
    } else {
      app.set({ ready: true, bootError: e });
    }
  }
}

function App() {
  const state = useStore(app);
  const { path } = useLocation();

  useEffect(() => {
    boot();
    const offUnauthorized = on('unauthorized', () => {
      if (!app.get().me) return;
      app.set({ me: null });
      navigate('/login?next=' + encodeURIComponent(location.pathname + location.search), { replace: true });
    });
    const offPw = on('password-change-required', async () => {
      try { app.set({ me: await get('/me') }); } catch { /* ignore */ }
    });
    return () => { offUnauthorized(); offPw(); };
  }, []);

  useEffect(() => {
    if (state.ready && !state.me && !resolve(PUBLIC, path)) {
      navigate('/login' + (path !== '/' ? '?next=' + encodeURIComponent(path) : ''), { replace: true });
    }
    if (state.ready && state.me && (path === '/login' || path === '/setup')) navigate('/', { replace: true });
  }, [state.ready, state.me, path]);

  if (!state.ready) return html`<${PageLoader} />`;
  if (state.bootError) {
    return html`<div class="center"><div class="card" style=${{ padding: '28px', maxWidth: '420px', textAlign: 'center' }}>
      <h2>${t('common.load_failed')}</h2><p class="muted" style=${{ margin: '10px 0 16px' }}>${state.bootError.message}</p>
      <button class="btn" onClick=${() => location.reload()}>${t('common.retry')}</button></div></div>`;
  }

  const pub = resolve(PUBLIC, path);
  if (pub && !(state.me && (path === '/login' || path === '/setup'))) {
    return html`<${pub.component} params=${pub.params} key=${path} />`;
  }
  if (!state.me) return html`<${PageLoader} />`;
  if (state.me.must_change_password) return html`<${ForcePasswordChange} />`;

  const route = resolve(ROUTES, path);
  let content;
  if (!route) content = html`<${NotFound} />`;
  else if (route.admin && state.me.user.role !== 'admin') content = html`<${NotFound} forbidden />`;
  else content = html`<${route.component} params=${route.params} key=${path} />`;
  return html`<${Shell}>${content}<//>`;
}

function Root() {
  useStore(app); // re-render everything on language/theme change
  return html`<${App} /><${Toasts} /><${ConfirmHost} />`;
}

setLang(detectLang());
app.set({ theme: document.documentElement.dataset.theme || pref('theme', 'light') });
render(html`<${Root} />`, document.getElementById('app'));
