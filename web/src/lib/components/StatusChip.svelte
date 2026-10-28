<script lang="ts">
  import type { TaskState } from '../api'
  import { t } from '../i18n/index.svelte'

  let { state, merged = false }: { state: TaskState; merged?: boolean } = $props()

  const tone = $derived(
    merged ? 'merged' : { planned: 'idle', approved: 'info', building: 'info', verified: 'ok', reviewed: 'ok', delivered: 'ok' }[state],
  )
</script>

<span class="chip {tone}">{merged ? t('state.merged') : t(`state.${state}`)}</span>

<style>
  .chip {
    display: inline-flex;
    align-items: center;
    height: 20px;
    padding: 0 7px;
    border-radius: 10px;
    font-size: 11.5px;
    font-weight: 600;
    white-space: nowrap;
  }
  .idle {
    background: var(--idle-bg);
    color: var(--text-2);
  }
  .info {
    background: var(--info-bg);
    color: var(--info);
  }
  .ok {
    background: var(--ok-bg);
    color: var(--ok);
  }
  .merged {
    background: var(--surface-2);
    color: var(--text-2);
  }
</style>
