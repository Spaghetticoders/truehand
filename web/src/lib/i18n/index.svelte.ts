import it from './it.json'
import en from './en.json'

export type Locale = 'it' | 'en'

type Leaves<T, P extends string = ''> = {
  [K in keyof T & string]: T[K] extends string ? `${P}${K}` : Leaves<T[K], `${P}${K}.`>
}[keyof T & string]

export type Key = Leaves<typeof it>

const dictionaries: Record<Locale, unknown> = { it, en: en satisfies typeof it }

export const i18n = $state({ locale: 'it' as Locale })

function lookup(dict: unknown, key: string): string | undefined {
  let node: unknown = dict
  for (const part of key.split('.')) {
    if (typeof node !== 'object' || node === null) return undefined
    node = (node as Record<string, unknown>)[part]
  }
  return typeof node === 'string' ? node : undefined
}

export function t(key: Key, params?: Record<string, string | number>): string {
  const s = lookup(dictionaries[i18n.locale], key) ?? lookup(it, key) ?? key
  return params ? s.replace(/\{(\w+)\}/g, (_, k) => String(params[k] ?? `{${k}}`)) : s
}

export function setLocale(l: Locale) {
  i18n.locale = l
  document.documentElement.lang = l
}

export function timeAgo(iso: string): string {
  const mins = Math.round((Date.now() - new Date(iso).getTime()) / 60_000)
  if (mins < 1) return t('time.now')
  if (mins < 60) return t('time.minutes', { n: mins })
  if (mins < 60 * 24) return t('time.hours', { n: Math.round(mins / 60) })
  return t('time.days', { n: Math.round(mins / 1440) })
}
