// Each plan card loads its task for the summary: at most a few at a time, until the server sends a digest.
const MAX = 4
let running = 0
const waiting: (() => void)[] = []

export async function limited<T>(f: () => Promise<T>): Promise<T> {
  if (running >= MAX) await new Promise<void>((r) => waiting.push(r))
  else running++
  try {
    return await f()
  } finally {
    // The slot goes straight to whoever waits, so a newcomer cannot slip in between.
    const next = waiting.shift()
    if (next) next()
    else running--
  }
}
