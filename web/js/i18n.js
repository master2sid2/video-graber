// Minimal i18n layer. Add a language by creating js/locales/<code>.js
// (same keys as en.js) and registering it in `dictionaries` below.

import en from './locales/en.js';

const dictionaries = { en };
const FALLBACK = 'en';

export const languages = Object.keys(dictionaries);

function detect() {
  const saved = localStorage.getItem('vg.lang');
  if (saved && dictionaries[saved]) return saved;
  for (const l of navigator.languages || [navigator.language]) {
    const code = String(l).toLowerCase().split('-')[0];
    if (dictionaries[code]) return code;
  }
  return FALLBACK;
}

let current = detect();
document.documentElement.lang = current;

export function lang() { return current; }

export function setLanguage(code) {
  if (!dictionaries[code]) return;
  current = code;
  localStorage.setItem('vg.lang', code);
  document.documentElement.lang = code;
}

function lookup(key) {
  return dictionaries[current]?.[key] ?? dictionaries[FALLBACK][key];
}

/**
 * Translate key, replacing {name} placeholders with params.
 * When params.count is set, "<key>_one" / "<key>_other" plural forms are used.
 */
export function t(key, params = {}) {
  let s;
  if (typeof params.count === 'number') {
    const rule = new Intl.PluralRules(current).select(params.count);
    s = lookup(`${key}_${rule}`) ?? lookup(`${key}_other`);
  }
  s ??= lookup(key);
  if (s === undefined) return key;
  return s.replace(/\{(\w+)\}/g, (m, k) => (k in params ? String(params[k]) : m));
}
