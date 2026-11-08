<script lang="ts">
  import Icon from './Icon.svelte'
  import IgnoredFiles from './IgnoredFiles.svelte'
  import { api, type RestackPreview } from '../api'
  import { t } from '../i18n/index.svelte'
  import { app, noteQueued, refresh } from '../stores/app.svelte'
  import { errorText, toast } from '../stores/actions.svelte'
  import { withOwnerContext } from '../stores/sign.svelte'

  let { projectId, taskId, stackError }: { projectId: string; taskId: string; stackError?: string } = $props()

  let preview = $state<RestackPreview | null>(null)
  let busy = $state(false)
  let seq = 0

  // A 409 no-restack-point is the common case: nothing to propose.
  async function load() {
    const n = ++seq
    const r = await api.restackPreview(projectId, taskId).catch(() => null)
    if (n === seq) preview = r
  }

  $effect(() => {
    void app.data
    void load()
  })

  async function move() {
    if (!preview) return
    const p = preview
    busy = true
    try {
      const r = await withOwnerContext({ restack: p }, () => api.restack(projectId, taskId, { point: p.point, stackedOn: p.stackedOn, ignoreHash: p.ignoreHash ?? '' }))
      if (r.queued) {
        if (r.queuedId) noteQueued({ id: r.queuedId, kind: 'restack', projectId, taskId, device: t('queuedKind.thisDevice'), time: new Date().toISOString() })
        toast('queued', t('action.queued'))
      } else {
        toast('ok', t('restack.done', { on: p.stackedOn }))
        preview = null
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

{#if stackError}
  <div class="broken" role="alert">
    <p><Icon name="alert" size={14} />{t(preview ? 'stack.brokenFix' : 'stack.broken')}</p>
    <details><summary>{t('common.details')}</summary><code>{stackError}</code></details>
  </div>
{/if}
{#if preview}
  <section class="restack" aria-labelledby="restack-title">
    <h2 id="restack-title"><Icon name="branch" size={16} />{t('restack.title', { on: preview.stackedOn })}</h2>
    <p>{t('restack.body', { on: preview.stackedOn })}</p>
    <dl>
      <div><dt>{t('restack.onTask')}</dt><dd><span class="mono">{preview.stackedOn}</span> {preview.predTitle} · {t(`state.${preview.predState}`)}</dd></div>
      <div><dt>{t('restack.point')}</dt><dd class="mono">{preview.from.slice(0, 7)} → {preview.point.slice(0, 7)}</dd></div>
      <div><dt>{t('restack.covered')}</dt><dd class="mono">{preview.covered.join(', ')}</dd></div>
    </dl>
    {#if preview.ignore?.length || preview.ignoreCount}
      <IgnoredFiles files={preview.ignore ?? []} on={preview.stackedOn} count={preview.ignoreCount} />
    {/if}
    <details>
      <summary>{t('restack.skipped', { n: preview.skipped.length })}</summary>
      <ol class="commits">
        {#each preview.skipped as c (c.sha)}<li><code>{c.sha.slice(0, 7)}</code> {c.subject}</li>{/each}
      </ol>
    </details>
    <div class="row-end">
      <button class="btn primary" disabled={busy} aria-busy={busy} onclick={move}><Icon name="branch" size={14} />{t('restack.button')}</button>
    </div>
  </section>
{/if}

<style>
  .broken {
    display: grid;
    gap: 4px;
    padding: 10px 12px;
    border-radius: var(--radius-sm);
    background: var(--err-bg);
    color: var(--err);
    font-size: 13px;
  }
  .broken p {
    display: flex;
    gap: 6px;
    align-items: flex-start;
    margin: 0;
  }
  .broken code {
    display: block;
    margin-top: 4px;
    font-family: var(--mono);
    font-size: 11.5px;
    overflow-wrap: anywhere;
  }
  .restack {
    display: grid;
    gap: 8px;
    padding: 12px 14px;
    border-radius: var(--radius);
    border: 1px solid var(--border);
    background: var(--info-bg);
    color: var(--text);
  }
  h2 {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    font-size: 14px;
    color: var(--info);
  }
  p {
    margin: 0;
    font-size: 13px;
  }
  dl {
    display: grid;
    gap: 2px;
    margin: 0;
    font-size: 12.5px;
  }
  dl div {
    display: flex;
    flex-wrap: wrap;
    gap: 2px 8px;
  }
  dt {
    color: var(--text-2);
  }
  dd {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .mono {
    font-family: var(--mono);
  }
  details {
    font-size: 12.5px;
  }
  summary {
    cursor: pointer;
    color: var(--text-2);
  }
  .commits {
    margin: 6px 0 0;
    padding-left: 20px;
    display: grid;
    gap: 2px;
  }
  .commits code {
    font-family: var(--mono);
    font-size: 12px;
  }
  .row-end {
    display: flex;
    justify-content: flex-end;
  }
</style>
