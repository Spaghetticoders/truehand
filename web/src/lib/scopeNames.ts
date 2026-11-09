import { t } from './i18n/index.svelte'

/** A real file name with * ? or \\: the server escapes it, so only that file joins the scope ([ ] are literal in this glob syntax). */
export const specialTag = (f: string) => (/[*?\\]/.test(f) ? t('scope.special') : undefined)
