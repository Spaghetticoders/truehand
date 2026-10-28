<script lang="ts">
  import { onMount, untrack } from 'svelte'
  import Icon from '../components/Icon.svelte'
  import Reveal from '../components/Reveal.svelte'
  import InvisibleNotice from '../components/InvisibleNotice.svelte'
  import { api, ApiError, type Activation, type ApproverRef, type HookState } from '../api'
  import { t, timeAgo } from '../i18n/index.svelte'
  import { app, clientName, noteQueued, project, refresh } from '../stores/app.svelte'
  import { href } from '../stores/router.svelte'
  import { confirmQueued, discardQueued, errorText, toast } from '../stores/actions.svelte'

  let { projectId }: { projectId: string } = $props()

  let a = $state<Activation | null>(null)
  let loaded = $state(false)
  let stack = $state('')
  let base = $state('')
  let yaml = $state('')
  let force = $state(false)
  let saving = $state(false)
  let error = $state<string | null>(null)
  let dirty = $state(false)
  let macKey = $state<string | null>(null)

  const proj = $derived(project(projectId))
  const canApprove = $derived(app.data?.me.canApprove ?? false)
  const foreign = $derived(a ? Object.values(a.gitHooks).includes('foreign') : false)
  const needsCommit = $derived(!!a?.hasConfig && !!proj?.active && (!a.configCommitted || !!a.configDiff))
  // Committed on the local base but not yet on origin: nothing left to commit, only to push.
  const pushPending = $derived(!!a?.configCommitted && !!a.configDiff && !dirty)
  const queuedCommit = $derived(app.data?.queued.find((q) => q.kind === 'commit-config' && q.projectId === projectId))
  const queuedStale = $derived(!!queuedCommit?.configSha256 && !!a?.configSha256 && queuedCommit.configSha256 !== a.configSha256)
  const added = $derived(new Set(a?.approversAdded?.map((x) => x.id + x.key)))
  const localBase = $derived((a?.base ?? '').replace(/^origin\//, ''))

  async function load(quiet = false) {
    if (!quiet) loaded = false
    try {
      const x = await api.activation(projectId)
      if (quiet && (dirty || saving)) return
      a = x
      stack = x.stack
      base = x.base
      yaml = x.configYaml
      dirty = false
      loaded = true
    } catch (e) {
      error = errorText(e)
    }
  }

  // Keep up with changes made elsewhere (a confirmed commit, an agent editing the file), but never under unsaved edits.
  $effect(() => {
    void app.data
    untrack(() => loaded && !dirty && !saving && void load(true))
  })

  onMount(() => {
    void load()
    api
      .macKeyFingerprint()
      ?.then((k) => (macKey = k))
      .catch(() => (macKey = null))
  })

  async function saveConfig() {
    saving = true
    error = null
    try {
      const r = await api.putConfig(projectId, yaml)
      if ('queued' in r) {
        dirty = false
        toast('queued', t('activate.queued'))
      } else {
        await load()
        if (!a?.configError) toast('ok', t('activate.saved'))
      }
    } catch (e) {
      error = errorText(e)
    } finally {
      saving = false
    }
  }

  async function activate() {
    saving = true
    error = null
    try {
      if (dirty) await saveConfig()
      await api.activate(projectId, { stack, base, forceHooks: force })
      // The reply may not carry the config yet: an empty editor saved now would wipe it.
      await load()
      toast('ok', t('activate.done', { name: proj?.name ?? '' }))
    } catch (e) {
      error = errorText(e)
    } finally {
      saving = false
    }
  }

  async function commit() {
    if (!a?.configSha256) return
    saving = true
    error = null
    try {
      const r = await api.commitConfig(projectId, a.configSha256)
      if (r.queued) {
        if (r.queuedId) noteQueued({ id: r.queuedId, kind: 'commit-config', projectId, taskId: '', configSha256: a.configSha256, device: t('queuedKind.thisDevice'), time: new Date().toISOString() })
        toast('queued', t('action.queued'))
      } else if (!r.ok) throw new Error(r.error)
      else {
        toast('ok', t('activate.committed', { base: localBase || a.base }))
      }
      await load()
    } catch (e) {
      error = errorText(e)
      if (e instanceof ApiError && e.code === 'config-changed') await load()
    } finally {
      saving = false
      void refresh()
    }
  }

  async function confirmCommit(qid: string) {
    saving = true
    try {
      await confirmQueued(qid)
      await load()
    } finally {
      saving = false
    }
  }

  const whose = (x: ApproverRef) => (macKey === null ? null : x.key === macKey ? 'mine' : 'foreign')
  const diffLine = (l: string) => (l.startsWith('+++') || l.startsWith('---') ? 'meta' : l.startsWith('+') ? 'add' : l.startsWith('-') ? 'del' : l.startsWith('@@') ? 'hunk' : '')

  const HOOK_TONE: Record<HookState, string> = { ours: 'ok', missing: 'idle', foreign: 'warn' }
</script>

<div class="activate">
  <nav class="crumbs"><a href={href({ name: 'board', project: projectId })}><Icon name="chevron-left" size={14} />{clientName(proj?.clientId ?? '')} / {proj?.name}</a></nav>
  <header>
    <h1>{proj?.active ? t('activate.titleActive') : t('activate.title', { name: proj?.name ?? '' })}</h1>
    <p class="muted">{t('activate.intro')}</p>
  </header>

  {#if a}
    <section class="card step">
      <h2><span class="num">1</span>{t('activate.found')}</h2>
      <div class="grid">
        <label class="field">
          <span>{t('activate.stack')}</span>
          <input class="input mono" bind:value={stack} disabled={proj?.active} />
        </label>
        <label class="field">
          <span>{t('activate.base')}</span>
          <input class="input mono" bind:value={base} disabled={proj?.active} />
          <small>{t('activate.baseHint')}</small>
        </label>
      </div>
    </section>

    <section class="card step">
      <h2><span class="num">2</span>{t('activate.hooks')}</h2>
      <p class="muted small">{t('activate.hooksHint')}</p>
      <ul class="hooks">
        {#each Object.entries(a.gitHooks) as [name, state] (name)}
          <li>
            <code>{name}</code>
            <span class="chip {HOOK_TONE[state]}">{t(`hook.${state}`)}</span>
          </li>
        {/each}
      </ul>
      {#if foreign && !proj?.active}
        <label class="force">
          <input type="checkbox" bind:checked={force} disabled={!app.data?.me.canApprove} />
          <span>{t('activate.force')}{#if !app.data?.me.canApprove}<small>{t('activate.forceMacOnly')}</small>{/if}</span>
        </label>
      {/if}
    </section>

    <section class="card step">
      <h2><span class="num">3</span>{t('activate.rules')}</h2>
      <p class="muted small">{t('activate.rulesHint')}</p>
      {#if a.hasConfig || a.configYaml}
        <InvisibleNotice text={yaml} />
        <label class="sr-only" for="config">.truehand/config.yaml</label>
        <textarea
          id="config"
          class="input mono yaml"
          spellcheck="false"
          rows="18"
          bind:value={yaml}
          disabled={!loaded}
          oninput={() => (dirty = true)}
        ></textarea>
        {#if a.configError}
          <p class="msg err"><Icon name="alert" size={14} />{a.configError}</p>
        {:else if a.hasConfig && !dirty}
          <p class="msg ok"><Icon name="check" size={14} />{t('activate.valid')}</p>
        {/if}
        <div class="row-end">
          <button class="btn" disabled={!loaded || !dirty || saving} onclick={saveConfig}>{t('activate.check')}</button>
        </div>
      {:else}
        <p class="muted small">{t('activate.rulesLater')}</p>
      {/if}
    </section>

    {#if needsCommit}
      <section class="card step" aria-labelledby="commit-title">
        <h2 id="commit-title"><span class="num">4</span>{t(pushPending ? 'activate.pushTitle' : 'activate.commitTitle', { base: pushPending ? a.base : localBase || a.base })}</h2>
        <p class="muted small">{t(pushPending ? 'activate.pushHint' : 'activate.commitHint')}</p>

        <h3 class="sub">{t('activate.approvers')}</h3>
        {#if a.approvers?.length || a.approversRemoved?.length}
          <ul class="approvers">
            {#each a.approvers ?? [] as x (x.id + x.key)}
              {@const w = whose(x)}
              <li>
                <span class="ap-name"><Reveal text={x.name || x.id} /></span>
                <code class="ap-id"><Reveal text={x.id} /></code>
                <code class="ap-key">{t('activate.key', { key: x.key })}</code>
                {#if w}<span class="chip {w === 'mine' ? 'ok' : 'warn'}">{t(w === 'mine' ? 'activate.thisMac' : 'activate.foreignKey')}</span>{/if}
                {#if added.has(x.id + x.key)}<span class="chip info">{t('activate.approverAdded')}</span>{/if}
              </li>
            {/each}
            {#each a.approversRemoved ?? [] as x (x.id + x.key)}
              <li class="removed">
                <span class="ap-name"><Reveal text={x.name || x.id} /></span>
                <code class="ap-id"><Reveal text={x.id} /></code>
                <code class="ap-key">{t('activate.key', { key: x.key })}</code>
                <span class="chip idle">{t('activate.approverRemoved')}</span>
              </li>
            {/each}
          </ul>
        {:else}
          <p class="muted small">{t('activate.noApprovers')}</p>
        {/if}

        <h3 class="sub">{t('activate.changes', { base: a.base })}</h3>
        {#if a.configDiff}
          <pre class="diff" aria-label={t('activate.changes', { base: a.base })}>{#each a.configDiff.replace(/\n$/, '').split('\n') as l, i (i)}<span class={diffLine(l)}><Reveal text={l} /></span>{/each}</pre>
        {:else}
          <p class="muted small">{t('activate.noChanges')}</p>
        {/if}
        <p class="muted small mono">{t('activate.sha', { sha: (a.configSha256 ?? '').slice(0, 12) })}</p>

        {#if queuedCommit}
          <div class="queued">
            <p><Icon name="clock" size={14} />{t('activate.commitRequested', { device: queuedCommit.device, time: timeAgo(queuedCommit.time) })}</p>
            {#if queuedStale}<p class="msg err"><Icon name="alert" size={14} />{t('activate.queuedStale')}</p>{/if}
            {#if canApprove}
              <div class="row-end">
                <button class="btn" disabled={saving} onclick={() => discardQueued(queuedCommit.id)}>{t('task.discardQueued')}</button>
                <button class="btn primary" disabled={saving || queuedStale || dirty} onclick={() => confirmCommit(queuedCommit.id)}><Icon name="shield" size={14} />{t('action.confirm-queued')}</button>
              </div>
            {/if}
          </div>
        {:else if pushPending}
          <p class="notice warn"><Icon name="alert" />{t('activate.pushBase', { base: localBase || a.base })}</p>
        {:else}
          {#if dirty}<p class="notice warn"><Icon name="alert" />{t('activate.saveFirst')}</p>{/if}
          <div class="row-end">
            <button class="btn primary" disabled={!loaded || saving || dirty || !a.configSha256 || !!a.configError} onclick={commit}>
              <Icon name="shield" size={14} />{t('activate.commit')}
            </button>
          </div>
        {/if}
      </section>
    {/if}

    {#if error}<p class="msg err box"><Icon name="alert" size={14} />{error}</p>{/if}

    {#if proj?.active}
      <div class="row-end">
        <a class="btn primary" href={href({ name: 'board', project: projectId })}>{t('activate.toBoard')}</a>
      </div>
    {:else}
      <div class="row-end">
        <a class="btn" href={href({ name: 'board', project: projectId })}>{t('common.cancel')}</a>
        <button class="btn primary" disabled={!loaded || saving || (foreign && !force) || !!a.configError} onclick={activate}>
          <Icon name="check" />{t('action.activate')}
        </button>
      </div>
    {/if}
  {:else}
    <p class="muted">{t('app.loading')}</p>
  {/if}
</div>

<style>
  .activate {
    display: grid;
    gap: 14px;
    max-width: 760px;
    margin: 0 auto;
    padding: 18px 28px 40px;
  }
  .crumbs a {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: 12.5px;
    color: var(--text-3);
  }
  header {
    display: grid;
    gap: 4px;
  }
  h1 {
    font-size: 20px;
  }
  .step {
    display: grid;
    gap: 10px;
    padding: 14px 16px;
  }
  h2 {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 14px;
  }
  .num {
    display: grid;
    place-items: center;
    width: 22px;
    height: 22px;
    border-radius: 50%;
    background: var(--accent-soft);
    color: var(--accent-soft-fg);
    font-size: 12px;
  }
  .grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 14px;
    align-items: start;
  }
  .small {
    font-size: 12.5px;
  }
  .hooks {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 6px;
  }
  .hooks li {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    padding: 8px 12px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
  }
  .chip {
    font-size: 11.5px;
    font-weight: 600;
    padding: 2px 8px;
    border-radius: 10px;
  }
  .chip.ok {
    background: var(--ok-bg);
    color: var(--ok);
  }
  .chip.idle {
    background: var(--idle-bg);
    color: var(--text-2);
  }
  .chip.warn {
    background: var(--warn-bg);
    color: var(--warn);
  }
  .force {
    display: flex;
    gap: 8px;
    align-items: flex-start;
    font-size: 13px;
    padding: 10px 12px;
    border-radius: var(--radius-sm);
    background: var(--warn-bg);
  }
  .force small {
    display: block;
    color: var(--text-2);
  }
  .force input {
    margin-top: 3px;
  }
  .yaml {
    font-size: 12.5px;
    line-height: 1.55;
    tab-size: 2;
    white-space: pre;
    overflow-x: auto;
  }
  .msg {
    display: flex;
    gap: 6px;
    align-items: center;
    font-size: 13px;
  }
  .msg.ok {
    color: var(--ok);
  }
  .msg.err {
    color: var(--err);
    font-family: var(--mono);
    font-size: 12.5px;
  }
  .msg.box {
    padding: 10px 12px;
    border-radius: var(--radius-sm);
    background: var(--err-bg);
  }
  .notice {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 10px 12px;
    border-radius: var(--radius-sm);
    font-size: 13px;
  }
  .notice.warn {
    background: var(--warn-bg);
    color: var(--warn);
  }
  .sub {
    font-size: 13px;
    margin-top: 4px;
  }
  .approvers {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 6px;
  }
  .approvers li {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 10px;
    padding: 8px 12px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    font-size: 13px;
  }
  .approvers li.removed {
    opacity: 0.7;
  }
  .approvers li.removed .ap-name {
    text-decoration: line-through;
  }
  .ap-name {
    font-weight: 600;
    overflow-wrap: anywhere;
  }
  .ap-id,
  .ap-key {
    font-size: 12px;
    color: var(--text-2);
  }
  .chip.info {
    background: var(--info-bg);
    color: var(--info);
  }
  .diff {
    margin: 0;
    padding: 8px 0;
    max-height: 360px;
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    font-family: var(--mono);
    font-size: 12px;
    line-height: 1.5;
  }
  .diff span {
    display: block;
    padding: 0 12px;
    white-space: pre;
  }
  .diff .add {
    background: var(--diff-add-bg);
    color: var(--diff-add-fg);
  }
  .diff .del {
    background: var(--diff-del-bg);
    color: var(--diff-del-fg);
  }
  .diff .hunk,
  .diff .meta {
    color: var(--diff-hunk);
  }
  .queued {
    display: grid;
    gap: 8px;
    padding: 10px 12px;
    border-radius: var(--radius-sm);
    background: var(--info-bg);
    font-size: 13px;
  }
  .queued > p {
    display: flex;
    gap: 6px;
    align-items: center;
  }
  .row-end {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
  @media (max-width: 640px) {
    .activate {
      padding: 12px 12px 32px;
    }
    .grid {
      grid-template-columns: 1fr;
    }
    .row-end .btn {
      flex: 1;
      height: 38px;
    }
  }
</style>
