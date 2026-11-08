<script lang="ts">
  import Icon from './Icon.svelte'
  import Modal from './Modal.svelte'
  import { t, type Key } from '../i18n/index.svelte'
  import { cancelPending, signing, signPending } from '../stores/sign.svelte'
  import { errorText } from '../stores/actions.svelte'
  import { groupKey } from '../passkeys'

  const p = $derived(signing.pending)
  let open = $state(false)
  let chosen = $state('')

  $effect(() => {
    open = !!p
    if (p?.mode === 'choose') chosen = p.options[0]?.id ?? ''
  })

  const FIELDS = ['action', 'repo', 'task', 'plan_sha256', 'head', 'approver', 'server', 'subject', 'credential', 'time', 'nonce'] as const
  type Field = (typeof FIELDS)[number]
  const ACTIONS = ['approve-plan', 'push', 'open-pr', 'commit-config', 'push-base', 'owner', 'add-owner', 'revoke-passkey'] as const
  const isField = (k: string): k is Field => (FIELDS as readonly string[]).includes(k)

  const label = (k: string) => (isField(k) ? t(`passkey.field.${k}` as Key) : k)
  function value(k: string, v: string) {
    if (k === 'action' && (ACTIONS as readonly string[]).includes(v)) return t(`passkey.actionName.${v as (typeof ACTIONS)[number]}`)
    if (k === 'time') {
      const d = new Date(v)
      return isNaN(d.getTime()) ? v : d.toLocaleString()
    }
    if (k === 'task' && v === 'config') return t('passkey.field.taskConfig')
    return v
  }
  const order = (k: string) => (isField(k) ? FIELDS.indexOf(k) : FIELDS.length)
  const rows = $derived(p?.mode === 'sign' ? [...p.message.fields].sort((a, b) => order(a[0]) - order(b[0])) : [])

  function submit() {
    if (p?.mode === 'choose') {
      signing.pending = null
      p.resolve(chosen)
    } else void signPending()
  }

  function close() {
    if (signing.pending) cancelPending()
  }
</script>

<Modal bind:open title={p?.mode === 'choose' ? t(`passkey.choose.${p.kind}`) : t(`passkey.signTitle.${p?.kind ?? 'task'}`)} onsubmit={submit} onclose={close}>
  {#if p?.mode === 'choose'}
    <p class="muted small">{t(`passkey.choose.${p.kind}Hint`)}</p>
    <fieldset class="choose">
      <legend class="sr-only">{t(`passkey.choose.${p.kind}`)}</legend>
      {#each p.options as a (a.id)}
        <label class="opt">
          <input type="radio" name="signer" value={a.id} bind:group={chosen} />
          <span class="opt-text"><strong>{a.name}</strong>{#if a.name !== a.id}<code>{a.id}</code>{/if}</span>
        </label>
      {/each}
    </fieldset>
  {:else if p?.mode === 'sign'}
    <div class="sheet">
      {#if p.context.title}
        <p class="ctx"><strong>{p.context.title}</strong>{#if p.context.branch}<code>{p.context.branch}</code>{/if}</p>
      {/if}
      {#if p.context.push}
        {@const push = p.context.push}
        <div class="push">
          <p class="range"><span>{t('passkey.push.range')}</span><code>{push.from.slice(0, 7)} → {push.head.slice(0, 7)}</code></p>
          <ol class="commits">
            {#each push.commits as c (c.sha)}
              <li><code>{c.sha.slice(0, 7)}</code> <span>{c.subject}</span><small>{c.files.join(', ')}</small></li>
            {/each}
          </ol>
          {#if push.onlyConfig}
            <p class="verified small"><Icon name="check" size={14} />{t('passkey.push.onlyConfig')}</p>
          {:else}
            <p class="err small" role="alert"><Icon name="alert" size={14} />{t('passkey.push.otherFiles')}</p>
          {/if}
        </div>
      {/if}
      {#if p.context.key}
        <p class="keyline"><span>{t('passkey.keyFingerprint')}</span><code>{groupKey(p.context.key)}</code></p>
        <p class="muted small">{t('passkey.keyCompare')}</p>
      {/if}
      {#if p.context.elsewhere}<p class="muted small">{t('passkey.elsewhereHint', { label: p.context.elsewhere })}</p>{/if}
      {#if p.context.verified}<p class="verified small"><Icon name="check" size={14} />{t('passkey.subjectVerified')}</p>{/if}
      <p class="muted small">{t('passkey.signHint')}</p>
      <dl class="fields" aria-label={t('passkey.signedFields')}>
        {#each rows as [k, v] (k)}
          <div class:key={k === 'action' || k === 'task'}>
            <dt>{label(k)}</dt>
            <dd class:mono={k !== 'action' && k !== 'time'}>{value(k, v) || '—'}</dd>
          </div>
        {/each}
      </dl>
      {#if p.context.diff}
        <details class="raw" open>
          <summary>{t('passkey.whatChanges')}</summary>
          <pre class="diff">{#each p.context.diff.replace(/\n$/, '').split('\n') as l, i (i)}<span class={l.startsWith('+') && !l.startsWith('+++') ? 'add' : l.startsWith('-') && !l.startsWith('---') ? 'del' : ''}>{l}</span>{/each}</pre>
        </details>
      {/if}
      <details class="raw">
        <summary>{t('passkey.rawBytes')}</summary>
        <pre>{p.raw}</pre>
      </details>
      {#if p.error}<p class="err" role="alert"><Icon name="alert" size={14} />{errorText(p.error)}</p>{/if}
    </div>
  {/if}
  {#snippet footer()}
    <button type="button" class="btn" onclick={close}>{t('common.cancel')}</button>
    {#if p?.mode === 'choose'}
      <button type="submit" class="btn primary" disabled={!chosen}>{t('common.continue')}</button>
    {:else}
      <button type="submit" class="btn primary" disabled={p?.mode === 'sign' && p.busy} aria-busy={p?.mode === 'sign' && p.busy}>
        <Icon name="lock" size={14} />{p?.mode === 'sign' && p.busy ? t('passkey.signing') : t('passkey.signButton')}
      </button>
    {/if}
  {/snippet}
</Modal>

<style>
  .sheet {
    display: grid;
    gap: 10px;
  }
  .small {
    font-size: 12.5px;
  }
  .ctx {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 4px 10px;
    margin: 0;
  }
  .ctx code {
    font-size: 12px;
    color: var(--text-3);
  }
  .fields {
    display: grid;
    margin: 0;
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }
  .fields div {
    display: grid;
    grid-template-columns: minmax(96px, auto) minmax(0, 1fr);
    gap: 10px;
    padding: 7px 10px;
    align-items: baseline;
  }
  .fields div + div {
    border-top: 1px solid var(--border);
  }
  .fields .key {
    background: var(--surface-2);
  }
  dt {
    font-size: 12px;
    color: var(--text-2);
  }
  dd {
    margin: 0;
    font-size: 13px;
    overflow-wrap: anywhere;
  }
  .fields .key dd {
    font-weight: 600;
  }
  dd.mono {
    font-family: var(--mono);
    font-size: 12px;
  }
  .raw summary {
    cursor: pointer;
    font-size: 12.5px;
    color: var(--text-2);
  }
  .raw pre {
    margin: 8px 0 0;
    padding: 8px 10px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    font-family: var(--mono);
    font-size: 11.5px;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .push {
    display: grid;
    gap: 6px;
  }
  .range {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 10px;
    margin: 0;
    font-size: 13px;
    color: var(--text-2);
  }
  .range code,
  .commits code {
    font-family: var(--mono);
    font-size: 12px;
    color: var(--text);
  }
  .commits {
    margin: 0;
    padding-left: 20px;
    display: grid;
    gap: 4px;
    font-size: 13px;
  }
  .commits small {
    display: block;
    font-family: var(--mono);
    font-size: 11.5px;
    color: var(--text-3);
    overflow-wrap: anywhere;
  }
  .keyline {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 4px 10px;
    margin: 0;
    padding: 8px 10px;
    border-radius: var(--radius-sm);
    background: var(--info-bg);
    color: var(--info);
    font-size: 13px;
  }
  .keyline code {
    font-family: var(--mono);
    font-size: 16px;
    font-weight: 700;
    letter-spacing: 0.06em;
  }
  .diff span {
    display: block;
  }
  .diff .add {
    background: var(--diff-add-bg);
    color: var(--diff-add-fg);
  }
  .diff .del {
    background: var(--diff-del-bg);
    color: var(--diff-del-fg);
  }
  .verified {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 0;
    color: var(--ok);
  }
  .err {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 0;
    color: var(--err);
    font-size: 13px;
  }
  .choose {
    display: grid;
    gap: 6px;
    margin: 10px 0 0;
    padding: 0;
    border: 0;
  }
  .opt {
    display: flex;
    gap: 10px;
    align-items: center;
    padding: 8px 10px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    cursor: pointer;
  }
  .opt-text {
    display: grid;
    min-width: 0;
  }
  .opt code {
    font-size: 11.5px;
    color: var(--text-3);
    overflow-wrap: anywhere;
  }
  @media (max-width: 480px) {
    .fields div {
      grid-template-columns: minmax(0, 1fr);
      gap: 2px;
    }
  }
</style>
