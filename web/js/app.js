// Entry point: routing, navigation and global chrome.

import { t } from './i18n.js';
import { h, icon, mount, toast } from './dom.js';
import { post, setUnauthorizedHandler } from './api.js';
import { state, loadStatus, loadPrefs, connectEvents, disconnectEvents, subscribe, activeCount, isIndividual, user } from './state.js';
import * as grab from './views/grab.js';
import * as queue from './views/queue.js';
import * as library from './views/library.js';
import * as settings from './views/settings.js';
import * as admin from './views/admin.js';
import * as authViews from './views/auth.js';

const routes = {
  grab: { view: grab, nav: 'nav.grab', icon: 'grab' },
  queue: { view: queue, nav: 'nav.queue', icon: 'queue' },
  library: { view: library, nav: 'nav.library', icon: 'library', individual: true },
  settings: { view: settings, nav: 'nav.settings', icon: 'settings' },
  admin: { view: admin, nav: 'nav.admin', icon: 'admin', admin: true },
  login: { view: authViews.login, public: true },
  setup: { view: authViews.setup, public: true },
  register: { view: authViews.register, public: true },
};

const viewEl = document.getElementById('view');
let cleanup = null;
let current = '';
let renderSeq = 0;

export function parseHash() {
  const raw = location.hash.replace(/^#\/?/, '');
  const [pathPart, query = ''] = raw.split('?');
  const [name = '', ...params] = pathPart.split('/').map(decodeURIComponent);
  return { name, params, query: new URLSearchParams(query) };
}

export function navigate(path) {
  if (location.hash === '#' + path) render();
  else location.hash = path;
}

function allowed(name) {
  const r = routes[name];
  if (!r) return false;
  if (r.individual && !isIndividual()) return false;
  if (r.admin && !(isIndividual() && user()?.is_admin)) return false;
  return true;
}

function renderNav(active) {
  const nav = document.getElementById('nav');
  const links = Object.entries(routes)
    .filter(([name, r]) => r.nav && allowed(name))
    .map(([name, r]) => {
      const badge = name === 'queue' && activeCount() ? h('span', { class: 'badge' }, String(activeCount())) : null;
      return h('a', { href: `#/${name}`, aria: active === name ? { current: 'page' } : null }, icon(r.icon), h('span', null, t(r.nav)), badge);
    });
  mount(nav, links);

  const acc = document.getElementById('account');
  const u = user();
  if (u) {
    mount(acc, h('div', { class: 'account' },
      h('span', null, t('account.signedInAs')), h('span', { class: 'account__name' }, u.username),
      h('button', { class: 'btn btn--ghost btn--icon', title: t('account.logout'), aria: { label: t('account.logout') }, onclick: logout }, icon('logout'))));
  } else {
    mount(acc, isIndividual() ? null : h('span', { class: 'eyebrow' }, t('account.sharedMode')));
  }
}

async function logout() {
  try { await post('/api/logout'); } catch { /* ignore */ }
  disconnectEvents();
  await loadStatus();
  navigate('/login');
}

async function render() {
  const { name, params, query } = parseHash();
  const st = state.status;
  let target = name || 'grab';

  if (st.setup_required) target = 'setup';
  else if (isIndividual() && !user()) {
    if (!['login', 'register'].includes(target)) target = 'login';
  } else if (routes[target]?.public || !allowed(target)) {
    target = 'grab';
  }
  if (target !== (name || 'grab')) {
    history.replaceState(null, '', `#/${target}`);
  }

  const r = routes[target];
  document.getElementById('topbar').hidden = !!r.public;
  if (!r.public) {
    connectEvents();
    renderNav(target);
  }
  if (cleanup) { try { cleanup(); } catch { /* ignore */ } cleanup = null; }
  current = target;
  const seq = ++renderSeq;
  // Async views render into their own container, attached only if they are
  // still the current route when they finish.
  const container = h('div');
  viewEl.replaceChildren(container);
  document.title = r.nav ? `${t(r.nav)} · VideoGraber` : 'VideoGraber';
  let c = null;
  try {
    c = (await r.view.render(container, { params, query, navigate, refresh: boot })) || null;
  } catch (err) {
    console.error(err);
    mount(container, h('div', { class: 'error-card' }, h('h3', null, t('common.error')), h('p', null, err.message)));
  }
  if (seq !== renderSeq) {
    if (c) { try { c(); } catch { /* ignore */ } }
    return;
  }
  cleanup = c;
  if (!viewEl.contains(document.activeElement)) viewEl.focus({ preventScroll: true });
}

// Running SMPTE-style clock in the header — purely decorative.
function startTimecode() {
  const el = document.getElementById('timecode');
  const p = (x) => String(x).padStart(2, '0');
  const tick = () => {
    const d = new Date();
    el.textContent = `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}:${p(Math.floor(d.getMilliseconds() / 40))}`;
  };
  tick();
  setInterval(tick, 40);
}

async function boot() {
  try {
    await loadStatus();
  } catch (err) {
    mount(viewEl, h('div', { class: 'error-card' }, h('h3', null, t('common.serverDown')), h('p', null, err.message)));
    return;
  }
  await loadPrefs();
  await render();
}

setUnauthorizedHandler(async () => {
  if (!isIndividual()) return;
  await loadStatus();
  if (!user() && current !== 'login') {
    toast(t('account.sessionExpired'), 'error');
    disconnectEvents();
    navigate('/login');
  }
});

let lastActive = -1;
subscribe((kind) => {
  if (kind !== 'jobs' || routes[current]?.public) return;
  const n = activeCount();
  if (n !== lastActive) {
    lastActive = n;
    renderNav(current);
  }
});

window.addEventListener('hashchange', render);
startTimecode();
boot();
