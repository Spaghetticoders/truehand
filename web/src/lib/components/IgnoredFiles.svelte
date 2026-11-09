<script lang="ts">
  import Icon from './Icon.svelte'
  import FileList from './FileList.svelte'
  import { t } from '../i18n/index.svelte'

  let { files, on, count }: { files: string[]; on: string; count?: number } = $props()

  const n = $derived(Math.max(count ?? 0, files.length))

  const examples = $derived(files.slice(0, 2).join(', ') + (n > 2 ? ', …' : ''))
</script>

<div class="ignored" role="note">
  <p><Icon name="alert" size={14} /><span>{t(n === 1 ? 'restack.ignore.one' : 'restack.ignore.other', { n, on, examples })}</span></p>
  <FileList {files} {count} label={t('restack.ignore.list', { n })} />
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
</style>
