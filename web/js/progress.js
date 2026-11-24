// "Preparing your video" dialog shown right after Download is pressed.
// Closing it does not stop anything: the jobs continue in the queue.

import { t } from './i18n.js';
import { h, mount, modal } from './dom.js';
import { post } from './api.js';
import { state, subscribe, deliver } from './state.js';
import { progressBlock, isFinished, isActive } from './jobview.js';

/**
 * Open the progress dialog and run create(), which must resolve with the
 * created jobs. navigate is used for the "Open queue" button.
 */
export function openProgress({ create, navigate, mode }) {
  const list = h('div', { class: 'prog-list' });
  const hint = h('p', { class: 'hint', style: { margin: '0' } });
  const head = h('div', { class: 'prog' },
    h('div', { class: 'prog__top' }, h('span', { class: 'led led--queued' }, t('job.creating'))),
    h('div', { class: 'bar bar--busy' }, h('div', { class: 'bar__fill' })));
  mount(list, head);

  const dlg = modal({ title: t('job.dialogTitle'), body: [list, hint], handle: true });
  const form = dlg.root;
  let unsubscribe = () => {};
  dlg.promise.then(() => unsubscribe());

  const goQueue = { label: t('job.openQueue'), kind: 'ghost', onclick: () => { dlg.close(); navigate('/queue'); } };

  create().then((jobs) => {
    if (!form.isConnected) {
      deliver(jobs);
      return;
    }
    deliver(jobs);
    hint.textContent = mode === 'stream' ? t('job.hintStream') : t('job.hintFile');
    const rows = new Map();
    const single = jobs.length === 1;
    mount(list, jobs.map((j) => {
      const block = progressBlock();
      const row = h('div', null,
        single ? null : h('div', { class: 'small ellipsis', style: { marginBottom: '6px' } }, j.subtitle || j.title),
        block.el);
      rows.set(j.id, block);
      block.update(j);
      return row;
    }));
    if (single) {
      const j = jobs[0];
      list.prepend(h('div', null, h('div', { class: 'job__title ellipsis' }, j.title), h('div', { class: 'job__sub ellipsis' }, j.subtitle ? `${j.subtitle} · ${j.filename}` : j.filename)));
    }

    const refresh = () => {
      const current = jobs.map((j) => state.jobs.get(j.id) || j);
      current.forEach((j) => rows.get(j.id)?.update(j));
      const active = current.filter(isActive);
      const finished = current.filter(isFinished).length;
      if (!single) form.setTitle(t('job.dialogTitleN', { done: finished, count: current.length }));
      else if (current[0].state === 'done') form.setTitle(t('job.dialogDone'));
      const cancel = { label: t('job.cancel'), kind: 'danger', onclick: () => active.forEach((j) => post(`/api/jobs/${j.id}/cancel`).catch(() => {})) };
      form.setActions(active.length
        ? [goQueue, cancel, { label: t('job.hide'), kind: 'sel', value: 'hide' }]
        : [goQueue, { label: t('common.close'), kind: 'sel', value: 'close' }]);
    };
    refresh();
    unsubscribe = subscribe((kind) => { if (kind === 'jobs') refresh(); });
  }).catch((err) => {
    mount(list, h('div', { class: 'form-error' }, err.message));
    form.setTitle(t('job.dialogFailed'));
    form.setActions([{ label: t('common.close'), kind: 'sel', value: 'close' }]);
  });
  return dlg.promise;
}
