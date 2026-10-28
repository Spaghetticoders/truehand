<script lang="ts">
  import Icon from './Icon.svelte'
  import Reveal from './Reveal.svelte'
  import { countInvisible, invisibleLines } from '../invisible'
  import { t } from '../i18n/index.svelte'

  let { text }: { text: string } = $props()

  const n = $derived(countInvisible(text))
  const lines = $derived(n ? invisibleLines(text) : [])
</script>

{#if n}
  <div class="inv" role="alert">
    <p><Icon name="alert" size={14} />{t('invisible.warning', { n })}</p>
    <details>
      <summary>{t('invisible.where', { n: lines.length })}</summary>
      <ol>
        {#each lines as l (l.n)}
          <li><span class="ln mono">{l.n}</span><code><Reveal text={l.text} /></code></li>
        {/each}
      </ol>
    </details>
  </div>
{/if}

<style>
  .inv {
    display: grid;
    gap: 6px;
    padding: 10px 12px;
    border-radius: var(--radius-sm);
    background: var(--warn-bg);
    color: var(--warn);
    font-size: 13px;
  }
  p {
    display: flex;
    gap: 6px;
    align-items: center;
    font-weight: 600;
  }
  summary {
    cursor: pointer;
    font-size: 12.5px;
  }
  ol {
    list-style: none;
    margin: 6px 0 0;
    padding: 0;
    display: grid;
    gap: 2px;
    color: var(--text);
  }
  li {
    display: grid;
    grid-template-columns: 3em minmax(0, 1fr);
    gap: 8px;
  }
  .ln {
    color: var(--text-3);
    text-align: right;
  }
  code {
    font-size: 12px;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
</style>
