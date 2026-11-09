/** The plan's first section (its goal): what the person needs before deciding. */
export function planBrief(plan: string): string {
  const m = /^##[^\n]*\n([\s\S]*?)(?=^##\s|$(?![\s\S]))/m.exec(plan)
  return (m?.[1] ?? plan.split('\n\n').slice(0, 2).join('\n\n')).trim()
}

/** The same, as one plain line for a card: no markup is ever rendered from it. */
export const planBriefText = (plan: string) =>
  planBrief(plan)
    .replace(/[`*_>#[\]]/g, '')
    .replace(/\s+/g, ' ')
    .trim()
