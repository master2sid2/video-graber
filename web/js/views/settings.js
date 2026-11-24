// Settings: a 2×2 grid of cards that fills the window
// (cookies · default output · account · about).

import { t, languages, lang, setLanguage } from '../i18n.js';
import { h, icon, mount, toast, field, selectEl, segmented } from '../dom.js';
import { get, put, del } from '../api.js';
import { state, caps, isIndividual, savePrefs } from '../state.js';

/** Personal YouTube cookies (individual mode only). */
function cookiesCard() {
  const endpoint = '/api/cookies';
  const status = h('span', { class: 'tag' }, '…');
  const area = h('textarea', { class: 'textarea cookies-text', placeholder: '# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t1767225600\tSID\t…', spellcheck: 'false', aria: { label: t('cookies.title') } });
  const fileInput = h('input', { type: 'file', accept: '.txt,text/plain', hidden: true, onchange: async () => {
    const f = fileInput.files[0];
    if (f) area.value = await f.text();
    fileInput.value = '';
  } });
  const draw = (info) => {
    status.className = ['tag', info.present && 'tag--ok'].filter(Boolean).join(' ');
    status.textContent = info.present ? t('cookies.loaded', { count: info.count, domains: info.domains }) : t('cookies.none');
  };
  get(endpoint).then(draw).catch((e) => toast(e.message, 'error'));

  return h('section', { class: 'card' },
    h('div', { class: 'card__head' }, h('h2', { class: 'card__title grow' }, t('cookies.title')), status),
    h('div', { class: 'card__body' },
      h('p', { class: 'muted', style: { margin: '0' } }, t('cookies.intro')),
      h('ol', { class: 'steps' }, h('li', null, t('cookies.step1')), h('li', null, t('cookies.step2')), h('li', null, t('cookies.step3'))),
      area, fileInput,
      h('p', { class: 'hint', style: { margin: '0' } }, t('cookies.privacy'))),
    h('div', { class: 'card__foot' },
      h('button', { class: 'btn btn--sm', type: 'button', onclick: () => fileInput.click() }, icon('upload'), t('cookies.upload')),
      h('span', { class: 'grow' }),
      h('button', { class: 'btn btn--ghost btn--danger btn--sm', type: 'button', onclick: async () => {
        try { draw(await del(endpoint)); toast(t('cookies.removed'), 'ok'); } catch (e) { toast(e.message, 'error'); }
      } }, icon('trash'), t('common.remove')),
      h('button', { class: 'btn btn--sel btn--sm', type: 'button', onclick: async () => {
        try {
          draw(await put(endpoint, { content: area.value }));
          area.value = '';
          toast(t('cookies.saved'), 'ok');
        } catch (e) { toast(e.message, 'error'); }
      } }, icon('check'), t('common.save'))));
}

function defaultsCard() {
  const p = { ...state.prefs };
  const vid = (caps().containers || []).filter((c) => !c.audio_only);
  const aud = (caps().containers || []).filter((c) => c.audio_only);
  return h('section', { class: 'card' },
    h('div', { class: 'card__head' }, h('h2', { class: 'card__title' }, t('settings.defaults'))),
    h('div', { class: 'card__body' },
      h('p', { class: 'muted', style: { margin: '0' } }, isIndividual() ? t('settings.defaultsAccount') : t('settings.defaultsBrowser')),
      h('div', { class: 'grid2' },
        field(t('grab.container'), selectEl(vid.map((c) => ({ value: c.id, label: c.label })), p.container || 'mp4', (v) => { p.container = v; })),
        field(t('settings.audioContainer'), selectEl(aud.map((c) => ({ value: c.id, label: c.label })), p.audioContainer || 'mp3', (v) => { p.audioContainer = v; }))),
      h('div', { class: 'grid2' },
        field(t('grab.resolution'), selectEl((caps().heights || [0]).map((x) => ({ value: x, label: x ? `${x}p` : t('grab.original') })), p.height || 0, (v) => { p.height = Number(v); })),
        field(t('grab.audioBitrate'), selectEl([0, 128, 192, 256, 320].map((b) => ({ value: b, label: b ? `${b} kbps` : t('grab.auto') })), p.audioBitrate || 0, (v) => { p.audioBitrate = Number(v); }))),
      h('div', null, h('span', { class: 'label' }, t('grab.delivery')), segmented([
        { value: 'stream', label: t('grab.modeStream'), icon: 'bolt' },
        { value: 'file', label: t('grab.modeFile'), icon: 'file' },
      ], p.mode || 'stream', (v) => { p.mode = v; })),
      languages.length > 1 ? field(t('settings.language'), selectEl(languages.map((l) => ({ value: l, label: t(`lang.${l}`) })), lang(), (v) => { setLanguage(v); location.reload(); })) : null),
    h('div', { class: 'card__foot' }, h('span', { class: 'grow' }),
      h('button', { class: 'btn btn--sel btn--sm', onclick: async () => { await savePrefs(p); toast(t('settings.savedDefaults'), 'ok'); } }, icon('check'), t('common.save'))));
}

function aboutCard(span) {
  const s = state.status;
  const yesno = (b) => h('span', { class: ['tag', b && 'tag--ok'] }, b ? t('common.on') : t('common.off'));
  return h('section', { class: 'card', style: span ? { gridColumn: '1 / -1' } : null },
    h('div', { class: 'card__head' }, h('h2', { class: 'card__title' }, t('settings.about'))),
    h('div', { class: 'card__body' }, h('dl', { class: 'kv' },
      h('dt', null, t('settings.version')), h('dd', { class: 'mono' }, s.version),
      h('dt', null, t('settings.mode')), h('dd', null, t(`mode.${s.mode}`)),
      h('dt', null, 'ffprobe'), h('dd', null, yesno(s.features.ffprobe)),
      h('dt', null, 'yt-dlp'), h('dd', null, yesno(s.features.ytdlp)),
      h('dt', null, t('settings.browser')), h('dd', null, yesno(s.features.browser)),
      h('dt', null, t('settings.videoEncoders')), h('dd', { class: 'mono small' }, (caps().video_codecs || []).filter((c) => c !== 'copy').join(', ') || '—'),
      h('dt', null, t('settings.audioEncoders')), h('dd', { class: 'mono small' }, (caps().audio_codecs || []).filter((c) => c !== 'copy').join(', ') || '—'),
      h('dt', null, t('settings.workers')), h('dd', null, String(s.max_jobs)),
      h('dt', null, t('settings.modules')), h('dd', null, h('div', { class: 'chips' }, (s.modules || []).map((m) => h('span', { class: 'tag' }, m)))))));
}

export async function render(root) {
  const s = state.status;
  const cards = [];
  if (s.features.cookies) cards.push(cookiesCard());
  cards.push(defaultsCard());
  cards.push(aboutCard(cards.length % 2 === 0));
  mount(root,
    h('div', { class: 'vhead' }, h('h1', null, t('settings.title'))),
    h('div', { class: 'vbody' }, h('div', { class: 'tiles' }, cards)));
}
