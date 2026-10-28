<script lang="ts">
  import qrcode from 'qrcode-generator'
  import Icon from '../components/Icon.svelte'
  import { api, ApiError } from '../api'
  import { t } from '../i18n/index.svelte'
  import { app, refresh } from '../stores/app.svelte'
  import { go, href } from '../stores/router.svelte'
  import { toast } from '../stores/actions.svelte'

  let { code: initial }: { code?: string } = $props()

  let pairing = $state<{ code: string; expires: number } | null>(null)
  let now = $state(Date.now())
  let code = $state('')
  let deviceName = $state('')
  let error = $state<string | null>(null)
  let busy = $state(false)

  const generator = $derived(app.data?.me.canApprove ?? false)
  const left = $derived(pairing ? Math.max(0, Math.floor((pairing.expires - now) / 1000)) : 0)
  const base = $derived(app.data?.settings.remote.address || location.origin)
  const link = $derived(pairing ? `${base}/#/pair?code=${pairing.code}` : '')
  const matrix = $derived.by(() => {
    if (!link) return null
    const q = qrcode(0, 'M')
    q.addData(link)
    q.make()
    const n = q.getModuleCount()
    const cells: [number, number][] = []
    for (let r = 0; r < n; r++) for (let c = 0; c < n; c++) if (q.isDark(r, c)) cells.push([c, r])
    return { n, cells }
  })

  $effect(() => {
    if (initial) code = initial
  })

  $effect(() => {
    const id = setInterval(() => (now = Date.now()), 1000)
    return () => clearInterval(id)
  })

  async function generate() {
    error = null
    try {
      const r = await api.pairCode()
      pairing = { code: r.code, expires: new Date(r.expires).getTime() }
    } catch (e) {
      error = e instanceof ApiError ? e.message : String(e)
    }
  }

  let token = $state('')

  async function login() {
    error = null
    try {
      await api.login(token.trim())
      token = ''
      await refresh()
      go({ name: 'board' })
    } catch (e) {
      error = e instanceof ApiError && e.status === 401 ? t('pair.badToken') : String(e)
    }
  }

  async function pair() {
    busy = true
    error = null
    try {
      await api.pair(code.trim(), deviceName.trim() || t('pair.defaultName'))
      toast('ok', t('pair.paired'))
      await refresh()
      go({ name: 'board' })
    } catch (e) {
      error = e instanceof ApiError && e.status === 401 ? t('pair.invalid') : e instanceof ApiError && e.status === 429 ? t('pair.tooMany') : e instanceof ApiError ? e.message : String(e)
    } finally {
      busy = false
    }
  }
</script>

<div class="pair">
  {#if !app.needsAuth}
    <nav class="crumbs"><a href={href({ name: 'settings' })}><Icon name="chevron-left" size={14} />{t('nav.settings')}</a></nav>
  {/if}
  {#if generator}
    <header>
      <h1>{t('pair.title')}</h1>
      <p class="muted">{t('pair.intro')}</p>
    </header>
    <section class="card box">
      <ol class="steps">
        <li>{t('pair.step1')} <code>{base}</code></li>
        <li>{t('pair.step2')}</li>
        <li>{t('pair.step3')}</li>
      </ol>
      {#if pairing && left > 0}
        <div class="code-area">
          {#if matrix}
            <svg class="qr" viewBox="-2 -2 {matrix.n + 4} {matrix.n + 4}" role="img" aria-label={t('pair.qrAlt')} shape-rendering="crispEdges">
              <rect x="-2" y="-2" width={matrix.n + 4} height={matrix.n + 4} class="qr-bg" />
              {#each matrix.cells as [x, y] (`${x}-${y}`)}<rect {x} {y} width="1" height="1" />{/each}
            </svg>
          {/if}
          <div class="code-col">
            <span class="k">{t('pair.code')}</span>
            <span class="code mono" aria-live="polite">{pairing.code.replace(/^(.{4})(.{4})$/, '$1-$2')}</span>
            <span class="muted small" role="timer">{t('pair.expires', { s: left })}</span>
            <button class="btn sm" onclick={generate}><Icon name="refresh" size={14} />{t('pair.newCode')}</button>
          </div>
        </div>
      {:else}
        {#if pairing}<p class="muted small">{t('pair.expired')}</p>{/if}
        <div><button class="btn primary" onclick={generate}><Icon name="link" />{t('pair.generate')}</button></div>
      {/if}
      <p class="muted small">{t('pair.security')}</p>
    </section>
  {:else}
    <header>
      <h1>{t('pair.phoneTitle')}</h1>
      <p class="muted">{t('pair.phoneIntro')}</p>
    </header>
    <form
      class="card box"
      onsubmit={(e) => {
        e.preventDefault()
        pair()
      }}
    >
      <label class="field">
        <span>{t('pair.code')}</span>
        <input class="input mono big" autocapitalize="characters" autocomplete="one-time-code" spellcheck="false" maxlength="9" bind:value={code} required />
      </label>
      <label class="field">
        <span>{t('pair.deviceName')}</span>
        <input class="input" bind:value={deviceName} placeholder={t('pair.defaultName')} />
      </label>
      <button class="btn primary" type="submit" disabled={busy || code.replace(/[-\s]/g, '').length !== 8}>{t('pair.submit')}</button>
    </form>
    <details class="token card">
      <summary>{t('pair.tokenTitle')}</summary>
      <form
        onsubmit={(e) => {
          e.preventDefault()
          login()
        }}
      >
        <p class="muted small">{t('pair.tokenHint')}</p>
        <label class="sr-only" for="token">{t('pair.token')}</label>
        <input id="token" class="input mono" type="password" autocomplete="off" bind:value={token} placeholder={t('pair.token')} />
        <button class="btn" type="submit" disabled={!token.trim()}>{t('pair.login')}</button>
      </form>
    </details>
  {/if}
  {#if error}<p class="err"><Icon name="alert" size={14} />{error}</p>{/if}
</div>

<style>
  .pair {
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
  .code-area {
    display: flex;
    gap: 20px;
    align-items: center;
    flex-wrap: wrap;
  }
  .qr {
    width: 196px;
    height: 196px;
    fill: var(--qr-fg);
    border-radius: var(--radius-sm);
  }
  .qr-bg {
    fill: var(--qr-bg);
  }
  .code-col {
    display: grid;
    gap: 6px;
    justify-items: start;
  }
  .k {
    font-size: 12.5px;
    color: var(--text-2);
    font-weight: 600;
  }
  .code {
    font-size: 34px;
    font-weight: 700;
    letter-spacing: 0.08em;
  }
  .small {
    font-size: 12.5px;
  }
  .big {
    font-size: 22px;
    letter-spacing: 0.2em;
    text-align: center;
  }
  .token {
    padding: 12px 16px;
  }
  .token summary {
    cursor: pointer;
    font-size: 13px;
    font-weight: 600;
    color: var(--text-2);
  }
  .token form {
    display: grid;
    gap: 10px;
    margin-top: 10px;
  }
  .err {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--err);
  }
  @media (max-width: 640px) {
    .pair {
      padding: 12px 12px 32px;
    }
    .code-area {
      justify-content: center;
    }
  }
</style>
