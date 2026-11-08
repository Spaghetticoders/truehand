<script lang="ts">
  import Icon from '../components/Icon.svelte'
  import Modal from '../components/Modal.svelte'
  import Qr from '../components/Qr.svelte'
  import AddApproverModal from './AddApproverModal.svelte'
  import { api, ApiError, type Device, type Passkey, type PasskeyList } from '../api'
  import { t, timeAgo } from '../i18n/index.svelte'
  import { app } from '../stores/app.svelte'
  import { href } from '../stores/router.svelte'
  import { errorText, toast } from '../stores/actions.svelte'
  import { ownerHeader, passkeyHere } from '../stores/sign.svelte'
  import { forget, groupKey, known, support } from '../passkeys'

  let list = $state<PasskeyList | null>(null)
  let devices = $state<Device[]>([])
  let unavailable = $state(false)
  let mine = $state<string[]>(known())
  const sup = support()
  const here = passkeyHere()
  const host = location.hostname

  async function load() {
    try {
      list = await api.passkeys()
      unavailable = false
      devices = await api.devices().catch(() => [])
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) unavailable = true
      else toast('error', errorText(e))
    }
  }

  $effect(() => {
    void app.data
    void load()
  })

  const insecure = $derived(here && (sup === 'insecure' || !!list?.rpIdError))
  const moved = $derived(list ? list.passkeys.filter((p) => !!list!.rpId && p.rpId !== list!.rpId) : [])
  const elsewhere = $derived(!!list?.rpId && sup === 'ok' && host !== list.rpId)
  // A broken revocation list suspends every owner: one notice above says why, rows only say so.
  const suspended = $derived(!!list?.revocationsCode || !!list?.revocationsError)
  const isMine = (p: Passkey) => p.thisDevice || mine.includes(p.id)
  const deviceName = (id?: string) => devices.find((d) => d.id === id)?.name
  const validOwners = $derived(list?.passkeys.filter((p) => p.owner && p.ownerValid && !p.revoked && p.rpId === list!.rpId) ?? [])

  // A code for another device, or for this browser on its enrolment page.
  let coding = $state(false)
  let asOwner = $state(false)
  let code = $state<{ code: string; expires: number; url: string; owner: boolean } | null>(null)
  let now = $state(Date.now())
  const left = $derived(code ? Math.max(0, Math.floor((code.expires - now) / 1000)) : 0)

  $effect(() => {
    if (!coding) return
    const id = setInterval(() => (now = Date.now()), 1000)
    return () => clearInterval(id)
  })

  function openCode() {
    asOwner = !!list?.headless
    code = null
    coding = true
  }

  async function newCode() {
    try {
      const r = await api.passkeyCode(asOwner)
      const base = list?.rpId ? `https://${list.rpId}` : location.origin
      code = { code: r.code, expires: new Date(r.expires).getTime(), owner: r.owner, url: `${base}/#/enroll?code=${encodeURIComponent(r.code)}` }
      now = Date.now()
    } catch (e) {
      toast('error', errorText(e))
    }
  }

  let revoking = $state<string | null>(null)
  let busy = $state<string | null>(null)

  const inShell = !here && !!app.data?.me.canApprove
  const expired = (p: Passkey) => p.pending === 'expired'

  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text)
      toast('ok', t('passkey.copied'))
    } catch {
      toast('error', text)
    }
  }

  async function makeDurable(id: string, label: string) {
    busy = id
    try {
      await api.revokeOwner(id)
      await load()
      toast('ok', t('passkey.madeDurable', { label }))
    } catch (e) {
      toast('error', errorText(e))
    } finally {
      busy = null
    }
  }

  async function revoke(p: Passkey) {
    if (revoking !== p.id) return void (revoking = p.id)
    revoking = null
    busy = p.id
    try {
      // From another owner's passkey, the owners it confirmed stay owners; elsewhere it is a plain human act.
      // In the Mac app an owner's revocation is made durable at once (Touch ID, Keychain).
      if (inShell && p.owner) {
        await api.revokeOwner(p.id)
        forget(p.id)
        await load()
        return toast('ok', t('passkey.madeDurable', { label: p.label }))
      }
      const others = validOwners.filter((o) => o.id !== p.id)
      const header = here && others.length && !expired(p) ? await ownerHeader('revoke-passkey', p) : undefined
      const r = await api.deletePasskey(p.id, header)
      forget(p.id)
      mine = known()
      await load()
      toast('ok', r?.orphaned?.length ? t(r.orphaned.length === 1 ? 'passkey.revokedOrphans.one' : 'passkey.revokedOrphans.other', { label: p.label, n: r.orphaned.length }) : t('passkey.revoked', { label: p.label }))
    } catch (e) {
      toast('error', errorText(e))
    } finally {
      busy = null
    }
  }


  async function anchor(p: Passkey) {
    busy = p.id
    try {
      await api.anchorPasskey(p.id)
      await load()
      toast('ok', t('passkey.granted', { label: p.label }))
    } catch (e) {
      toast('error', errorText(e))
    } finally {
      busy = null
    }
  }

  async function grant(p: Passkey) {
    busy = p.id
    try {
      await api.grantPasskey(p.id, await ownerHeader('add-owner', p))
      await load()
      toast('ok', t('passkey.granted', { label: p.label }))
    } catch (e) {
      toast('error', errorText(e))
    } finally {
      busy = null
    }
  }

  let approverOpen = $state(false)
  let approverFor = $state<Passkey | null>(null)
  function openApprover(p: Passkey) {
    approverFor = p
    approverOpen = true
  }
</script>

<section class="card block" aria-labelledby="pk-title">
  <div class="label between">
    <span class="label"><Icon name="lock" /><h2 id="pk-title">{t('passkey.title')}</h2></span>
    {#if list}
      <span class="tools">
        <button class="btn sm ghost" onclick={openCode}><Icon name="link" size={14} />{t('passkey.codeButton')}</button>
        {#if here && sup === 'ok' && !elsewhere && !insecure}
          <a class="btn sm" href={href({ name: 'enroll' })}><Icon name="plus" size={14} />{t('passkey.addHere')}</a>
        {/if}
      </span>
    {/if}
  </div>
  <p class="muted small">{t('passkey.intro')}</p>

  {#if list?.revocationsError}
    <div class="notice err" role="alert">
      <Icon name="alert" size={14} />
      <span>{t(list.revocationsCode === 'mac-revocations-missing' ? 'passkey.revocationsMac' : list.revocationsCode === 'revoked-list-invalid' ? 'passkey.revocationsInvalid' : 'passkey.revocationsError')}<details><summary>{t('common.details')}</summary><code>{list.revocationsError}</code></details></span>
    </div>
  {/if}

  {#if unavailable}
    <p class="notice"><Icon name="alert" size={14} />{t('passkey.serverOld')}</p>
  {:else if insecure}
    <p class="notice"><Icon name="lock" size={14} /><span>{t('passkey.needsHttps')}</span></p>
  {:else if here && sup === 'unsupported'}
    <p class="notice"><Icon name="alert" size={14} />{t('passkey.unsupported')}</p>
  {:else if elsewhere && list}
    <p class="notice"><Icon name="globe" size={14} /><span>{t('passkey.otherHost', { host: list.rpId })} <a href="https://{list.rpId}/#/settings">https://{list.rpId}</a></span></p>
  {/if}

  {#if list?.headless && !list.hasOwner && !suspended}
    <p class="notice" role="status"><Icon name="lock" size={14} /><span>{t('passkey.noOwner')} <code>sudo truehand passkey bootstrap</code></span></p>
  {/if}

  {#if moved.length && list}
    <p class="notice err" role="status"><Icon name="alert" size={14} />{t(moved.length === 1 ? 'passkey.renamed.one' : 'passkey.renamed.other', { old: moved[0].rpId, host: list.rpId, n: moved.length })}</p>
  {/if}

  {#if list?.passkeys.length}
    <ul class="list">
      {#each list.passkeys as p (p.id)}
        {@const gone = expired(p)}
        {@const off = gone || p.revoked === 'durable'}
        <li>
          <div class="p-main">
            <span class="p-name">{p.label}</span>
            {#if isMine(p)}<span class="tag">{t('settings.thisDevice')}</span>{/if}
            {#if p.revoked === 'durable'}
              <span class="chip err">{t('passkey.revokedDurable')}</span>
            {:else if gone}
              <span class="chip err">{t('passkey.expiredChip')}</span>
            {:else if p.owner && suspended}
              <span class="chip err">{t('passkey.ownerSuspended')}</span>
            {:else if p.owner}
              <span class="chip {p.ownerValid ? 'info' : 'warn'}">{t(p.ownerValid ? 'passkey.owner' : 'passkey.ownerPending')}</span>
            {/if}
            <span class="chip {p.synced ? 'warn' : 'ok'}" title={t(p.synced ? 'passkey.syncedHint' : 'passkey.boundHint')}>{t(p.synced ? 'passkey.synced' : 'passkey.bound')}</span>
            <span class="chip idle mono">{p.alg}</span>
            {#if p.rpId !== list.rpId}<span class="chip err">{t('passkey.oldHost')}</span>{/if}
            <span class="p-path">
              {[deviceName(p.device), p.lastUsed ? t('passkey.lastUsed', { time: timeAgo(p.lastUsed) }) : t('passkey.neverUsed')].filter(Boolean).join(' · ')} · <code>{groupKey(p.key)}</code>
            </span>
            {#if gone}
              <span class="pending">{t(isMine(p) ? 'passkey.expiredHere' : 'passkey.expiredElsewhere')}</span>
            {:else if p.owner && suspended}
              <span class="pending">{t('passkey.suspendedHint')}</span>
            {:else if p.owner && !p.ownerValid && p.revoked !== 'durable'}
              <span class="pending">
                {t(p.problem ? 'passkey.problemShort' : `passkey.pending.${p.pending ?? 'owner'}`)}
                {#if p.problem}<details class="tech"><summary>{t('common.details')}</summary><code>{p.problem}</code></details>{/if}
              </span>
            {/if}
          </div>
          <div class="m-tools">
            {#if gone && isMine(p) && here}
              <a class="btn sm" href={href({ name: 'enroll' })} aria-label={t('passkey.registerAgainLabel', { label: p.label })}><Icon name="refresh" size={14} /><span class="rm">{t('passkey.registerAgain')}</span></a>
            {/if}
            {#if !off && p.pending === 'mac' && inShell}
              <button class="btn sm" disabled={busy === p.id} aria-label={t('passkey.anchorLabel', { label: p.label })} onclick={() => anchor(p)}><Icon name="check" size={14} /><span class="rm">{t('passkey.anchorHere')}</span></button>
            {/if}
            {#if !off && !suspended && p.pending === 'owner' && here && validOwners.length}
              <button class="btn sm" disabled={busy === p.id} aria-label={t('passkey.grantLabel', { label: p.label })} onclick={() => grant(p)}><Icon name="check" size={14} /><span class="rm">{t('passkey.grant')}</span></button>
            {/if}
            {#if !off && p.rpId === list.rpId}
              <button class="btn ghost sm" onclick={() => openApprover(p)} aria-label={t('passkey.approver.titleFor', { label: p.label })}>
                <Icon name="shield" size={14} /><span class="rm">{t('passkey.approver.short')}</span>
              </button>
            {/if}
            <button class="btn sm" class:ghost={revoking !== p.id} class:danger={revoking === p.id} disabled={busy === p.id} aria-label={t(off ? 'passkey.removeLabel' : 'passkey.revokeLabel', { label: p.label })} onclick={() => revoke(p)}>
              <Icon name="trash" size={14} /><span class="rm">{revoking === p.id ? t('settings.confirmRemove') : t(off ? 'settings.remove' : 'settings.revoke')}</span>
            </button>
          </div>
        </li>
      {/each}
    </ul>
  {:else if list}
    <p class="muted small">{t('passkey.none')}</p>
  {/if}
  {#if list?.passkeys.some((p) => p.synced)}<p class="muted small">{t('passkey.syncedRisk')}</p>{/if}

  {#if list?.revocations?.length}
    <h3 class="sub">{t('passkey.revocations')}</h3>
    <ul class="list">
      {#each list.revocations as r (r.id + r.time)}
        <li>
          <div class="p-main">
            <span class="p-name">{r.label || r.id.slice(0, 12)}</span>
            <span class="chip {r.revoked === 'durable' ? 'err' : 'warn'}">{t(r.revoked === 'durable' ? 'passkey.revokedDurable' : 'passkey.revokedSoft')}</span>
            <span class="p-path">{timeAgo(r.time)}{r.key ? ' · ' : ''}{#if r.key}<code>{groupKey(r.key)}</code>{/if}</span>
            {#if r.revoked === 'soft'}
              <span class="pending">{t(inShell ? 'passkey.softHintMac' : 'passkey.softHint')}</span>
              {#if !inShell}
                <span class="cmd"><code>sudo truehand passkey revoke {r.id}</code><button class="btn ghost sm" aria-label={t('passkey.copyCommand')} onclick={() => copy(`sudo truehand passkey revoke ${r.id}`)}><Icon name="copy" size={14} /></button></span>
              {/if}
            {/if}
          </div>
          {#if r.revoked === 'soft' && inShell}
            <div class="m-tools">
              <button class="btn sm" disabled={busy === r.id} onclick={() => makeDurable(r.id, r.label ?? r.id)}><Icon name="lock" size={14} />{t('passkey.makeDurable')}</button>
            </div>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</section>

<Modal bind:open={coding} title={t('passkey.codeTitle')} onsubmit={newCode}>
  <div class="mbody">
    <p class="muted small">{t('passkey.codeHint')}</p>
    <label class="check">
      <input type="checkbox" bind:checked={asOwner} />
      <span>{t('passkey.codeOwner')}<small>{t('passkey.codeOwnerHint')}</small></span>
    </label>
    {#if code && left > 0}
      <div class="code-area">
        <Qr text={code.url} label={t('passkey.qrAlt')} size={168} />
        <div class="code-col">
          <span class="k">{code.owner ? `${t('pair.code')} · ${t('passkey.owner')}` : t('pair.code')}</span>
          <span class="code mono" aria-live="polite">{code.code.replace(/^(.{4})(.{4})$/, '$1-$2')}</span>
          <span class="muted small" role="timer">{t('pair.expires', { s: left })}</span>
          <code class="url">{code.url}</code>
        </div>
      </div>
    {:else if code}
      <p class="muted small">{t('pair.expired')}</p>
    {/if}
  </div>
  {#snippet footer()}
    <button type="button" class="btn" onclick={() => (coding = false)}>{t('common.close')}</button>
    <button type="submit" class="btn primary"><Icon name={code ? 'refresh' : 'link'} size={14} />{code ? t('pair.newCode') : t('pair.generate')}</button>
  {/snippet}
</Modal>

<AddApproverModal bind:open={approverOpen} passkey={approverFor} />

<style>
  .block {
    display: grid;
    gap: 12px;
    padding: 14px 16px;
  }
  h2 {
    font-size: 14px;
  }
  .label {
    display: flex;
    align-items: center;
    gap: 9px;
    color: var(--text-2);
  }
  .label h2 {
    color: var(--text);
  }
  .between {
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 8px;
  }
  .tools {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
  }
  .small {
    font-size: 12.5px;
  }
  .notice {
    display: flex;
    gap: 8px;
    align-items: flex-start;
    margin: 0;
    padding: 8px 10px;
    border-radius: var(--radius-sm);
    background: var(--warn-bg);
    color: var(--warn);
    font-size: 12.5px;
    overflow-wrap: anywhere;
  }
  .notice.err {
    background: var(--err-bg);
    color: var(--err);
  }
  .notice details {
    margin-top: 4px;
  }
  .notice summary {
    cursor: pointer;
  }
  .notice details code {
    display: block;
    margin-top: 4px;
    font-family: var(--mono);
    font-size: 11.5px;
    overflow-wrap: anywhere;
  }
  .notice a {
    color: inherit;
    text-decoration: underline;
    text-underline-offset: 2px;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }
  .list li {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 8px 8px 12px;
  }
  .list li + li {
    border-top: 1px solid var(--border);
  }
  .p-main {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px 8px;
  }
  .p-name {
    font-weight: 600;
  }
  .p-path {
    flex-basis: 100%;
    font-size: 12px;
    color: var(--text-3);
    overflow-wrap: anywhere;
  }
  .tag {
    font-size: 11.5px;
    font-weight: 600;
    color: var(--info);
  }
  .chip {
    font-size: 11px;
    font-weight: 600;
    padding: 1px 7px;
    border-radius: 10px;
    background: var(--idle-bg);
    color: var(--idle);
  }
  .chip.ok {
    background: var(--ok-bg);
    color: var(--ok);
  }
  .chip.warn {
    background: var(--warn-bg);
    color: var(--warn);
  }
  .chip.info {
    background: var(--info-bg);
    color: var(--info);
  }
  .chip.err {
    background: var(--err-bg);
    color: var(--err);
  }
  .chip.mono {
    font-family: var(--mono);
  }
  .m-tools {
    display: flex;
    gap: 4px;
  }
  .mbody {
    display: grid;
    gap: 12px;
  }
  .sub {
    font-size: 12px;
    font-weight: 600;
    color: var(--text-3);
    margin: 4px 0 0;
  }
  .cmd {
    flex-basis: 100%;
    display: flex;
    align-items: center;
    gap: 4px;
  }
  .cmd code {
    flex: 1;
    min-width: 0;
    padding: 4px 8px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    font-family: var(--mono);
    font-size: 12px;
    overflow-wrap: anywhere;
  }
  .tech summary {
    cursor: pointer;
  }
  .tech code {
    display: block;
    margin-top: 2px;
    font-family: var(--mono);
    font-size: 11.5px;
    color: var(--text-3);
    overflow-wrap: anywhere;
  }
  .pending {
    flex-basis: 100%;
    font-size: 12px;
    color: var(--warn);
  }
  .p-path code {
    font-size: 11.5px;
  }
  .notice code {
    font-family: var(--mono);
    overflow-wrap: anywhere;
  }
  .check {
    display: flex;
    gap: 10px;
    align-items: flex-start;
    font-size: 13px;
    cursor: pointer;
  }
  .check input {
    margin-top: 3px;
  }
  .check small {
    display: block;
    font-size: 12px;
    color: var(--text-3);
  }
  .code-area {
    display: flex;
    gap: 18px;
    align-items: center;
    flex-wrap: wrap;
  }
  .code-col {
    display: grid;
    gap: 6px;
    justify-items: start;
    min-width: 0;
  }
  .k {
    font-size: 12.5px;
    color: var(--text-2);
    font-weight: 600;
  }
  .code {
    font-size: 30px;
    font-weight: 700;
    letter-spacing: 0.08em;
  }
  .url {
    font-size: 11.5px;
    color: var(--text-3);
    overflow-wrap: anywhere;
  }
  @media (max-width: 640px) {
    .rm {
      display: none;
    }
    .danger .rm {
      display: inline;
    }
    .code-area {
      justify-content: center;
    }
  }
</style>
