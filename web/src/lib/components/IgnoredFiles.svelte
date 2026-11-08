<script lang="ts">
  import Icon from './Icon.svelte'
  import Reveal from './Reveal.svelte'
  import { t } from '../i18n/index.svelte'

  let { files, on, count }: { files: string[]; on: string; count?: number } = $props()

  const n = $derived(Math.max(count ?? 0, files.length))

  const examples = $derived(files.slice(0, 2).join(', ') + (n > 2 ? ', …' : ''))

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

<div class="ignored" role="note">
  <p><Icon name="alert" size={14} /><span>{t(n === 1 ? 'restack.ignore.one' : 'restack.ignore.other', { n, on, examples })}</span></p>
  <details>
    <summary>{t('restack.ignore.list', { n })}</summary>
    <ul>
      {#each groups as [dir, list] (dir)}
        {#if dir && list.length > 1}
          <li>
            <details>
              <summary><code><Reveal text={dir} /></code> <span class="folder">({list.length})</span></summary>
              <ul>
                {#each list as f (f)}<li><code><Reveal text={f.slice(dir.length)} /></code></li>{/each}
              </ul>
            </details>
          </li>
        {:else}
          {#each list as f (f)}<li><code><Reveal text={f} /></code></li>{/each}
        {/if}
      {/each}
    </ul>
    {#if n > files.length}<p class="more">{t('restack.ignore.more', { n: n - files.length })}</p>{/if}
  </details>
</div>

<style>
  .ignored {
    display: grid;
    gap: 4px;
    padding: 8px 10px;
    border-radius: var(--radius-sm);
    background: var(--warn-bg);
    color: var(--warn);
    font-size: 12.5px;
  }
  p {
    display: flex;
    gap: 6px;
    align-items: flex-start;
    margin: 0;
  }
  summary {
    cursor: pointer;
  }
  .ignored > details > ul {
    max-height: 220px;
    overflow: auto;
  }
  ul {
    margin: 4px 0 0;
    padding-left: 18px;
  }
  .folder,
  .more {
    color: var(--text-2);
  }
  code {
    font-family: var(--mono);
    font-size: 11.5px;
    color: var(--text);
    overflow-wrap: anywhere;
  }
</style>
