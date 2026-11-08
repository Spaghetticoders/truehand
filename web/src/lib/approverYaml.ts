import type { Passkey } from './api'

const ID_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/

/** An approver id from the passkey's label, unique among the ids the config already uses. */
export function approverId(label: string, taken: string[]): string {
  const base =
    label
      .normalize('NFKD')
      .replace(/[̀-ͯ]/g, '')
      .toLowerCase()
      .replace(/[^a-z0-9._-]+/g, '-')
      .replace(/^[^a-z0-9]+|-+$/g, '')
      .slice(0, 48) || 'passkey'
  let id = base
  for (let n = 2; taken.includes(id); n++) id = `${base}-${n}`
  return ID_RE.test(id) ? id : 'passkey'
}

export const takenIds = (yaml: string) => [...yaml.matchAll(/^\s*-\s+id:\s*["']?([A-Za-z0-9._-]+)/gm)].map((m) => m[1])

/** Appends the passkey to the top-level approvers list, leaving every other line as it is. */
export function addApprover(yaml: string, p: Passkey, id: string): string {
  const pem = p.publicKey.trim().split('\n')
  const entry = (ind: string) =>
    [
      `${ind}- id: ${id}`,
      `${ind}  name: ${JSON.stringify(p.label)}`,
      `${ind}  public_key: |`,
      ...pem.map((l: string) => `${ind}    ${l.trim()}`),
      `${ind}  rp_id: ${p.rpId.toLowerCase()}`,
    ].join('\n')
  const lines = yaml.replace(/\s+$/, '').split('\n')
  const at = lines.findIndex((l) => /^approvers:\s*(\[\s*\])?\s*(#.*)?$/.test(l))
  if (at < 0) return `${lines.join('\n')}${lines.length && lines[0] !== '' ? '\n\n' : ''}approvers:\n${entry('  ')}\n`
  if (/\[\s*\]/.test(lines[at])) lines[at] = 'approvers:'
  let end = at + 1
  while (end < lines.length && (lines[end].trim() === '' || /^[\s#]/.test(lines[end]) || lines[end].startsWith('-'))) end++
  while (end > at + 1 && lines[end - 1].trim() === '') end--
  const item = lines.slice(at + 1, end).find((l) => /^\s*-\s/.test(l))
  const ind = item ? (/^(\s*)-/.exec(item)?.[1] ?? '  ') : '  '
  lines.splice(end, 0, entry(ind))
  return lines.join('\n') + '\n'
}
