<script lang="ts">
  import Icon from './Icon.svelte'
  import { t } from '../i18n/index.svelte'
  import { app, refresh } from '../stores/app.svelte'
</script>

{#if app.error || app.offline}
  <div class="offline" role="alert">
    <Icon name="alert" /><span>{t('inbox.unreachable')}</span>
    <button class="btn sm" onclick={() => void refresh()}>{t('inbox.retry')}</button>
  </div>
{/if}

<style>
  .offline {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 12px;
    border-radius: var(--radius);
    background: var(--err-bg);
    color: var(--err);
    font-size: 14px;
  }
  span {
    flex: 1;
  }
  .btn {
    min-height: 44px;
    flex: none;
  }
</style>
