// Entry point: routing, navigation and the application shell.

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

const appEl = document.getElementById('app');
const sideEl = document.getElementById('sidebar');
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
  const n = activeCount();
  mount(document.getElementById('nav'), Object.entries(routes)
    .filter(([name, r]) => r.nav && allowed(name))
    .map(([name, r]) => h('a', { href: `#/${name}`, title: t(r.nav), aria: active === name ? { current: 'page' } : null },
      icon(r.icon), h('span', null, t(r.nav)),
      name === 'queue' && n ? h('span', { class: 'badge' }, String(n)) : null)));

  const u = user();
  mount(document.getElementById('account'), h('div', { class: 'account' },
    h('span', { class: 'avatar', aria: { hidden: 'true' } }, u ? u.username.slice(0, 2).toUpperCase() : '··'),
    h('div', { class: 'account__info grow' },
      h('div', { class: 'account__name ellipsis' }, u ? u.username : t('account.guest')),
      h('div', { class: 'account__role' }, u ? (u.is_admin ? t('account.admin') : t('account.member')) : t('account.sharedMode'))),
    u ? h('button', { class: 'btn btn--ghost btn--icon btn--sm', title: t('account.logout'), aria: { label: t('account.logout') }, onclick: logout }, icon('logout')) : null));
  renderActivity();
}

// Small "now recording" widget in the sidebar.
function renderActivity() {
  const box = document.getElementById('activity');
  const jobs = [...state.jobs.values()];
  const running = jobs.filter((j) => j.state === 'running');
  const waiting = jobs.filter((j) => j.state === 'queued' || j.state === 'pending').length;
  const ready = jobs.filter((j) => j.state === 'ready').length;
  if (!running.length && !waiting && !ready) { box.replaceChildren(); return; }
  const j = running[0];
  const pct = j?.progress?.percent || 0;
  mount(box, h('a', { class: 'activity', href: '#/queue' },
    h('div', { class: 'activity__row' },
      h('span', { class: `led led--${j ? 'running' : ready ? 'ready' : 'queued'}` },
        j ? t('activity.running', { count: running.length }) : ready ? t('activity.ready', { count: ready }) : t('activity.waiting', { count: waiting }))),
    j ? h('div', { class: 'ellipsis small' }, j.subtitle || j.title) : null,
    j ? h('div', { class: ['bar', !pct && 'bar--busy'] }, h('div', { class: 'bar__fill', style: { width: `${pct}%` } })) : null));
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
  if (target !== (name || 'grab')) history.replaceState(null, '', `#/${target}`);

  const r = routes[target];
  sideEl.hidden = !!r.public;
  appEl.classList.toggle('is-bare', !!r.public);
  if (!r.public) {
    connectEvents();
    renderNav(target);
  }
  if (cleanup) { try { cleanup(); } catch { /* ignore */ } cleanup = null; }
  current = target;
  const seq = ++renderSeq;
  viewEl.replaceChildren();
  document.title = r.nav ? `${t(r.nav)} · VideoGraber` : 'VideoGraber';
  let c = null;
  try {
    c = (await r.view.render(viewEl, { params, query, navigate, isCurrent: () => seq === renderSeq })) || null;
  } catch (err) {
    console.error(err);
    if (seq === renderSeq) {
      mount(viewEl, h('div', { class: 'state state--error' }, icon('x'), h('h3', null, t('common.error')), h('p', null, err.message)));
    }
  }
  if (seq !== renderSeq) {
    if (c) { try { c(); } catch { /* ignore */ } }
    return;
  }
  cleanup = c;
  if (!viewEl.contains(document.activeElement)) viewEl.focus({ preventScroll: true });
}

async function boot() {
  try {
    await loadStatus();
  } catch (err) {
    mount(viewEl, h('div', { class: 'state state--error' }, icon('x'), h('h3', null, t('common.serverDown')), h('p', null, err.message)));
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
  } else {
    renderActivity();
  }
});

window.addEventListener('hashchange', render);
boot();
