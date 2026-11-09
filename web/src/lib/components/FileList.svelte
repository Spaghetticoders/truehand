<script lang="ts">
  import Reveal from './Reveal.svelte'
  import { t } from '../i18n/index.svelte'

  /** count: the full number when the server sends a shortened list. */
  let { files, count, label, open = false, tag }: { files: string[]; count?: number; label: string; open?: boolean; tag?: (f: string) => string | undefined } = $props()

  const n = $derived(Math.max(count ?? 0, files.length))

  // Long lists (a build cache, a vendored tool) read better by folder: up to two levels deep.
  const groups = $derived.by(() => {
    const out = new Map<string, string[]>()
    for (const f of files) {
      const parts = f.split('/')
      const key = parts.length > 2 ? `${parts.slice(0, 2).join('/')}/` : parts.length === 2 ? `${parts[0]}/` : ''
      out.set(key, [...(out.get(key) ?? []), f])
    }
    return [...out].sort(([a], [b]) => a.localeCompare(b))
  })
</script>

<details class="files" {open}>
  <summary>{label}</summary>
  <ul>
    {#each groups as [dir, list] (dir)}
      {#if dir && list.length > 1}
        <li>
          <details>
            <summary><code><Reveal text={dir} /></code> <span class="dim">({list.length})</span></summary>
            <ul>
              {#each list as f (f)}<li><code><Reveal text={f.slice(dir.length)} /></code>{#if tag?.(f)}<span class="tag">{tag(f)}</span>{/if}</li>{/each}
            </ul>
          </details>
        </li>
      {:else}
        {#each list as f (f)}<li><code><Reveal text={f} /></code>{#if tag?.(f)}<span class="tag">{tag(f)}</span>{/if}</li>{/each}
      {/if}
    {/each}
  </ul>
  {#if n > files.length}<p class="dim">{t('restack.ignore.more', { n: n - files.length })}</p>{/if}
</details>

<style>
  summary {
    cursor: pointer;
  }
  .files > ul {
    max-height: 220px;
    overflow: auto;
  }
  ul {
    margin: 4px 0 0;
    padding-left: 18px;
  }
  p {
    margin: 4px 0 0;
  }
  .dim {
    color: var(--text-2);
  }
  .tag {
    display: inline-block;
    margin-left: 6px;
    padding: 0 6px;
    border-radius: 8px;
    background: var(--info-bg);
    color: var(--info);
    font-size: 11px;
    font-weight: 600;
  }
  code {
    font-family: var(--mono);
    font-size: 11.5px;
    color: var(--text);
    overflow-wrap: anywhere;
  }
</style>
