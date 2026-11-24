// Library (individual mode): categories on the left, download history grid
// on the right with infinite scrolling inside its own card.

import { t } from '../i18n.js';
import { h, icon, mount, thumb, fmt, toast, modal, confirmDialog, selectEl, field } from '../dom.js';
import { get, post, patch, del } from '../api.js';

const PAGE = 48;
const COLORS = ['#ff5a36', '#ffb547', '#8fdc5a', '#66c8f0', '#b68cff', '#ff7ab6', '#eceae6'];

export async function render(root, { navigate }) {
  const st = { categories: [], items: [], total: 0, category: 0, q: '', loading: false };
  const catList = h('div', { class: 'catlist' });
  const sheet = h('div', { class: 'sheet' });
  const itemsBody = h('div', { class: 'card__body' }, sheet);
  const total = h('span', { class: 'n' });
  let searchTimer = null;
  const search = h('input', {
    class: 'input', type: 'search', placeholder: t('library.search'), aria: { label: t('library.search') },
    style: { maxWidth: '320px' },
    oninput: () => { clearTimeout(searchTimer); searchTimer = setTimeout(() => { st.q = search.value.trim(); load(true); }, 250); },
  });

  mount(root,
    h('div', { class: 'vhead' }, h('h1', null, t('library.title')), h('span', { class: 'grow' }),
      h('button', { class: 'btn btn--ghost btn--danger btn--sm', onclick: clearAll }, icon('trash'), t('library.clear'))),
    h('div', { class: 'vbody' }, h('div', { class: 'lib' },
      h('section', { class: 'card' },
        h('div', { class: 'card__head' }, h('h2', { class: 'card__title grow' }, t('library.categories')),
          h('button', { class: 'btn btn--ghost btn--icon btn--sm', title: t('library.newCategory'), aria: { label: t('library.newCategory') }, onclick: () => editCategory(null) }, icon('plus'))),
        h('div', { class: 'card__body', style: { padding: '8px' } }, catList)),
      h('section', { class: 'card' },
        h('div', { class: 'card__head' }, h('h2', { class: 'card__title' }, t('library.items'), total), h('span', { class: 'grow' }), search),
        itemsBody))));

  // Infinite scroll: a sentinel at the end of the grid loads the next page.
  const sentinel = h('div', { class: 'sentinel' });
  const io = new IntersectionObserver((entries) => {
    if (entries.some((e) => e.isIntersecting) && !st.loading && st.items.length < st.total) load(false);
  }, { root: null, rootMargin: '300px' });

  async function loadCategories() {
    st.categories = (await get('/api/categories')) || [];
    drawCategories();
  }

  function drawCategories() {
    const entry = (id, label, count, color, cat) => h('button', {
      type: 'button', class: 'cat', aria: { pressed: String(st.category === id) },
      onclick: () => { st.category = id; drawCategories(); load(true); },
    }, h('span', { class: 'dot', style: { background: color || 'var(--muted)' } }), h('span', { class: 'ellipsis' }, label),
    cat ? h('span', { class: 'edit', role: 'button', tabindex: '0', title: t('library.editCategory'), aria: { label: t('library.editCategory') },
      onclick: (e) => { e.stopPropagation(); editCategory(cat); },
      onkeydown: (e) => { if (e.key === 'Enter') { e.stopPropagation(); editCategory(cat); } } }, icon('edit')) : null,
    h('span', { class: 'n' }, count === null ? '' : String(count)));
    mount(catList,
      entry(0, t('library.all'), null, 'var(--text-2)'),
      entry(-1, t('library.uncategorized'), null, 'var(--line-2)'),
      st.categories.map((c) => entry(c.id, c.name, c.count, c.color, c)));
  }

  let loadSeq = 0;
  async function load(reset) {
    const seq = ++loadSeq;
    st.loading = true;
    const params = new URLSearchParams({ limit: PAGE, offset: reset ? 0 : st.items.length });
    if (st.category) params.set('category', st.category);
    if (st.q) params.set('q', st.q);
    try {
      const res = await get(`/api/history?${params}`);
      if (seq !== loadSeq) return;
      st.items = reset ? res.items : [...st.items, ...res.items];
      st.total = res.total;
      drawItems(reset);
    } catch (err) {
      toast(err.message, 'error');
    } finally {
      if (seq === loadSeq) st.loading = false;
    }
  }

  function drawItems(reset) {
    total.textContent = String(st.total);
    io.disconnect();
    if (!st.items.length) {
      mount(sheet);
      mount(itemsBody, h('div', { class: 'state' }, icon('library'),
        h('h3', null, st.q || st.category ? t('library.noMatches') : t('library.emptyTitle')),
        h('p', null, t('library.emptyText'))));
      return;
    }
    if (!itemsBody.contains(sheet)) mount(itemsBody, sheet);
    if (reset) itemsBody.scrollTop = 0;
    mount(sheet, st.items.map(card), sentinel);
    if (st.items.length < st.total) io.observe(sentinel);
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
    const meta = [fmt.date(item.created_at), String(out.container || '').toUpperCase() + (out.height ? ` ${out.height}p` : ''), item.size ? fmt.bytes(item.size) : '']
      .filter(Boolean).join(' · ');
    return h('article', { class: 'item' },
      h('div', { class: 'item__thumb' }, thumb(item.thumbnail, item.title), h('span', { class: `led led--${item.status}` }, t(`state.${item.status}`))),
      h('div', { class: 'item__body' },
        h('div', { class: 'item__title', title: item.title }, item.title || item.page_url),
        item.subtitle ? h('div', { class: 'small ellipsis', style: { color: 'var(--text-2)' }, title: item.subtitle }, item.subtitle) : null,
        h('div', { class: 'item__meta' }, meta),
        item.error ? h('div', { class: 'job__err' }, item.error) : null),
      h('div', { class: 'item__foot' },
        catSel,
        h('button', { class: 'btn btn--ghost btn--icon btn--sm', title: t('library.again'), aria: { label: t('library.again') },
          onclick: () => navigate(`/grab?url=${encodeURIComponent(item.page_url)}`) }, icon('refresh')),
        h('a', { class: 'btn btn--ghost btn--icon btn--sm', href: item.page_url, target: '_blank', rel: 'noopener noreferrer', title: t('library.open'), aria: { label: t('library.open') } }, icon('link')),
        h('button', { class: 'btn btn--ghost btn--icon btn--sm btn--danger', title: t('common.delete'), aria: { label: t('common.delete') },
          onclick: async () => {
            try {
              await del(`/api/history/${item.id}`);
              st.items = st.items.filter((x) => x.id !== item.id);
              st.total--;
              drawItems(false);
              loadCategories();
            } catch (err) { toast(err.message, 'error'); }
          } }, icon('trash'))));
  }

  async function editCategory(cat) {
    const name = h('input', { class: 'input', required: true, maxlength: 60, value: cat?.name || '' });
    let color = cat?.color || COLORS[st.categories.length % COLORS.length];
    const swatches = h('div', { class: 'chips' }, COLORS.map((c) => h('button', {
      type: 'button', class: 'chip', dataset: { c }, aria: { pressed: String(c === color), label: c },
      onclick: () => { color = c; for (const x of swatches.children) x.setAttribute('aria-pressed', String(x.dataset.c === c)); },
    }, h('span', { class: 'dot', style: { background: c } }))));
    const err = h('div', { class: 'form-error', hidden: true });
    const actions = [{ label: t('common.cancel'), kind: 'ghost' }];
    if (cat) actions.unshift({ label: t('common.delete'), kind: 'danger', value: 'delete' });
    actions.push({ label: t('common.save'), kind: 'sel', submit: true, value: 'save' });
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
  return () => io.disconnect();
}
