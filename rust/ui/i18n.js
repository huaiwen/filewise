'use strict';
// No framework: keyed messages, locale fallback and native Intl formatting.
const FilewiseI18n = {
  resolve(preference, languages = []) {
    if (['en', 'zh-CN'].includes(preference)) return preference;
    for (const language of languages) {
      if (/^zh(?:-|$)/i.test(language)) return 'zh-CN';
      if (/^en(?:-|$)/i.test(language)) return 'en';
    }
    return 'en';
  },
  create(messages, locale) {
    return {
      locale,
      t(key, params = {}) {
        const plural = typeof params.count === 'number' ? new Intl.PluralRules(locale).select(params.count) : '';
        const template = messages[locale]?.[key + '.' + plural] ?? messages[locale]?.[key] ?? messages.en[key + '.' + plural] ?? messages.en[key] ?? key;
        return template.replace(/\{(\w+)\}/g, (match, name) => Object.hasOwn(params, name) ? (typeof params[name] === 'number' ? this.number(params[name]) : String(params[name])) : match);
      },
      number(value) { return new Intl.NumberFormat(locale).format(value); },
      date(value) {
        const date = new Date(value);
        return Number.isNaN(date.valueOf()) ? '' : new Intl.DateTimeFormat(locale, {dateStyle:'medium', timeStyle:'short'}).format(date);
      }
    };
  },
  apply(root, translator) {
    for (const e of root.querySelectorAll('[data-i18n]')) e.textContent = translator.t(e.dataset.i18n);
    for (const attr of ['placeholder', 'aria-label']) {
      for (const e of root.querySelectorAll('[data-i18n-' + attr + ']')) e.setAttribute(attr, translator.t(e.getAttribute('data-i18n-' + attr)));
    }
  }
};
if (typeof module !== 'undefined') module.exports = FilewiseI18n;
