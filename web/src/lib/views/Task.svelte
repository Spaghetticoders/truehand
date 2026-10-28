<script lang="ts">
  import Icon from '../components/Icon.svelte'
  import Stepper from '../components/Stepper.svelte'
  import Dot from '../components/Dot.svelte'
  import Terminal from '../components/Terminal.svelte'
  import Markdown from '../components/Markdown.svelte'
  import InvisibleNotice from '../components/InvisibleNotice.svelte'
  import ActionButton from '../components/ActionButton.svelte'
  import Modal from '../components/Modal.svelte'
  import { api, ApiError, TASK_STATES, type FilePatch, type TaskDetail } from '../api'
  import { t, timeAgo, type Key } from '../i18n/index.svelte'
  import { app, clientName, crashed, dotOf, isLive, isOffline, isRemote, machineOf, NEEDS_HUMAN, project, queuedFor, statusKey, taskOf } from '../stores/app.svelte'
  import { discardQueued, errorText, toast } from '../stores/actions.svelte'
  import { href, TASK_TABS, type TaskTab } from '../stores/router.svelte'
  import { ui } from '../stores/ui.svelte'
  import { actorLabel, sessionLabel } from '../actors'

  let { projectId, taskId, tab, sessionId }: { projectId: string; taskId: string; tab: TaskTab; sessionId?: string } = $props()

  let detail = $state<TaskDetail | null>(null)
  let missing = $state(false)
  let menuOpen = $state(false)
  let comment = $state('')
  let toAgent = $state(false)
  let editing = $state(false)
  let edit = $state({ title: '', description: '', externalTicket: '' })
  let dropping = $state<null | 'ask' | 'force'>(null)
  let patches = $state<Record<string, FilePatch | 'loading' | 'error'>>({})
  let tabsEl = $state<HTMLDivElement>()

  // Every snapshot (after our own actions too) refetches the detail; a slow older reply must not win.
  let fetchSeq = 0
  let shownSeq = 0
  $effect(() => {
    void app.data
    const [p, id, n] = [projectId, taskId, ++fetchSeq]
    api
      .task(p, id)
      .then((d) => {
        if (n < shownSeq) return
        shownSeq = n
        detail = d
        missing = false
      })
      .catch(() => n >= shownSeq && (missing = true))
  })

  const proj = $derived(project(projectId))
  const summary = $derived(taskOf(projectId, taskId) ?? detail ?? undefined)
  const sessions = $derived(app.data?.sessions.filter((s) => s.projectId === projectId && s.taskId === taskId) ?? [])
  const activeSession = $derived(sessions.find((s) => s.id === sessionId) ?? sessions.find(isLive) ?? sessions[0])
  const liveDev = $derived(sessions.find((s) => s.role === 'dev' && isLive(s)))
  const queued = $derived(queuedFor(projectId, taskId)[0])
  const canApprove = $derived(app.data?.me.canApprove ?? false)
  const readOnly = $derived(detail?.readOnly ?? false)
  const earlier = $derived(detail ? TASK_STATES.slice(0, TASK_STATES.indexOf(detail.state)) : [])
  const nextChecks = $derived(detail?.checks.filter((c) => c.status !== 'pending') ?? [])
  const laterChecks = $derived(detail?.checks.filter((c) => c.status === 'pending') ?? [])
  const counts = $derived<Partial<Record<TaskTab, number>>>({
    description: detail?.comments.length,
    agent: sessions.length,
    proofs: detail?.proofs.length,
    diff: detail?.diff.length,
  })

  const tabHref = (tb: TaskTab) => href({ name: 'task', project: projectId, id: taskId, tab: tb })
  const label = (prefix: 'check' | 'hand', id: string) => {
    const k = `${prefix}.${id}` as Key
    const s = t(k)
    return s === k ? id : s
  }

  function tabKey(e: KeyboardEvent) {
    const i = TASK_TABS.indexOf(tab)
    const next = e.key === 'ArrowRight' ? i + 1 : e.key === 'ArrowLeft' ? i - 1 : e.key === 'Home' ? 0 : e.key === 'End' ? TASK_TABS.length - 1 : -2
    if (next === -2) return
    e.preventDefault()
    const tb = TASK_TABS[(next + TASK_TABS.length) % TASK_TABS.length]
    location.hash = tabHref(tb)
    queueMicrotask(() => tabsEl?.querySelector<HTMLElement>(`#tab-${tb}`)?.focus())
  }

  async function loadPatch(path: string) {
    if (patches[path]) return
    patches[path] = 'loading'
    try {
      patches[path] = await api.filePatch(projectId, taskId, path)
    } catch {
      patches[path] = 'error'
    }
  }

  $effect(() => {
    if (tab === 'diff' && detail) for (const f of detail.diff.filter((f) => !f.binary).slice(0, 3)) void loadPatch(f.path)
  })

  async function send() {
    if (!detail || !comment.trim()) return
    try {
      await api.addComment(projectId, taskId, comment.trim(), toAgent && liveDev ? liveDev.id : undefined)
      comment = ''
    } catch (e) {
      toast('error', errorText(e))
    }
  }

  function openEdit() {
    menuOpen = false
    if (!detail) return
    edit = { title: detail.title, description: detail.description, externalTicket: detail.externalTicket ?? '' }
    editing = true
  }

  async function doEdit() {
    try {
      await api.editTask(projectId, taskId, edit)
      editing = false
    } catch (e) {
      toast('error', errorText(e))
    }
  }

  async function dropWorktree() {
    try {
      await api.deleteWorktree(projectId, taskId, dropping === 'force')
      dropping = null
      toast('ok', t('task.worktreeDeleted'))
    } catch (e) {
      if (e instanceof ApiError && e.status === 409 && dropping === 'ask') dropping = 'force'
      else toast('error', errorText(e))
    }
  }

  async function signal(sig: 'int' | 'term') {
    if (activeSession) await api.signalSession(activeSession.id, sig).catch((e) => toast('error', errorText(e)))
  }

  async function resume() {
    if (activeSession) await api.resumeSession(activeSession.id).catch((e) => toast('error', errorText(e)))
  }
</script>

<svelte:window onclick={() => (menuOpen = false)} onkeydown={(e) => e.key === 'Escape' && (menuOpen = false)} />

{#if missing}
  <div class="missing">
    <p>{t('task.notFound')}</p>
    <a class="btn" href={href({ name: 'board' })}><Icon name="chevron-left" />{t('task.back')}</a>
  </div>
{:else if detail && summary}
  <article class="task" class:agent={tab === 'agent'}>
    <header class="top">
      <nav class="crumbs" aria-label={t('task.back')}>
        <a href={href({ name: 'board', project: projectId })}>{clientName(proj?.clientId ?? '')}</a>
        <span aria-hidden="true">/</span>
        <a href={href({ name: 'board', project: projectId })}>{proj?.name}</a>
        {#if isRemote(projectId)}<span class="machine"><Icon name="machine" size={11} />{machineOf(projectId)?.label}</span>{/if}
      </nav>
      <div class="title-row">
        <div class="title">
          <span class="id mono">{detail.id}{#if detail.externalTicket}<span class="src"> · {detail.externalTicket}</span>{/if}</span>
          <h1>{detail.title}</h1>
        </div>
        <div class="actions">
          <ActionButton task={summary} hide={tab === 'agent' ? ['open-agent', 'resume-agent'] : []} />
          {#if !readOnly}
            <div class="menu-wrap">
              <button
                class="btn icon"
                aria-label={t('task.more')}
                aria-haspopup="menu"
                aria-expanded={menuOpen}
                onclick={(e) => {
                  e.stopPropagation()
                  menuOpen = !menuOpen
                }}><Icon name="more" size={18} /></button
              >
              {#if menuOpen}
                <ul class="menu card" role="menu">
                  <li role="none">
                    <button role="menuitem" disabled={!earlier.length || detail.merged} onclick={() => ((menuOpen = false), (ui.sendBack = { pid: projectId, id: taskId }))}>
                      <Icon name="undo" />{t('task.sendBack')}
                    </button>
                  </li>
                  <li role="none">
                    <button role="menuitem" onclick={openEdit}><Icon name="edit" />{t('task.edit')}</button>
                  </li>
                  {#if detail.worktree && detail.state === 'delivered'}
                    <li role="none">
                      <button role="menuitem" onclick={() => ((menuOpen = false), (dropping = 'ask'))}><Icon name="trash" />{t('task.deleteWorktree')}</button>
                    </li>
                  {/if}
                </ul>
              {/if}
            </div>
          {/if}
        </div>
      </div>
      {#if summary.primaryAction && NEEDS_HUMAN.includes(summary.primaryAction) && !queued}
        {#if !canApprove}
          <p class="phone-hint"><Icon name="shield" size={13} />{t('action.remoteHint')}</p>
        {/if}
      {/if}
      <Stepper state={detail.state} merged={detail.merged} />
      {#if proj?.configError}
        <p class="notice err"><Icon name="alert" />{t('board.configErrorTitle')} <code>{proj.configError}</code></p>
      {:else if !proj?.active}
        <p class="notice warn"><Icon name="alert" />{t('task.inactiveNotice')}</p>
      {/if}
      {#if isOffline(projectId)}
        <p class="notice"><Icon name="alert" />{t('machine.offlineHint')}</p>
      {/if}
      {#if readOnly}
        <p class="notice"><Icon name="lock" />{t('task.readOnly', { branch: detail.branch ?? '' })}</p>
      {:else if detail.worktree}
        <p class="notice"><Icon name="branch" />{t('task.onBranch', { branch: `th/${detail.id}` })}</p>
      {/if}
      {#if queued}
        <div class="notice info">
          <Icon name="clock" />
          <span>{canApprove ? t('task.queuedMac', { device: queued.device, time: timeAgo(queued.time), what: t(`queuedKind.${queued.kind}`) }) : t('task.queuedPhone', { what: t(`queuedKind.${queued.kind}`) })}</span>
          <button class="btn sm ghost" onclick={() => discardQueued(queued.id)}>{t('task.discardQueued')}</button>
        </div>
      {/if}
      {#if detail.stale && !detail.merged}
        <p class="notice warn"><Icon name="alert" />{t('task.stale')}</p>
      {/if}
    </header>

    <!-- svelte-ignore a11y_interactive_supports_focus -->
    <div class="tabs" role="tablist" tabindex="-1" bind:this={tabsEl} onkeydown={tabKey}>
      {#each TASK_TABS as tb (tb)}
        <a role="tab" href={tabHref(tb)} aria-selected={tab === tb} tabindex={tab === tb ? 0 : -1} aria-controls="panel" id="tab-{tb}">
          {t(`task.tabs.${tb}`)}
          {#if counts[tb]}<span class="n">{counts[tb]}</span>{/if}
          {#if tb === 'proofs' && detail.proofs.some((e) => e.verdict === 'fail')}<span class="flag" aria-hidden="true"></span>{/if}
          {#if tb === 'agent' && sessions.some((s) => s.status === 'waiting')}<Dot status="waiting" size={7} />{/if}
        </a>
      {/each}
    </div>

    <div class="panel" id="panel" role="tabpanel" aria-labelledby="tab-{tab}">
      {#if tab === 'description'}
        <div class="desc">
          <div class="main">
            {#if detail.description}<Markdown class="body" source={detail.description} />{:else}<p class="muted">{t('task.noDescription')}</p>{/if}
            <h2 class="h">{t('task.comments')}</h2>
            {#if detail.comments.length}
              <ol class="comments">
                {#each detail.comments as c (c.id)}
                  <li class:question={c.openQuestion}>
                    <span class="avatar {c.actor.kind}" aria-hidden="true">
                      {#if c.actor.kind === 'human'}{c.actor.name.charAt(0)}{:else}<Icon name="terminal" size={13} />{/if}
                    </span>
                    <div class="c-body">
                      <div class="c-head">
                        <strong>{actorLabel(c.actor)}</strong>
                        <time datetime={c.time}>{timeAgo(c.time)}</time>
                        {#if c.openQuestion}<span class="q"><Icon name="question" size={12} />{t('task.openQuestion')}</span>{/if}
                      </div>
                      <Markdown source={c.body} />
                    </div>
                  </li>
                {/each}
              </ol>
            {:else}
              <p class="muted">{t('task.noComments')}</p>
            {/if}
            {#if !readOnly}
              <form
                class="composer"
                onsubmit={(e) => {
                  e.preventDefault()
                  send()
                }}
              >
                <label class="sr-only" for="composer">{t('task.composer')}</label>
                <textarea
                  id="composer"
                  class="input"
                  rows="3"
                  placeholder={t('task.composer')}
                  bind:value={comment}
                  onkeydown={(e) => (e.metaKey || e.ctrlKey) && e.key === 'Enter' && send()}
                ></textarea>
                <div class="c-foot">
                  {#if liveDev}
                    <label class="check"><input type="checkbox" bind:checked={toAgent} />{t('task.sendToAgent')}</label>
                  {:else}
                    <small class="muted">{t('task.composerHint')}</small>
                  {/if}
                  <button class="btn primary sm" type="submit" disabled={!comment.trim()}><Icon name="send" size={14} />{t('task.send')}</button>
                </div>
              </form>
            {/if}
            {#if detail.history.length}
              <details class="history">
                <summary>{t('task.history')}</summary>
                <ol>
                  {#each detail.history as h, i (i)}
                    <li>
                      <time datetime={h.time}>{timeAgo(h.time)}</time>
                      <span>{h.to ? t('task.movedTo', { who: actorLabel(h.actor), state: t(`state.${h.to}`) }) : t('task.event', { who: actorLabel(h.actor), what: h.type })}</span>
                    </li>
                  {/each}
                </ol>
              </details>
            {/if}
          </div>
          <aside class="details card" aria-label={t('task.details')}>
            <dl>
              <dt>{t('task.client')}</dt>
              <dd>{clientName(proj?.clientId ?? '')}</dd>
              <dt>{t('task.project')}</dt>
              <dd>{proj?.name}</dd>
              <dt>{t('task.branch')}</dt>
              <dd class="mono">{detail.branch ?? (detail.worktree ? `th/${detail.id}` : proj?.branch)}</dd>
              {#if detail.externalTicket}
                <dt>{t('task.ticket')}</dt>
                <dd class="mono">{detail.externalTicket}</dd>
              {/if}
              <dt>{t('task.scope')}</dt>
              <dd>
                {#if detail.scope.length}
                  {#each detail.scope as s (s)}<code>{s}</code>{/each}
                {:else}{t('task.scopeAll')}{/if}
              </dd>
              {#if detail.prUrl}
                <dt>{t('task.pr')}</dt>
                <dd><a class="link" href={detail.prUrl} target="_blank" rel="noreferrer">{detail.prUrl.replace(/^https:\/\/github.com\//, '')}</a></dd>
              {/if}
              <dt>{t('task.updated')}</dt>
              <dd>{timeAgo(detail.updatedAt)}</dd>
            </dl>
          </aside>
        </div>
      {:else if tab === 'agent'}
        {#if activeSession}
          {@const live = isLive(activeSession)}
          <div class="agent-wrap">
            <div class="agent-bar">
              <div class="subtabs" role="tablist" aria-label={t('task.sessionsLabel')}>
                {#each sessions as s (s.id)}
                  <a role="tab" aria-selected={s.id === activeSession.id} href={href({ name: 'task', project: projectId, id: taskId, tab: 'agent', session: s.id })}>
                    <Dot status={dotOf(s)} />{sessionLabel(s.role)}
                  </a>
                {/each}
              </div>
              <span class="s-status">{t(statusKey(activeSession))}{#if activeSession.permissionMode !== 'default'} · {t(`perm.${activeSession.permissionMode}`)}{/if}</span>
              <div class="s-tools">
                {#if live}
                  <button class="btn sm" onclick={() => signal('int')} title={t('session.interruptHint')}><Icon name="stop" size={13} />{t('session.interrupt')}</button>
                  <button class="btn sm ghost" onclick={() => signal('term')}>{t('session.terminate')}</button>
                {:else if activeSession.resumable}
                  <button class="btn sm primary" onclick={resume}><Icon name="play" size={13} />{t('session.resume')}</button>
                {/if}
              </div>
            </div>
            {#if crashed(activeSession)}
              <p class="notice err"><Icon name="alert" />{t('session.crashed', { code: activeSession.exitCode ?? '' })}</p>
            {/if}
            <div class="term-wrap">
              {#key `${activeSession.id}/${live}`}
                <Terminal sessionId={activeSession.id} {live} label={t('task.terminal', { who: sessionLabel(activeSession.role) })} />
              {/key}
            </div>
          </div>
        {:else}
          <div class="empty">
            <p class="muted">{t('task.noSession')}</p>
            <ActionButton task={summary} />
          </div>
        {/if}
      {:else if tab === 'plan'}
        <div class="narrow">
          {#if detail.plan}
            {#if detail.waitReason === 'approvePlan' || detail.planReady}
              <div class="callout"><Icon name="shield" /><span>{t('task.planWaiting')}</span></div>
            {/if}
            {#if detail.planProblems.length}
              <ul class="problems">
                {#each detail.planProblems as p, i (i)}<li><Icon name="alert" size={13} />{p.section ? `${p.section}: ` : ''}{p.message}</li>{/each}
              </ul>
            {/if}
            <InvisibleNotice text={detail.plan} />
            <div class="card pad"><Markdown class="body" source={detail.plan} /></div>
            {#if detail.approvals.length}
              <h2 class="h">{t('task.approvals')}</h2>
              <ul class="approvals">
                {#each detail.approvals as a, i (i)}
                  <li class:old={!a.current}><Icon name="check" size={14} />{t(a.current ? 'task.approvedBy' : 'task.approvedOld', { name: actorLabel(a.actor), time: timeAgo(a.time) })}</li>
                {/each}
              </ul>
            {/if}
          {:else}
            <p class="muted empty">{t('task.noPlan')}</p>
          {/if}
        </div>
      {:else if tab === 'proofs'}
        <div class="narrow proofs">
          {#if nextChecks.length && !detail.merged}
            <h2 class="h">{t('task.checks', { state: t(`state.${nextChecks[0].gate}`) })}</h2>
            <ul class="checks card">
              {#each nextChecks as c, i (i)}
                <li class={c.status}>
                  <span class="c-icon"><Icon name={c.status === 'pass' ? 'check' : c.status === 'fail' ? 'x' : 'chevron-right'} size={14} /></span>
                  <span class="c-label">{label('check', c.id)}</span>
                  {#if c.detail}<span class="c-detail" title={c.detail}>{c.detail}</span>{/if}
                  <span class="c-status">{t(`checkStatus.${c.status}`)}</span>
                </li>
              {/each}
            </ul>
          {/if}
          {#if laterChecks.length && !detail.merged}
            <details class="later">
              <summary>{t('task.laterChecks', { n: laterChecks.length })}</summary>
              <ul class="checks card">
                {#each laterChecks as c, i (i)}
                  <li class="pending">
                    <span class="c-icon"><Icon name="clock" size={14} /></span>
                    <span class="c-label">{label('check', c.id)}</span>
                    <span class="c-status">{t(`state.${c.gate}`)}</span>
                  </li>
                {/each}
              </ul>
            </details>
          {/if}

          <h2 class="h">{t('task.proofs')}</h2>
          {#if detail.proofs.length}
            {#each detail.proofs as p (p.id)}
              <div class="proof card" class:fail={p.verdict === 'fail'}>
                <div class="p-head">
                  <span class="verdict {p.verdict}"><Icon name={p.verdict === 'pass' ? 'check' : 'x'} size={14} />{t(`verdict.${p.verdict}`)}</span>
                  <strong>{label('hand', p.title)}</strong>
                  <span class="muted small">{actorLabel(p.actor)} · {timeAgo(p.time)}</span>
                </div>
                <p>{p.summary}</p>
                {#if !p.intact}
                  <p class="note err"><Icon name="alert" size={13} />{t('task.proofTampered')}</p>
                {:else if !p.fresh && detail.merged}
                  <p class="note"><Icon name="clock" size={13} />{t('task.proofHistory', { time: timeAgo(p.time) })}</p>
                {:else if !p.fresh}
                  <p class="note warn"><Icon name="alert" size={13} />{t('task.proofOutdated')}</p>
                {/if}
                {#if p.images.length}
                  <div class="shots">
                    {#each p.images as img (img.url)}
                      <figure>
                        <a href={img.url} target="_blank" rel="noreferrer"><img src={img.url} alt={img.caption} loading="lazy" /></a>
                        <figcaption class="mono">{img.caption}</figcaption>
                      </figure>
                    {/each}
                  </div>
                {/if}
                {#if p.log}
                  <details>
                    <summary>{t('task.log')}</summary>
                    <pre>{p.log}</pre>
                  </details>
                {/if}
              </div>
            {/each}
          {:else}
            <p class="muted">{t('task.noProofs')}</p>
          {/if}
        </div>
      {:else if tab === 'diff'}
        <div class="narrow">
          {#if detail.diff.length}
            <p class="muted small">{t('task.diffSummary', { files: detail.diff.length, added: detail.diff.reduce((a, f) => a + f.added, 0), removed: detail.diff.reduce((a, f) => a + f.removed, 0) })}</p>
            {#each detail.diff as f (f.path)}
              {@const p = patches[f.path]}
              <details class="file card" class:out={!f.inScope} open={!!p && p !== 'error'} ontoggle={(e) => e.currentTarget.open && loadPatch(f.path)}>
                <summary class="f-head">
                  <span class="mono path">{f.path}</span>
                  {#if !f.inScope}<span class="out-tag">{t('task.outOfScope')}</span>{/if}
                  <span class="plus">+{f.added}</span>
                  <span class="minus">−{f.removed}</span>
                </summary>
                {#if f.binary}
                  <p class="muted small pad-s">{t('task.binary')}</p>
                {:else if p === 'loading' || !p}
                  <p class="muted small pad-s">{t('app.loading')}</p>
                {:else if p === 'error'}
                  <p class="muted small pad-s">{t('task.patchError')}</p>
                {:else}
                  <pre class="patch">{#each p.patch.split('\n') as line, i (i)}<span class:add={line.startsWith('+')} class:del={line.startsWith('-')} class:hunk={line.startsWith('@@')}>{line}{'\n'}</span>{/each}</pre>
                  {#if p.truncated}<p class="muted small pad-s">{t('task.diffTruncated')}</p>{/if}
                {/if}
              </details>
            {/each}
          {:else}
            <p class="muted empty">{t('task.noDiff')}</p>
          {/if}
        </div>
      {/if}
    </div>
  </article>

  <Modal bind:open={editing} title={t('task.editTitle')} onsubmit={doEdit}>
    <label class="field">
      <span>{t('newTask.name')}</span>
      <input class="input" bind:value={edit.title} required />
    </label>
    <label class="field">
      <span>{t('newTask.description')}</span>
      <textarea class="input" rows="8" bind:value={edit.description}></textarea>
    </label>
    <label class="field">
      <span>{t('newTask.ticket')}</span>
      <input class="input mono" bind:value={edit.externalTicket} />
    </label>
    {#snippet footer()}
      <button type="button" class="btn" onclick={() => (editing = false)}>{t('common.cancel')}</button>
      <button type="submit" class="btn primary" disabled={!edit.title.trim()}>{t('common.save')}</button>
    {/snippet}
  </Modal>

  <Modal open={dropping !== null} title={t('task.deleteWorktree')} onsubmit={dropWorktree} onclose={() => (dropping = null)}>
    <p>{t(dropping === 'force' ? 'task.deleteWorktreeForce' : 'task.deleteWorktreeHint', { path: detail.worktree ?? '' })}</p>
    {#snippet footer()}
      <button type="button" class="btn" onclick={() => (dropping = null)}>{t('common.cancel')}</button>
      <button type="submit" class="btn danger">{t(dropping === 'force' ? 'task.deleteAnyway' : 'task.deleteWorktree')}</button>
    {/snippet}
  </Modal>
{:else}
  <p class="loading muted">{t('app.loading')}</p>
{/if}

<style>
  .crumbs {
    align-items: center;
  }
  .machine {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    margin-left: 6px;
    padding: 0 6px;
    border-radius: 8px;
    border: 1px solid var(--border-strong);
    font-size: 11px;
    font-weight: 600;
    color: var(--text-2);
  }
  .notice code {
    font-size: 12px;
    overflow-wrap: anywhere;
  }
  .history summary,
  .later summary {
    cursor: pointer;
    font-size: 12.5px;
    font-weight: 600;
    color: var(--text-2);
  }
  .history ol {
    list-style: none;
    margin: 8px 0 0;
    padding: 0;
    display: grid;
    gap: 4px;
    font-size: 12.5px;
    color: var(--text-2);
  }
  .history li {
    display: flex;
    gap: 10px;
  }
  .history time {
    min-width: 72px;
    color: var(--text-3);
  }
  .later .checks {
    margin-top: 8px;
  }
  .approvals li.old {
    color: var(--text-3);
  }
  .file > summary {
    display: flex;
    align-items: center;
    gap: 10px;
    cursor: pointer;
    list-style: none;
  }
  .file > summary::-webkit-details-marker {
    display: none;
  }
  .file .path {
    flex: 1;
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .file.out > summary {
    background: var(--warn-bg);
  }
  .file:not([open]) > summary {
    border-bottom: 0;
  }
  .pad-s {
    padding: 10px 12px;
  }
  .task {
    display: flex;
    flex-direction: column;
    min-height: 100%;
    max-width: 1080px;
    margin: 0 auto;
    padding: 18px 28px 32px;
    gap: 14px;
  }
  .task.agent {
    height: 100%;
    padding-bottom: 16px;
  }
  .top {
    display: grid;
    gap: 12px;
  }
  .crumbs {
    display: flex;
    gap: 6px;
    font-size: 12.5px;
    color: var(--text-3);
  }
  .crumbs a:hover {
    color: var(--text);
  }
  .title-row {
    display: flex;
    align-items: flex-start;
    gap: 16px;
  }
  .title {
    flex: 1;
    min-width: 0;
    display: grid;
    gap: 2px;
  }
  .id {
    font-size: 12.5px;
    color: var(--text-2);
  }
  .src {
    color: var(--text-3);
  }
  h1 {
    font-size: 21px;
    line-height: 1.25;
  }
  .actions {
    display: flex;
    gap: 8px;
    align-items: center;
    flex: none;
  }
  .phone-hint :global(svg) {
    flex: none;
  }
  .phone-hint {
    display: flex;
    align-items: center;
    gap: 6px;
    margin-top: -6px;
    font-size: 12px;
    color: var(--text-3);
  }
  .menu-wrap {
    position: relative;
  }
  .menu {
    position: absolute;
    right: 0;
    top: 36px;
    z-index: 10;
    min-width: 190px;
    list-style: none;
    margin: 0;
    padding: 4px;
    box-shadow: var(--shadow);
  }
  .menu button {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    height: 32px;
    padding: 0 10px;
    border: 0;
    border-radius: var(--radius-sm);
    background: none;
    text-align: left;
    cursor: pointer;
  }
  .menu button:hover:not(:disabled) {
    background: var(--surface-hover);
  }
  .menu button:disabled {
    color: var(--text-3);
    cursor: default;
  }
  .notice {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    color: var(--text-2);
    font-size: 13px;
  }
  .notice span {
    flex: 1;
  }
  .notice.warn {
    background: var(--warn-bg);
    color: var(--warn);
  }
  .notice.info {
    background: var(--info-bg);
    color: var(--info);
  }
  .notice.err {
    background: var(--err-bg);
    color: var(--err);
  }
  .tabs {
    display: flex;
    gap: 2px;
    border-bottom: 1px solid var(--border);
    overflow-x: auto;
    scrollbar-width: none;
  }
  .tabs a {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 8px 12px 9px;
    font-weight: 500;
    color: var(--text-2);
    border-bottom: 2px solid transparent;
    margin-bottom: -1px;
    white-space: nowrap;
  }
  .tabs a:hover {
    color: var(--text);
  }
  .tabs a[aria-selected='true'] {
    color: var(--text);
    border-bottom-color: var(--accent);
  }
  .tabs .n {
    font-size: 11.5px;
    color: var(--text-3);
    background: var(--surface-2);
    border-radius: 8px;
    padding: 0 6px;
  }
  .flag {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--err);
  }
  .panel {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
  }
  .desc {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 260px;
    gap: 28px;
    align-items: start;
  }
  .main {
    display: grid;
    gap: 14px;
  }
  .main :global(.body) {
    font-size: 14.5px;
    line-height: 1.6;
  }
  .h {
    font-size: 13px;
    font-weight: 600;
    color: var(--text-2);
    margin-top: 8px;
  }
  .comments {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 14px;
  }
  .comments li {
    display: flex;
    gap: 10px;
  }
  .comments li.question .c-body {
    border-color: var(--warn);
    background: var(--warn-bg);
  }
  .avatar {
    flex: none;
    display: grid;
    place-items: center;
    width: 26px;
    height: 26px;
    border-radius: 50%;
    font-size: 12px;
    font-weight: 700;
    background: var(--accent);
    color: var(--accent-fg);
  }
  .avatar.agent,
  .avatar.agent_unbound,
  .avatar.unknown {
    background: var(--brand-navy);
    color: var(--brand-cyan);
  }
  .avatar.review {
    background: var(--info-bg);
    color: var(--info);
  }
  .c-body {
    flex: 1;
    min-width: 0;
    padding: 9px 12px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--surface);
  }
  .c-head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px 8px;
    font-size: 12.5px;
    margin-bottom: 4px;
  }
  .c-head time {
    color: var(--text-3);
  }
  .q {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    color: var(--warn);
    font-weight: 600;
  }
  .composer {
    display: grid;
    gap: 6px;
  }
  .c-foot {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
  }
  .check {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
    color: var(--text-2);
  }
  .details {
    padding: 14px 16px;
    position: sticky;
    top: 16px;
  }
  dl {
    display: grid;
    gap: 3px;
    margin: 0;
    font-size: 13px;
  }
  dt {
    color: var(--text-3);
    font-size: 12px;
    margin-top: 8px;
  }
  dt:first-child {
    margin-top: 0;
  }
  dd {
    margin: 0;
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    overflow-wrap: anywhere;
  }
  dd code {
    background: var(--surface-2);
    padding: 1px 6px;
    border-radius: 4px;
  }
  .link {
    color: var(--info);
    text-decoration: underline;
    text-underline-offset: 2px;
  }
  .agent-wrap {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .agent-bar {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
  }
  .subtabs {
    display: flex;
    gap: 6px;
    overflow-x: auto;
    scrollbar-width: none;
  }
  .subtabs a {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    height: 28px;
    padding: 0 11px;
    border-radius: 14px;
    border: 1px solid var(--border);
    background: var(--surface);
    font-size: 12.5px;
    font-weight: 500;
    color: var(--text-2);
    white-space: nowrap;
  }
  .subtabs a[aria-selected='true'] {
    border-color: var(--accent);
    color: var(--text);
    box-shadow: inset 0 0 0 1px var(--accent);
  }
  .s-status {
    font-size: 12.5px;
    color: var(--text-3);
  }
  .s-tools {
    margin-left: auto;
    display: flex;
    gap: 6px;
  }
  .term-wrap {
    flex: 1;
    min-height: 360px;
  }
  .empty {
    display: grid;
    justify-items: start;
    gap: 12px;
    padding: 24px 0;
  }
  .narrow {
    display: grid;
    gap: 12px;
    max-width: 820px;
  }
  .pad {
    padding: 18px 20px;
  }
  .callout {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 10px 12px;
    border-radius: var(--radius);
    background: var(--info-bg);
    color: var(--info);
    font-weight: 600;
    font-size: 13px;
  }
  .problems,
  .approvals {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 6px;
    font-size: 13px;
  }
  .problems li {
    display: flex;
    gap: 6px;
    align-items: center;
    color: var(--warn);
  }
  .approvals li {
    display: flex;
    gap: 6px;
    align-items: center;
    color: var(--text-2);
  }
  .approvals :global(svg) {
    color: var(--ok);
  }
  .checks {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .checks li {
    display: grid;
    grid-template-columns: 22px minmax(0, 1fr) auto;
    align-items: center;
    gap: 4px 10px;
    padding: 10px 14px;
    border-bottom: 1px solid var(--border);
  }
  .checks li:last-child {
    border-bottom: 0;
  }
  .c-icon {
    display: grid;
    place-items: center;
    width: 22px;
    height: 22px;
    border-radius: 50%;
    background: var(--idle-bg);
    color: var(--text-3);
  }
  .pass .c-icon {
    background: var(--ok-bg);
    color: var(--ok);
  }
  .fail .c-icon {
    background: var(--warn-bg);
    color: var(--warn);
  }
  .c-label {
    font-weight: 500;
  }
  .c-detail {
    grid-column: 2 / -1;
    grid-row: 2;
    font-size: 12px;
    color: var(--text-3);
    font-family: var(--mono);
    overflow-wrap: anywhere;
  }
  .c-status {
    font-size: 12px;
    color: var(--text-3);
    min-width: 76px;
    text-align: right;
  }
  .proof {
    display: grid;
    gap: 8px;
    padding: 14px 16px;
  }
  .proof.fail {
    border-color: var(--err);
  }
  .p-head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 10px;
  }
  .verdict {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    height: 20px;
    padding: 0 7px;
    border-radius: 10px;
    font-size: 11.5px;
    font-weight: 600;
  }
  .verdict.pass {
    background: var(--ok-bg);
    color: var(--ok);
  }
  .verdict.fail {
    background: var(--err-bg);
    color: var(--err);
  }
  .small {
    font-size: 12px;
  }
  .note {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12.5px;
    color: var(--text-3);
  }
  .note.warn {
    color: var(--warn);
  }
  .note.err {
    color: var(--err);
  }
  .shots {
    display: flex;
    gap: 12px;
    flex-wrap: wrap;
  }
  figure {
    margin: 0;
    display: grid;
    gap: 4px;
  }
  figure img {
    display: block;
    max-height: 220px;
    max-width: 100%;
    border-radius: var(--radius-sm);
    border: 1px solid var(--border);
  }
  figcaption {
    font-size: 11.5px;
    color: var(--text-3);
  }
  .out-tag {
    font-size: 11.5px;
    font-weight: 600;
    color: var(--warn);
    white-space: nowrap;
  }
  .file {
    overflow: hidden;
  }
  .f-head {
    padding: 8px 12px;
    border-bottom: 1px solid var(--border);
    font-size: 12.5px;
    background: var(--surface-2);
    overflow-wrap: anywhere;
  }
  .plus {
    color: var(--ok);
  }
  .minus {
    color: var(--err);
  }
  .patch {
    margin: 0;
    padding: 8px 0;
    overflow-x: auto;
    line-height: 1.55;
  }
  .patch span {
    display: block;
    padding: 0 12px;
    min-width: max-content;
  }
  .patch .add {
    background: var(--diff-add-bg);
    color: var(--diff-add-fg);
  }
  .patch .del {
    background: var(--diff-del-bg);
    color: var(--diff-del-fg);
  }
  .patch .hunk {
    color: var(--diff-hunk);
  }
  .missing,
  .loading {
    padding: 40px 28px;
    display: grid;
    justify-items: start;
    gap: 12px;
  }

  @media (max-width: 900px) {
    .desc {
      grid-template-columns: 1fr;
    }
    .details {
      position: static;
    }
    .details dl {
      grid-template-columns: auto 1fr;
      column-gap: 14px;
      row-gap: 6px;
    }
    .details dt {
      margin: 0;
    }
  }

  @media (max-width: 640px) {
    .task {
      padding: 12px 12px 24px;
      gap: 12px;
    }
    .task.agent {
      padding-bottom: 12px;
    }
    .title-row {
      flex-direction: column;
      gap: 10px;
    }
    h1 {
      font-size: 18px;
    }
    .actions {
      width: 100%;
      justify-content: flex-end;
    }
    .actions :global(.btn.primary),
    .actions :global(.btn:not(.icon)) {
      flex: 1;
      height: 38px;
    }
    .top {
      position: relative;
    }
    .crumbs {
      padding-right: 48px;
    }
    .menu-wrap {
      position: absolute;
      top: -6px;
      right: 0;
    }
    .actions:not(:has(> :not(.menu-wrap))) {
      height: 0;
      margin-top: -10px;
    }
    .tabs {
      margin: 0 -12px;
      padding: 0 8px;
      justify-content: space-between;
    }
    .tabs a {
      flex: 1 1 auto;
      justify-content: center;
      padding: 8px 4px 9px;
      font-size: 13px;
    }
    .tabs .n {
      display: none;
    }
    .term-wrap {
      min-height: 0;
    }
    .s-tools {
      margin-left: 0;
    }
    .c-status {
      min-width: 0;
    }
    .pad {
      padding: 14px;
    }
    .c-foot {
      flex-wrap: wrap;
    }
  }
</style>
