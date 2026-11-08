<script lang="ts">
  import Icon from '../components/Icon.svelte'
  import AddApproverModal from './AddApproverModal.svelte'
  import { api, ApiError, type PasskeyOptions } from '../api'
  import { t } from '../i18n/index.svelte'
  import { app, refresh } from '../stores/app.svelte'
  import { go, href } from '../stores/router.svelte'
  import { errorText } from '../stores/actions.svelte'
  import { defaultLabel, enrolled, finish, prepare } from '../stores/enroll.svelte'
  import { groupKey, PasskeyError, support } from '../passkeys'

  let { code: initial }: { code?: string } = $props()

  const sup = support()
  let code = $state('')
  let label = $state(defaultLabel())
  let opts = $state<PasskeyOptions | null>(null)
  let busy = $state(false)
  let error = $state<string | null>(null)
  let approverOpen = $state(false)

  $effect(() => {
    if (initial) code = initial
  })

  const clean = $derived(code.replace(/[-\s]/g, '').toUpperCase())

  // An anchored owner turns valid once `passkey bootstrap` sees the registration and closes.
  const created = $derived(enrolled.passkey)

  async function settle(id: string) {
    for (let i = 0; i < 30 && enrolled.passkey?.owner && !enrolled.passkey.ownerValid; i++) {
      await new Promise((r) => setTimeout(r, 2000))
      const p = (await api.passkeys().catch(() => null))?.passkeys.find((x) => x.id === id)
      if (p) enrolled.passkey = p
    }
  }

  async function step() {
    busy = true
    error = null
    try {
      if (!opts) opts = await prepare(label.trim(), clean)
      else {
        const pk = await finish(opts)
        await refresh()
        void settle(pk.id)
      }
    } catch (e) {
      error = e instanceof ApiError && e.status === 401 ? t('pair.invalid') : e instanceof ApiError && e.status === 429 ? t('pair.tooMany') : errorText(e)
      if (!(e instanceof PasskeyError && (e.code === 'cancelled' || e.code === 'timeout'))) opts = null
    } finally {
      busy = false
    }
  }
</script>

<div class="enroll">
  {#if !app.needsAuth}
    <nav class="crumbs"><a href={href({ name: 'settings' })}><Icon name="chevron-left" size={14} />{t('nav.settings')}</a></nav>
  {/if}
  <header>
    <h1>{t('enroll.title')}</h1>
    <p class="muted">{t('enroll.intro')}</p>
  </header>

  {#if sup === 'insecure'}
    <p class="notice" role="alert"><Icon name="lock" size={16} /><span>{t('passkey.needsHttps')}</span></p>
  {:else if sup === 'unsupported'}
    <p class="notice" role="alert"><Icon name="alert" size={16} /><span>{t('passkey.unsupported')}</span></p>
  {/if}

  {#if created}
    <section class="card box" aria-live="polite">
      <p class="ok-msg"><Icon name="check" size={18} />{t('enroll.done', { label: created.label })}</p>
      <p class="keyline"><span>{t(enrolled.fingerprint ? 'passkey.keyFingerprint' : 'enroll.keyFromServer')}</span><code>{groupKey(enrolled.fingerprint ?? created.key)}</code></p>
      {#if enrolled.fingerprint && enrolled.fingerprint !== created.key}
        <p class="notice err" role="alert"><Icon name="alert" size={16} /><span>{t('enroll.keyMismatch', { mine: groupKey(enrolled.fingerprint), server: groupKey(created.key) })}</span></p>
      {:else if !enrolled.fingerprint}
        <p class="muted small">{t('enroll.keyFromServerHint')}</p>
      {:else}
        <p class="muted small">{t('enroll.keyHint')}</p>
      {/if}
      {#if created.owner && !created.ownerValid}
        <p class="notice" role="status"><Icon name="clock" size={16} /><span>{t(`passkey.pending.${created.pending ?? 'owner'}`)}</span></p>
      {:else if created.owner}
        <p class="muted small">{t('enroll.owner')}</p>
      {/if}
      <p class="muted small">{t('passkey.approver.offer')}</p>
      <div class="row-end">
        <button class="btn" onclick={() => ((enrolled.passkey = null), go({ name: 'board' }))}>{t('enroll.toBoard')}</button>
        <button class="btn primary" disabled={!app.data} onclick={() => (approverOpen = true)}><Icon name="shield" size={14} />{t('passkey.approver.short')}</button>
      </div>
    </section>
  {:else}
    <form
      class="card box"
      onsubmit={(e) => {
        e.preventDefault()
        void step()
      }}
    >
      <ol class="steps small">
        <li>{t('enroll.step1')} <code>sudo truehand passkey bootstrap</code></li>
        <li>{t('enroll.step2')}</li>
        <li>{t('enroll.step3')}</li>
      </ol>
      <label class="field">
        <span>{t('pair.code')}</span>
        <input class="input mono big" autocapitalize="characters" autocomplete="one-time-code" spellcheck="false" maxlength="19" bind:value={code} disabled={!!opts} required />
      </label>
      <label class="field">
        <span>{t('passkey.label')}</span>
        <input class="input" bind:value={label} disabled={!!opts} required maxlength="64" />
        <small>{t('passkey.labelHint')}</small>
      </label>
      {#if opts}<p class="muted small" role="status">{t('passkey.readyHint')}</p>{/if}
      <button class="btn primary" type="submit" disabled={busy || sup !== 'ok' || (clean.length !== 8 && clean.length !== 16) || !label.trim()}>
        <Icon name={opts ? 'lock' : 'chevron-right'} size={14} />{opts ? t('passkey.create') : t('common.continue')}
      </button>
    </form>
  {/if}
  {#if error}<p class="err" role="alert"><Icon name="alert" size={14} />{error}</p>{/if}
  {#if app.needsAuth}
    <p class="muted small"><a href={href({ name: 'pair' })}>{t('enroll.toPair')}</a></p>
  {/if}
</div>

<AddApproverModal bind:open={approverOpen} passkey={created} />

<style>
  .enroll {
    display: grid;
    gap: 14px;
    max-width: 620px;
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
  .box {
    display: grid;
    gap: 14px;
    padding: 16px;
  }
  .steps {
    margin: 0;
    padding-left: 20px;
    display: grid;
    gap: 6px;
  }
  .steps code {
    overflow-wrap: anywhere;
    background: var(--surface-2);
    padding: 1px 6px;
    border-radius: 4px;
  }
  .small {
    font-size: 12.5px;
  }
  .big {
    font-size: 22px;
    letter-spacing: 0.2em;
    text-align: center;
  }
  .notice {
    display: flex;
    gap: 8px;
    align-items: flex-start;
    margin: 0;
    padding: 10px 12px;
    border-radius: var(--radius-sm);
    background: var(--warn-bg);
    color: var(--warn);
    font-size: 13px;
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
    font-size: 18px;
    font-weight: 700;
    letter-spacing: 0.06em;
  }
  .notice.err {
    background: var(--err-bg);
    color: var(--err);
  }
  .ok-msg {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    color: var(--ok);
    font-weight: 600;
  }
  .row-end {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    flex-wrap: wrap;
  }
  .err {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--err);
  }
  a {
    color: var(--text-2);
    text-decoration: underline;
    text-underline-offset: 2px;
  }
  .crumbs a {
    text-decoration: none;
  }
  @media (max-width: 640px) {
    .enroll {
      padding: 12px 12px 32px;
    }
  }
</style>
