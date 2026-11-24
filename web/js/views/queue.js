// Queue view: live job list. Rows are long-lived components; progress
// events only touch text and bar widths, actions are rebuilt only when the
// job state changes.

import { t } from '../i18n.js';
import { h, icon, mount, thumb, toast, download, confirmDialog, segmented } from '../dom.js';
import { post, del } from '../api.js';
import { state, subscribe } from '../state.js';
import { progressBlock, isFinished, isActive } from '../jobview.js';

let filter = 'all';

export async function render(root) {
  const list = h('div', { class: 'jobs' });
  const counts = h('span', { class: 'sub' });
  const clearBtn = h('button', { class: 'btn btn--ghost btn--sm', onclick: clearFinished }, icon('trash'), t('queue.clearFinished'));
  const filters = segmented([
    { value: 'all', label: t('queue.all') },
    { value: 'active', label: t('queue.active') },
    { value: 'finished', label: t('queue.finished') },
  ], filter, (v) => { filter = v; draw(); }, { label: t('queue.filter') });
  filters.style.width = 'auto';

  mount(root,
    h('div', { class: 'vhead' }, h('h1', null, t('queue.title')), counts, h('span', { class: 'grow' }), filters, clearBtn),
    h('div', { class: 'vbody' }, list));

  const rows = new Map(); // id -> row component

  function draw() {
    const all = [...state.jobs.values()].sort((a, b) => new Date(b.created_at) - new Date(a.created_at));
    const active = all.filter((j) => !isFinished(j)).length;
    counts.textContent = all.length ? t('queue.counts', { active, total: all.length }) : '';
    clearBtn.hidden = !all.some(isFinished);
    const jobs = all.filter((j) => filter === 'all' || (filter === 'active' ? !isFinished(j) : isFinished(j)));

    if (!jobs.length) {
      rows.clear();
      mount(list, h('div', { class: 'card', style: { flex: '1', minHeight: '260px' } }, h('div', { class: 'state' }, icon('queue'),
        h('h3', null, all.length ? t('queue.noneInFilter') : t('queue.emptyTitle')),
        h('p', null, t('queue.emptyText')),
        all.length ? null : h('a', { class: 'btn btn--sel', href: '#/grab' }, t('queue.goGrab')))));
      list.style.display = 'flex';
      return;
    }
    list.style.display = '';
    if (list.querySelector('.state')) list.replaceChildren();
    const seen = new Set();
    jobs.forEach((j, i) => {
      seen.add(j.id);
      let row = rows.get(j.id);
      if (!row) { row = jobRow(j); rows.set(j.id, row); }
      row.update(j);
      if (list.children[i] !== row.el) list.insertBefore(row.el, list.children[i] || null);
    });
    for (const [id, row] of rows) if (!seen.has(id)) { row.el.remove(); rows.delete(id); }
  }

  draw();
  return subscribe((kind) => { if (kind === 'jobs') draw(); });
}

async function clearFinished() {
  const done = [...state.jobs.values()].filter(isFinished);
  await Promise.allSettled(done.map((j) => del(`/api/jobs/${j.id}`)));
}

function jobRow(initial) {
  const prog = progressBlock();
  const title = h('div', { class: 'job__title ellipsis' });
  const sub = h('div', { class: 'job__sub ellipsis' });
  const hint = h('div', { class: 'hint', hidden: true }, t('queue.pendingHint'));
  const actions = h('div', { class: 'job__actions' });
  const el = h('article', { class: 'job' },
    h('div', { class: 'job__thumb' }, thumb(initial.thumbnail)),
    h('div', { style: { minWidth: '0' } }, title, sub, hint),
    h('div', { class: 'job__prog' }, prog.el),
    actions);
  let lastState = null;
  let lastJob = null;

  const buildActions = (j) => {
    const list = [];
    if (j.state === 'ready') {
      list.push(h('button', { class: 'btn btn--sel btn--sm', onclick: () => download(`/api/jobs/${j.id}/download`) }, icon('grab'), t('queue.save')));
    }
    if (j.mode === 'stream' && ['pending', 'failed', 'canceled', 'done'].includes(j.state)) {
      list.push(h('button', { class: 'btn btn--sm', title: t('queue.restartHint'), onclick: () => download(`/api/jobs/${j.id}/download`) },
        icon(j.state === 'pending' ? 'play' : 'refresh'), j.state === 'pending' ? t('queue.start') : t('queue.again')));
    }
    if (isActive(j) && j.state !== 'pending') {
      list.push(h('button', { class: 'btn btn--sm btn--danger', onclick: () => post(`/api/jobs/${j.id}/cancel`).catch((e) => toast(e.message, 'error')) }, icon('stop'), t('queue.cancel')));
    }
    list.push(h('button', {
      class: 'btn btn--ghost btn--icon btn--sm', title: t('queue.remove'), aria: { label: t('queue.remove') },
      onclick: async () => {
        const cur = state.jobs.get(j.id) || j;
        if (cur.state === 'running' && !(await confirmDialog(t('queue.removeActive')))) return;
        del(`/api/jobs/${j.id}`).catch((e) => toast(e.message, 'error'));
      },
    }, icon('x')));
    mount(actions, list);
  };

  return {
    el,
    update(j) {
      if (j === lastJob) return;
      lastJob = j;
      title.textContent = j.title || j.filename;
      title.title = j.title || '';
      sub.textContent = j.subtitle ? `${j.subtitle} · ${j.filename}` : j.filename;
      sub.title = j.filename;
      hint.hidden = j.state !== 'pending';
      prog.update(j);
      if (j.state !== lastState) {
        lastState = j.state;
        buildActions(j);
      }
    },
  };
}
