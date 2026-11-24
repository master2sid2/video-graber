// Library view (individual mode): download history and categories.

import { t } from '../i18n.js';
import { h, icon, mount, thumb, fmt, toast, modal, confirmDialog, selectEl, field } from '../dom.js';
import { get, post, patch, del } from '../api.js';

const PAGE = 48;
const COLORS = ['#ff4a26', '#ffb43b', '#a6f05a', '#6fd6ff', '#c792ff', '#ff7ab6', '#efe9dc'];

export async function render(root, { navigate }) {
  const st = { categories: [], items: [], total: 0, category: 0, q: '' };
  const chips = h('div', { class: 'chips' });
  const sheet = h('div', { class: 'sheet' });
  const more = h('div', { class: 'row', style: { justifyContent: 'center', marginTop: '22px' } });
  let searchTimer = null;
  const search = h('input', {
    class: 'input', type: 'search', placeholder: t('library.search'), aria: { label: t('library.search') },
    oninput: () => { clearTimeout(searchTimer); searchTimer = setTimeout(() => { st.q = search.value.trim(); load(true); }, 250); },
  });

  mount(root,
    h('div', { class: 'page-head' },
      h('h1', { class: 'page-title' }, h('small', null, t('library.eyebrow')), t('library.title')),
      h('div', { class: 'row' },
        h('button', { class: 'btn', onclick: () => editCategory(null) }, icon('plus'), t('library.newCategory')),
        h('button', { class: 'btn btn--ghost btn--danger', onclick: clearAll }, icon('trash'), t('library.clear')))),
    h('div', { class: 'toolbar' }, h('div', { class: 'search' }, icon('search'), search)),
    h('div', { class: 'toolbar' }, chips),
    sheet, more);

  async function loadCategories() {
    st.categories = (await get('/api/categories')) || [];
    drawChips();
  }

  function drawChips() {
    const all = h('button', { class: 'chip', aria: { pressed: String(st.category === 0) }, onclick: () => { st.category = 0; drawChips(); load(true); } }, t('library.all'));
    const none = h('button', { class: 'chip', aria: { pressed: String(st.category === -1) }, onclick: () => { st.category = -1; drawChips(); load(true); } }, t('library.uncategorized'));
    const cats = st.categories.map((c) => h('button', {
      class: 'chip', aria: { pressed: String(st.category === c.id) }, title: t('library.editHint'),
      onclick: () => { st.category = c.id; drawChips(); load(true); },
      ondblclick: () => editCategory(c),
    }, h('span', { class: 'chip__dot', style: { background: c.color || 'var(--dust)' } }), c.name, h('span', { class: 'chip__count' }, String(c.count))));
    const edit = st.category > 0
      ? h('button', { class: 'btn btn--ghost btn--sm', onclick: () => editCategory(st.categories.find((c) => c.id === st.category)) }, icon('edit'), t('library.editCategory'))
      : null;
    mount(chips, all, none, cats, edit);
  }

  let loadSeq = 0;
  async function load(reset) {
    const seq = ++loadSeq;
    const offset = reset ? 0 : st.items.length;
    const params = new URLSearchParams({ limit: PAGE, offset });
    if (st.category) params.set('category', st.category);
    if (st.q) params.set('q', st.q);
    try {
      const res = await get(`/api/history?${params}`);
      if (seq !== loadSeq) return; // a newer request superseded this one
      st.items = reset ? res.items : [...st.items, ...res.items];
      st.total = res.total;
    } catch (err) {
      toast(err.message, 'error');
    }
    drawItems();
  }

  function drawItems() {
    if (!st.items.length) {
      mount(sheet, h('div', { class: 'empty', style: { gridColumn: '1 / -1' } }, icon('library'),
        h('h3', null, st.q || st.category ? t('library.noMatches') : t('library.emptyTitle')),
        h('p', null, t('library.emptyText'))));
    } else {
      mount(sheet, st.items.map(card));
    }
    mount(more, st.items.length < st.total
      ? h('button', { class: 'btn', onclick: () => load(false) }, t('library.more', { count: st.total - st.items.length }))
      : null);
  }

  function card(item) {
    let opts = {};
    try { opts = JSON.parse(item.options || '{}'); } catch { /* ignore */ }
    const out = opts.output || {};
    const catSel = selectEl(
      [{ value: '', label: t('library.uncategorized') }, ...st.categories.map((c) => ({ value: c.id, label: c.name }))],
      item.category_id ?? '',
      async (v) => {
        try {
          await patch(`/api/history/${item.id}`, { category_id: v ? Number(v) : null });
          item.category_id = v ? Number(v) : null;
          loadCategories();
        } catch (err) { toast(err.message, 'error'); }
      }, { aria: { label: t('library.category') } });
    return h('article', { class: 'card' },
      h('div', { class: 'card__thumb' }, thumb(item.thumbnail, item.title), h('span', { class: `led led--${item.status}` }, t(`state.${item.status}`))),
      h('div', { class: 'card__body' },
        h('h3', { class: 'card__title', title: item.title }, item.title || item.page_url),
        item.subtitle ? h('div', { class: 'card__sub', title: item.subtitle }, item.subtitle) : null,
        h('div', { class: 'card__meta' },
          fmt.date(item.created_at), ' · ', String(out.container || '').toUpperCase(),
          out.height ? ` ${out.height}p` : '', item.size ? ` · ${fmt.bytes(item.size)}` : ''),
        item.error ? h('div', { class: 'card__meta', style: { color: 'var(--rec-2)' } }, item.error) : null),
      h('div', { class: 'card__foot' },
        catSel,
        h('span', { class: 'spacer' }),
        h('button', { class: 'btn btn--ghost btn--icon', title: t('library.again'), aria: { label: t('library.again') },
          onclick: () => navigate(`/grab?url=${encodeURIComponent(item.page_url)}`) }, icon('refresh')),
        h('a', { class: 'btn btn--ghost btn--icon', href: item.page_url, target: '_blank', rel: 'noopener noreferrer', title: t('library.open'), aria: { label: t('library.open') } }, icon('link')),
        h('button', { class: 'btn btn--ghost btn--icon btn--danger', title: t('common.delete'), aria: { label: t('common.delete') },
          onclick: async () => {
            try {
              await del(`/api/history/${item.id}`);
              st.items = st.items.filter((x) => x.id !== item.id);
              st.total--;
              drawItems();
              loadCategories();
            } catch (err) { toast(err.message, 'error'); }
          } }, icon('trash'))));
  }

  async function editCategory(cat) {
    const name = h('input', { class: 'input', required: true, maxlength: 60, value: cat?.name || '' });
    let color = cat?.color || COLORS[st.categories.length % COLORS.length];
    const swatches = h('div', { class: 'chips' }, COLORS.map((c) => {
      const b = h('button', { type: 'button', class: 'chip', aria: { pressed: String(c === color), label: c }, onclick: () => {
        color = c;
        for (const x of swatches.children) x.setAttribute('aria-pressed', String(x.dataset.c === c));
      }, dataset: { c } }, h('span', { class: 'chip__dot', style: { background: c } }));
      return b;
    }));
    const err = h('div', { class: 'form-error', hidden: true });
    const actions = [{ label: t('common.cancel'), kind: 'ghost' }];
    if (cat) actions.unshift({ label: t('common.delete'), kind: 'danger', value: 'delete' });
    actions.push({ label: t('common.save'), kind: 'amber', submit: true, value: 'save' });
    const res = await modal({
      title: cat ? t('library.editCategory') : t('library.newCategory'),
      body: [field(t('library.categoryName'), name), field(t('library.color'), swatches), err],
      actions,
      onSubmit: async () => {
        try {
          if (cat) await patch(`/api/categories/${cat.id}`, { name: name.value, color });
          else await post('/api/categories', { name: name.value, color });
          return true;
        } catch (e) {
          err.hidden = false;
          err.textContent = e.message;
          return false;
        }
      },
    });
    if (res === 'delete' && cat && await confirmDialog(t('library.deleteCategory', { name: cat.name }))) {
      try {
        await del(`/api/categories/${cat.id}`);
        if (st.category === cat.id) st.category = 0;
      } catch (e) { toast(e.message, 'error'); }
    }
    await loadCategories();
    if (res) load(true);
  }

  async function clearAll() {
    if (!(await confirmDialog(t('library.clearConfirm')))) return;
    try {
      await del('/api/history');
      await loadCategories();
      load(true);
    } catch (err) { toast(err.message, 'error'); }
  }

  await loadCategories();
  await load(true);
}
