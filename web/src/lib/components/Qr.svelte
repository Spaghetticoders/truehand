<script lang="ts">
  import qrcode from 'qrcode-generator'

  let { text, label, size = 196 }: { text: string; label: string; size?: number } = $props()

  const matrix = $derived.by(() => {
    const q = qrcode(0, 'M')
    q.addData(text)
    q.make()
    const n = q.getModuleCount()
    const cells: [number, number][] = []
    for (let r = 0; r < n; r++) for (let c = 0; c < n; c++) if (q.isDark(r, c)) cells.push([c, r])
    return { n, cells }
  })
</script>

<svg class="qr" style:width="{size}px" style:height="{size}px" viewBox="-2 -2 {matrix.n + 4} {matrix.n + 4}" role="img" aria-label={label} shape-rendering="crispEdges">
  <rect x="-2" y="-2" width={matrix.n + 4} height={matrix.n + 4} class="qr-bg" />
  {#each matrix.cells as [x, y] (`${x}-${y}`)}<rect {x} {y} width="1" height="1" />{/each}
</svg>

<style>
  .qr {
    flex: none;
    fill: var(--qr-fg);
    border-radius: var(--radius-sm);
  }
  .qr-bg {
    fill: var(--qr-bg);
  }
</style>
