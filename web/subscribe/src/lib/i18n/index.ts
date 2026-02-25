// index.ts — Simple i18n store for the Roost subscriber portal (P14-T06).
//
// Supports English and Arabic. Language detection order:
//   1. localStorage 'roost_lang' preference
//   2. navigator.language browser setting
//   3. Default: English
//
// RTL support: Arabic sets document.documentElement.dir = 'rtl'.
// The layout reads the `dir` attribute to apply RTL-specific Tailwind classes.
import { writable, derived } from 'svelte/store';
import en from './en.json';
import ar from './ar.json';

export type Locale = 'en' | 'ar';
export type TranslationKey = string;

// All supported locales and their translation maps.
const translations: Record<Locale, Record<string, unknown>> = { en, ar };

// Detect initial language.
function detectLocale(): Locale {
	if (typeof localStorage !== 'undefined') {
		const stored = localStorage.getItem('roost_lang');
		if (stored === 'ar' || stored === 'en') return stored;
	}
	if (typeof navigator !== 'undefined') {
		const lang = navigator.language.toLowerCase();
		if (lang.startsWith('ar')) return 'ar';
	}
	return 'en';
}

// The active locale store.
export const locale = writable<Locale>(detectLocale());

// Persist locale changes and update document direction.
locale.subscribe((lang) => {
	if (typeof localStorage !== 'undefined') {
		localStorage.setItem('roost_lang', lang);
	}
	if (typeof document !== 'undefined') {
		document.documentElement.dir = lang === 'ar' ? 'rtl' : 'ltr';
		document.documentElement.lang = lang;
	}
});

// Deep path lookup: t('auth.email') → translations[locale].auth.email
function lookup(obj: Record<string, unknown>, path: string): string {
	const parts = path.split('.');
	let current: unknown = obj;
	for (const part of parts) {
		if (current == null || typeof current !== 'object') return path;
		current = (current as Record<string, unknown>)[part];
	}
	if (typeof current === 'string') return current;
	return path; // key not found — return key as fallback
}

// Template interpolation: t('subscription.cancellation_note', { date: '2026-03-01' })
function interpolate(str: string, vars?: Record<string, string>): string {
	if (!vars) return str;
	return str.replace(/\{\{(\w+)\}\}/g, (_, key) => vars[key] ?? `{{${key}}}`);
}

// Derived translate function — reactive to locale changes.
export const t = derived(locale, ($locale) => {
	const dict = translations[$locale] as Record<string, unknown>;
	return (key: string, vars?: Record<string, string>): string => {
		const raw = lookup(dict, key);
		return interpolate(raw, vars);
	};
});

// Utility: set locale manually.
export function setLocale(lang: Locale): void {
	locale.set(lang);
}

// Utility: check if current locale is RTL.
export const isRTL = derived(locale, ($locale) => $locale === 'ar');

// Re-export locale options for UI selectors.
export const LOCALE_OPTIONS: Array<{ code: Locale; label: string; nativeLabel: string }> = [
	{ code: 'en', label: 'English', nativeLabel: 'English' },
	{ code: 'ar', label: 'Arabic', nativeLabel: 'العربية' }
];
