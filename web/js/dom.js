// Small DOM toolkit. Everything is built with createElement/textContent so
// that data coming from scraped pages can never inject markup.

import { t } from './i18n.js';

const SVG_NS = 'http://www.w3.org/2000/svg';

/**
 * Create an element.
 * h('button', {class: 'btn', onclick: fn, aria: {pressed: true}}, 'Text', child)
 */
export function h(tag, props = null, ...children) {
  const el = document.createElement(tag);
  if (props) {
    for (const [k, v] of Object.entries(props)) {
      if (v === null || v === undefined || v === false) continue;
      if (k === 'class') el.className = Array.isArray(v) ? v.filter(Boolean).join(' ') : v;
      else if (k === 'dataset') Object.assign(el.dataset, v);
      else if (k === 'style') Object.assign(el.style, v);
      else if (k === 'aria') for (const [a, av] of Object.entries(v)) el.setAttribute('aria-' + a, String(av));
      else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
      else if (k === 'value' || k === 'checked' || k === 'selected' || k === 'disabled' || k === 'hidden') el[k] = v;
      else el.setAttribute(k, v === true ? '' : String(v));
    }
  }
  append(el, children);
  return el;
}

function append(el, children) {
  for (const c of children.flat(Infinity)) {
    if (c === null || c === undefined || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
}

export function clear(el) {
  while (el.firstChild) el.firstChild.remove();
  return el;
}

export function mount(el, ...children) {
  clear(el);
  append(el, children);
  return el;
}

// ── Icons (static path data only) ──────────────────────────────────────────
const ICONS = {
  grab: 'M12 3v12m0 0l-5-5m5 5l5-5M4 17v2a2 2 0 002 2h12a2 2 0 002-2v-2',
  queue: 'M4 6h16M4 12h10M4 18h7M17 15l4 3-4 3z',
  library: 'M4 4h6v6H4zM14 4h6v6h-6zM4 14h6v6H4zM14 14h6v6h-6z',
  settings: 'M12 15a3 3 0 100-6 3 3 0 000 6zM19.4 15a1.7 1.7 0 00.3 1.8l.1.1a2 2 0 11-2.8 2.8l-.1-.1a1.7 1.7 0 00-1.8-.3 1.7 1.7 0 00-1 1.5V21a2 2 0 11-4 0v-.1a1.7 1.7 0 00-1.1-1.5 1.7 1.7 0 00-1.8.3l-.1.1a2 2 0 11-2.8-2.8l.1-.1a1.7 1.7 0 00.3-1.8 1.7 1.7 0 00-1.5-1H3a2 2 0 110-4h.1a1.7 1.7 0 001.5-1.1 1.7 1.7 0 00-.3-1.8l-.1-.1a2 2 0 112.8-2.8l.1.1a1.7 1.7 0 001.8.3H9a1.7 1.7 0 001-1.5V3a2 2 0 114 0v.1a1.7 1.7 0 001 1.5 1.7 1.7 0 001.8-.3l.1-.1a2 2 0 112.8 2.8l-.1.1a1.7 1.7 0 00-.3 1.8V9a1.7 1.7 0 001.5 1H21a2 2 0 110 4h-.1a1.7 1.7 0 00-1.5 1z',
  admin: 'M12 3l8 3v6c0 4.5-3.4 8.3-8 9-4.6-.7-8-4.5-8-9V6zM9 12l2 2 4-4',
  logout: 'M15 4h3a2 2 0 012 2v12a2 2 0 01-2 2h-3M10 17l5-5-5-5M15 12H4',
  play: 'M7 4l13 8-13 8z',
  x: 'M6 6l12 12M18 6L6 18',
  trash: 'M4 7h16M10 11v6M14 11v6M5 7l1 13h12l1-13M9 7V4h6v3',
  refresh: 'M20 11a8 8 0 10-2.3 5.7M20 4v7h-7',
  link: 'M10 14a4 4 0 005.7 0l3-3a4 4 0 00-5.7-5.7l-1 1M14 10a4 4 0 00-5.7 0l-3 3a4 4 0 005.7 5.7l1-1',
  upload: 'M12 16V4m0 0l-5 5m5-5l5 5M4 17v2a2 2 0 002 2h12a2 2 0 002-2v-2',
  film: 'M4 4h16v16H4zM8 4v16M16 4v16M4 8h4M4 12h4M4 16h4M16 8h4M16 12h4M16 16h4',
  music: 'M9 18V5l12-2v13M9 18a3 3 0 11-6 0 3 3 0 016 0zM21 16a3 3 0 11-6 0 3 3 0 016 0z',
  search: 'M11 18a7 7 0 100-14 7 7 0 000 14zM21 21l-5-5',
  plus: 'M12 5v14M5 12h14',
  check: 'M5 12l5 5L20 7',
  edit: 'M4 20h4L19 9l-4-4L4 16zM14 6l4 4',
  copy: 'M9 9h11v11H9zM5 15H4V4h11v1',
  stop: 'M6 6h12v12H6z',
  bolt: 'M13 2L4 14h7l-1 8 9-12h-7z',
  file: 'M14 3H6v18h12V7zM14 3v4h4',
};

export function icon(name, cls = '') {
  const svg = document.createElementNS(SVG_NS, 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('fill', 'none');
  svg.setAttribute('stroke', 'currentColor');
  svg.setAttribute('stroke-width', '1.8');
  svg.setAttribute('stroke-linecap', 'round');
  svg.setAttribute('stroke-linejoin', 'round');
  svg.setAttribute('aria-hidden', 'true');
  if (cls) svg.setAttribute('class', cls);
  const path = document.createElementNS(SVG_NS, 'path');
  path.setAttribute('d', ICONS[name] || '');
  svg.append(path);
  return svg;
}

/** An <img> that degrades gracefully and never leaks the page referrer. */
export function thumb(src, alt = '') {
  if (!src) return icon('film');
  const img = h('img', { src, alt, loading: 'lazy', referrerpolicy: 'no-referrer', decoding: 'async' });
  img.addEventListener('error', () => img.replaceWith(icon('film')), { once: true });
  return img;
}

/**
 * Run fn (which re-renders part of the DOM) while preserving the scroll
 * position of el and of its scrollable descendants marked with data-keep.
 */
export function keepScroll(el, fn) {
  const saved = [[el, el.scrollTop]];
  el.querySelectorAll('[data-keep]').forEach((x) => saved.push([x.dataset.keep, x.scrollTop]));
  fn();
  for (const [k, top] of saved) {
    const target = typeof k === 'string' ? el.querySelector(`[data-keep="${k}"]`) : k;
    if (target) target.scrollTop = top;
  }
}

// ── Toasts ─────────────────────────────────────────────────────────────────
export function toast(message, kind = 'info', ms = 4200) {
  const box = document.getElementById('toasts');
  const el = h('div', { class: ['toast', `toast--${kind}`] }, message);
  box.append(el);
  setTimeout(() => {
    el.classList.add('is-leaving');
    el.addEventListener('animationend', () => el.remove(), { once: true });
  }, ms);
}

// ── Modal ──────────────────────────────────────────────────────────────────
/**
 * Open a modal dialog. `actions` is a list of {label, kind, value, submit}.
 * Resolves with the value of the clicked action (or null when dismissed).
 * `onSubmit(value)` may return false to keep the dialog open.
 * Returns {promise, close, root} when `handle` is true.
 */
export function modal({ title, body, actions = [], onSubmit, wide = false, handle = false }) {
  let close;
  const promise = new Promise((resolve) => {
    const prev = document.activeElement;
    close = (v) => {
      if (!backdrop.isConnected) return;
      backdrop.remove();
      document.removeEventListener('keydown', onKey);
      prev?.focus?.();
      resolve(v);
    };
    const onKey = (e) => { if (e.key === 'Escape') close(null); };
    const foot = h('div', { class: 'card__foot' });
    const renderActions = (list) => mount(foot, list.map((a) => h('button', {
      type: a.submit ? 'submit' : 'button', class: ['btn', a.kind && `btn--${a.kind}`],
      onclick: a.submit ? null : () => (a.onclick ? a.onclick() : close(a.value ?? null)),
    }, a.icon ? icon(a.icon) : null, a.label)));
    renderActions(actions);
    const form = h('form', { class: ['card', 'modal', wide && 'modal--wide'], role: 'dialog', aria: { modal: 'true', label: title } },
      h('div', { class: 'card__head' }, h('h2', { class: 'card__title grow ellipsis' }, title),
        h('button', { type: 'button', class: 'btn btn--ghost btn--icon btn--sm', aria: { label: t('common.close') }, onclick: () => close(null) }, icon('x'))),
      h('div', { class: 'card__body' }, body),
      foot);
    foot.hidden = !actions.length;
    form.setActions = (list) => { renderActions(list); foot.hidden = !list.length; };
    form.setTitle = (text) => { form.querySelector('.card__title').textContent = text; };
    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      const submitAction = actions.find((a) => a.submit);
      const v = submitAction?.value ?? true;
      if (onSubmit) {
        const btns = form.querySelectorAll('button');
        btns.forEach((b) => (b.disabled = true));
        try {
          if ((await onSubmit(v)) === false) return;
        } finally {
          btns.forEach((b) => (b.disabled = false));
        }
      }
      close(v);
    });
    const backdrop = h('div', { class: 'modal-backdrop', onmousedown: (e) => { if (e.target === backdrop) close(null); } }, form);
    document.body.append(backdrop);
    document.addEventListener('keydown', onKey);
    (form.querySelector('.card__body input, .card__body select, .card__body textarea') || form.querySelector('[type=submit]') || form.querySelector('.card__foot button'))?.focus();
    close.root = form;
  });
  return handle ? { promise, close, root: close.root } : promise;
}

export async function confirmDialog(text, { okLabel, danger = true } = {}) {
  const v = await modal({
    title: t('common.confirm'),
    body: h('p', null, text),
    actions: [
      { label: t('common.cancel'), kind: 'ghost', value: false },
      { label: okLabel || t('common.ok'), kind: danger ? 'primary' : 'sel', value: true, submit: true },
    ],
  });
  return v === true;
}

// ── Formatting ─────────────────────────────────────────────────────────────
export const fmt = {
  bytes(n) {
    if (!n) return '—';
    const u = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return `${n.toFixed(n >= 100 || i === 0 ? 0 : 1)} ${u[i]}`;
  },
  duration(sec) {
    if (!sec || sec < 0) return '--:--';
    sec = Math.round(sec);
    const hh = Math.floor(sec / 3600), mm = Math.floor((sec % 3600) / 60), ss = sec % 60;
    const p = (x) => String(x).padStart(2, '0');
    return hh ? `${hh}:${p(mm)}:${p(ss)}` : `${p(mm)}:${p(ss)}`;
  },
  bitrate(bps) {
    if (!bps) return '';
    return bps >= 1e6 ? `${(bps / 1e6).toFixed(1)} Mbps` : `${Math.round(bps / 1000)} kbps`;
  },
  date(s) {
    if (!s) return '';
    return new Date(s).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
  },
};

/** Trigger a browser download of url without leaving the page. */
export function download(url) {
  const a = h('a', { href: url, download: '', hidden: true });
  document.body.append(a);
  a.click();
  setTimeout(() => a.remove(), 1000);
}

export function copyText(text) {
  if (navigator.clipboard?.writeText) return navigator.clipboard.writeText(text).then(() => true, () => fallbackCopy(text));
  return Promise.resolve(fallbackCopy(text));
}

function fallbackCopy(text) {
  const ta = h('textarea', { style: { position: 'fixed', opacity: '0' } });
  ta.value = text;
  document.body.append(ta);
  ta.select();
  let ok = false;
  try { ok = document.execCommand('copy'); } catch { ok = false; }
  ta.remove();
  return ok;
}

/** A segmented control. options: [{value, label, icon}] */
export function segmented(options, value, onChange, { block = false, label } = {}) {
  const root = h('div', { class: ['seg', block && 'seg--block'], role: 'group', aria: label ? { label } : null });
  const render = (v) => {
    for (const b of root.children) b.setAttribute('aria-pressed', String(b.dataset.value === String(v)));
  };
  for (const o of options) {
    root.append(h('button', {
      type: 'button', dataset: { value: String(o.value) },
      onclick: () => { render(o.value); onChange(o.value); },
    }, o.icon ? icon(o.icon) : null, o.label));
  }
  render(value);
  return root;
}

export function selectEl(options, value, onChange, attrs = {}) {
  const s = h('select', { class: 'select', ...attrs, onchange: (e) => onChange(e.target.value) },
    options.map((o) => h('option', { value: String(o.value), selected: String(o.value) === String(value) }, o.label)));
  return s;
}

export function field(label, control, hint) {
  return h('label', { class: 'field' }, h('span', { class: 'label' }, label), control, hint ? h('p', { class: 'hint' }, hint) : null);
}
