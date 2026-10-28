// Characters that render as nothing (or reorder text) yet are part of what gets approved.
const INVISIBLE = /(?![\n\t]|\r\n)[\p{Cc}\p{Cf}\p{Zl}\p{Zp}\u115F\u1160\u2800\u3164\uFFA0\uFE00-\uFE0F\u{E0100}-\u{E01EF}]/gu

export const countInvisible = (s: string) => s.match(INVISIBLE)?.length ?? 0

const code = (c: string) => 'U+' + c.codePointAt(0)!.toString(16).toUpperCase().padStart(4, '0')

/** The text split into plain runs and visible stand-ins for each invisible character. */
export function revealParts(s: string): { text: string; hidden: boolean }[] {
  const out: { text: string; hidden: boolean }[] = []
  let last = 0
  for (const m of s.matchAll(INVISIBLE)) {
    if (m.index > last) out.push({ text: s.slice(last, m.index), hidden: false })
    out.push({ text: code(m[0]), hidden: true })
    last = m.index + m[0].length
  }
  if (last < s.length) out.push({ text: s.slice(last), hidden: false })
  return out
}

/** Lines that hold invisible characters, numbered from 1. */
export const invisibleLines = (s: string) =>
  s
    .split('\n')
    .map((text, i) => ({ n: i + 1, text }))
    .filter((l) => countInvisible(l.text) > 0)
