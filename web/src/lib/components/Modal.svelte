<script lang="ts">
  import type { Snippet } from 'svelte'
  import Icon from './Icon.svelte'
  import { t } from '../i18n/index.svelte'

  let {
    open = $bindable(false),
    title,
    children,
    footer,
    onsubmit,
    onclose,
  }: { open?: boolean; title: string; children: Snippet; footer: Snippet; onsubmit: () => void; onclose?: () => void } = $props()

  let dialog: HTMLDialogElement
  const titleId = `modal-${Math.random().toString(36).slice(2, 8)}`

  $effect(() => {
    if (open && !dialog.open) dialog.showModal()
    else if (!open && dialog.open) dialog.close()
  })
</script>

<dialog bind:this={dialog} aria-labelledby={titleId} onclose={() => ((open = false), onclose?.())} onclick={(e) => e.target === dialog && dialog.close()}>
  <form
    method="dialog"
    onsubmit={(e) => {
      e.preventDefault()
      onsubmit()
    }}
  >
    <header>
      <h2 id={titleId}>{title}</h2>
      <button type="button" class="btn ghost icon" aria-label={t('common.close')} onclick={() => dialog.close()}><Icon name="close" /></button>
    </header>
    <div class="body">{@render children()}</div>
    <footer>{@render footer()}</footer>
  </form>
</dialog>

<style>
  dialog {
    width: min(560px, calc(100vw - 24px));
    max-height: calc(100dvh - 48px);
    padding: 0;
    border: 1px solid var(--border);
    border-radius: 12px;
    background: var(--surface);
    color: var(--text);
    box-shadow: var(--shadow);
  }
  dialog::backdrop {
    background: var(--overlay);
  }
  form {
    display: flex;
    flex-direction: column;
    max-height: calc(100dvh - 48px);
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 14px 14px 6px 20px;
  }
  h2 {
    font-size: 16px;
  }
  .body {
    display: grid;
    gap: 16px;
    padding: 10px 20px 20px;
    overflow: auto;
  }
  footer {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    padding: 12px 20px;
    border-top: 1px solid var(--border);
  }
  @media (max-width: 640px) {
    dialog {
      margin: auto auto 0;
      width: 100vw;
      max-width: 100vw;
      border-radius: 14px 14px 0 0;
    }
  }
</style>
