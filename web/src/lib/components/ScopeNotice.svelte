<script lang="ts">
  import Icon from './Icon.svelte'
  import FileList from './FileList.svelte'
  import { specialTag } from '../scopeNames'
  import { api, type ScopePreview } from '../api'
  import { t } from '../i18n/index.svelte'
  import { app, noteQueued, refresh } from '../stores/app.svelte'
  import { errorText, toast } from '../stores/actions.svelte'
  import { withOwnerContext } from '../stores/sign.svelte'
  import { outsideScope } from '../stores/scope.svelte'

  let { projectId, taskId }: { projectId: string; taskId: string } = $props()

  let preview = $state<ScopePreview | null>(null)
  let busy = $state(false)
  let seq = 0

  async function load() {
    const n = ++seq
    const r = await api.scopePreview(projectId, taskId).catch(() => null)
    if (n !== seq) return
    preview = r && (r.outside.length || r.outsideCount) ? r : null
    // Keep the inbox in step with what this page just read.
    if (r) outsideScope[`${projectId}/${taskId}`] = Math.max(r.outsideCount ?? 0, r.outside.length)
  }

  $effect(() => {
    void app.data
    void load()
  })

  const count = $derived(preview ? Math.max(preview.outsideCount ?? 0, preview.outside.length) : 0)

  async function widen() {
    if (!preview) return
    const p = preview
    busy = true
    try {
      const r = await withOwnerContext({ scope: p }, () => api.widenScope(projectId, taskId, { add: p.add }))
      if (r.queued) {
        if (r.queuedId) noteQueued({ id: r.queuedId, kind: 'scope', projectId, taskId, device: t('queuedKind.thisDevice'), time: new Date().toISOString() })
        toast('queued', t('action.queued'))
      } else {
        toast('ok', t('scope.done'))
        preview = null
        outsideScope[`${projectId}/${taskId}`] = 0
      }
      await refresh()
    } catch (e) {
      toast('error', errorText(e))
      void load()
    } finally {
      busy = false
    }
  }
</script>

{#if preview}
  <section class="scope" aria-labelledby="scope-title">
    <h2 id="scope-title"><Icon name="folder" size={16} />{t(count === 1 ? 'scope.title.one' : 'scope.title.other', { n: count })}</h2>
    <p>{t('scope.body')}</p>
    <FileList files={preview.outside} count={preview.outsideCount} label={t('scope.willAdd', { n: count })} open={count <= 8} tag={specialTag} />
    <div class="row-end">
      <button class="btn primary" disabled={busy || !preview.add.length} aria-busy={busy} onclick={widen}><Icon name="folder" size={14} />{t('scope.button')}</button>
    </div>
  </section>
{/if}

<style>
  .scope {
    display: grid;
    gap: 8px;
    padding: 12px 14px;
    border-radius: var(--radius);
    border: 1px solid var(--border);
    background: var(--warn-bg);
    color: var(--text);
    font-size: 13px;
  }
  h2 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    font-size: 14px;
    color: var(--warn);
  }
  p {
    margin: 0;
  }
  .row-end {
    display: flex;
    justify-content: flex-end;
  }
  .btn {
    min-height: 44px;
  }
</style>
