<script lang="ts">
  import Icon from './Icon.svelte'
  import { t } from '../i18n/index.svelte'
  import { toasts } from '../stores/actions.svelte'

  const ICON = { info: 'check', ok: 'check', error: 'alert', queued: 'clock' } as const
</script>

<div class="toasts" role="status" aria-live="polite">
  {#each toasts.list as x (x.id)}
    <div class="toast {x.kind}">
      <Icon name={ICON[x.kind]} size={16} />
      <span>{x.text}</span>
      {#if x.href}<a href={x.href} target="_blank" rel="noreferrer">{t('common.open')}</a>{/if}
    </div>
  {/each}
</div>

<style>
  .toasts {
    position: fixed;
    right: 16px;
    bottom: 16px;
    z-index: 60;
    display: grid;
    gap: 8px;
    max-width: min(420px, calc(100vw - 32px));
  }
  .toast {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 10px 14px;
    border-radius: var(--radius);
    background: var(--surface);
    border: 1px solid var(--border-strong);
    box-shadow: var(--shadow);
    font-size: 13.5px;
  }
  .toast span {
    flex: 1;
  }
  .ok :global(svg) {
    color: var(--ok);
  }
  .error {
    border-color: var(--err);
  }
  .error :global(svg) {
    color: var(--err);
  }
  .queued {
    border-color: var(--info);
  }
  .queued :global(svg) {
    color: var(--info);
  }
  a {
    font-weight: 600;
    text-decoration: underline;
    text-underline-offset: 2px;
  }
  @media (max-width: 640px) {
    .toasts {
      left: 12px;
      right: 12px;
      bottom: 12px;
      max-width: none;
    }
  }
</style>
