<script lang="ts">
  import Icon from '../components/Icon.svelte'
  import Modal from '../components/Modal.svelte'
  import { api, PERMISSION_MODES, type Device, type Machine, type PermissionMode, type RemoteMode, type Settings, type SettingsPatch } from '../api'
  import { t, timeAgo, type Key } from '../i18n/index.svelte'
  import { app, applyTheme, clientName, saveSettings, theme, type Theme } from '../stores/app.svelte'
  import { href } from '../stores/router.svelte'
  import { errorText, toast } from '../stores/actions.svelte'
  import { ui } from '../stores/ui.svelte'

  const s = $derived(app.data?.settings)
  const mac = $derived(app.data?.me.canApprove ?? false)
  const machines = $derived(app.data?.machines ?? [])
  let devices = $state<Device[]>([])
  let removing = $state<string | null>(null)
  let addingMachine = $state(false)
  let sshTarget = $state('')
  let machineLabel = $state('')
  let rotate = $state(false)

  $effect(() => {
    void app.data?.settings.remote.devices.length
    void api.devices().then((d) => (devices = d)).catch(() => (devices = []))
  })

  const LANGS: [Settings['language'], Key][] = [
    ['it', 'settings.langIt'],
    ['en', 'settings.langEn'],
  ]
  const THEMES: Theme[] = ['system', 'light', 'dark']
  const REMOTE: (RemoteMode | 'off')[] = ['off', 'tailnet', 'tailscale-serve']
  const remoteMode = $derived<RemoteMode | 'off'>(s && s.remote.enabled ? s.remote.mode : 'off')

  async function save(patch: SettingsPatch) {
    try {
      await saveSettings(patch)
    } catch (e) {
      toast('error', errorText(e))
    }
  }

  const setRemote = (m: RemoteMode | 'off') => save({ remote: m === 'off' ? { enabled: false } : { enabled: true, mode: m } })

  let address = $state('')
  let addressFor = ''
  $effect(() => {
    const a = s?.remote.address ?? ''
    if (a !== addressFor) address = addressFor = a
  })
  const remoteStatus = $derived(s?.remote.status ?? 'off')

  async function revoke(d: Device) {
    try {
      await api.deleteDevice(d.id)
      // This phone is no longer paired: only pairing makes sense now.
      if (d.current) return void (app.needsAuth = true)
      devices = await api.devices()
    } catch (e) {
      toast('error', errorText(e))
    }
  }

  async function removeProject(pid: string) {
    if (removing !== pid) return void (removing = pid)
    removing = null
    try {
      await api.removeProject(pid)
    } catch (e) {
      toast('error', errorText(e))
    }
  }

  async function machineAction(fn: () => Promise<unknown>) {
    try {
      await fn()
    } catch (e) {
      toast('error', errorText(e))
    }
  }

  async function addMachine() {
    if (!sshTarget.trim()) return
    await machineAction(() => api.addMachine(sshTarget.trim(), machineLabel.trim() || undefined))
    addingMachine = false
    sshTarget = machineLabel = ''
  }

  async function doRotate() {
    rotate = false
    await machineAction(() => api.rotateToken())
    toast('ok', t('settings.rotated'))
  }

  const projectsOn = (m: Machine) => app.data?.projects.filter((p) => (p.machineId ?? 'local') === m.id).length ?? 0
</script>

{#if s}
  <div class="settings">
    <h1>{t('settings.title')}</h1>

    <section class="card">
      <div class="row">
        <div class="label"><Icon name="globe" /><h2 id="lang">{t('settings.language')}</h2></div>
        <div class="seg" role="radiogroup" aria-labelledby="lang">
          {#each LANGS as [code, key] (code)}
            <button role="radio" aria-checked={s.language === code} onclick={() => save({ language: code })}>{t(key)}</button>
          {/each}
        </div>
      </div>
      <div class="row">
        <div class="label">
          <Icon name="sun" />
          <div>
            <h2 id="theme">{t('settings.theme')}</h2>
            <small class="muted">{t('settings.themeHint')}</small>
          </div>
        </div>
        <div class="seg" role="radiogroup" aria-labelledby="theme">
          {#each THEMES as v (v)}
            <button role="radio" aria-checked={theme.value === v} onclick={() => applyTheme(v)}>{t(`settings.theme_${v}`)}</button>
          {/each}
        </div>
      </div>
      <div class="row">
        <div class="label">
          <Icon name="shield" />
          <div>
            <h2 id="perm">{t('settings.permission')}</h2>
            <small class="muted">{t('settings.permissionHint')}</small>
          </div>
        </div>
        <select class="input narrow" aria-labelledby="perm" value={s.defaultPermissionMode} onchange={(e) => save({ defaultPermissionMode: e.currentTarget.value as PermissionMode })}>
          {#each PERMISSION_MODES as m (m)}<option value={m}>{t(`perm.${m}`)}</option>{/each}
        </select>
      </div>
    </section>

    <section class="card block">
      <div class="label between">
        <span class="label"><Icon name="phone" /><h2 id="remote">{t('settings.remote')}</h2></span>
        {#if mac}<a class="btn sm" href={href({ name: 'pair' })}><Icon name="link" size={14} />{t('settings.pairPhone')}</a>{/if}
      </div>
      <div class="seg wide" role="radiogroup" aria-labelledby="remote">
        {#each REMOTE as m (m)}
          <button role="radio" aria-checked={remoteMode === m} disabled={!mac} onclick={() => setRemote(m)}>{t(`remote.${m}`)}</button>
        {/each}
      </div>
      <p class="muted small">{t(`remote.${remoteMode}Hint`)}{#if !mac}{' '}{t('settings.macOnly')}{/if}</p>
      {#if remoteMode === 'tailnet' && mac}
        <form class="addr" onsubmit={(e) => (e.preventDefault(), save({ remote: { enabled: true, mode: 'tailnet', address: address.trim() } }))}>
          <label class="field">
            <span>{t('settings.addressOptional')}</span>
            <input class="input mono" bind:value={address} placeholder={t('settings.addressAuto')} autocomplete="off" spellcheck="false" />
          </label>
          <button class="btn sm" type="submit" disabled={address.trim() === (s.remote.address ?? '')}>{t('common.save')}</button>
        </form>
      {/if}
      {#if s.remote.enabled || remoteStatus !== 'off'}
        <p class="r-status {remoteStatus}" role="status">
          <Icon name={remoteStatus === 'listening' ? 'check' : remoteStatus === 'error' ? 'alert' : 'clock'} size={14} />
          {#if remoteStatus === 'listening'}
            {s.remote.address ? t('remote.listening', { address: s.remote.address }) : t('remote.listeningNoAddress')}
          {:else if remoteStatus === 'error'}
            {t('remote.error', { error: s.remote.error ?? '' })}
          {:else}
            {t('remote.notRunning')}
          {/if}
        </p>
      {/if}
      <h3 class="sub">{t('settings.devices')}</h3>
      {#if devices.length}
        <ul class="list">
          {#each devices as d (d.id)}
            <li>
              <div class="p-main">
                <span class="p-name">{d.name}</span>
                {#if d.current}<span class="tag">{t('settings.thisDevice')}</span>{/if}
                <span class="p-path">{t('settings.lastSeen', { time: timeAgo(d.lastSeen) })}</span>
              </div>
              {#if mac || d.current}
                <button class="btn ghost sm" aria-label={t('settings.revokeDevice', { name: d.name })} onclick={() => revoke(d)}>
                  <Icon name="x" size={14} /><span class="rm">{t('settings.revoke')}</span>
                </button>
              {/if}
            </li>
          {/each}
        </ul>
      {:else}
        <p class="muted small">{t('settings.noDevices')}</p>
      {/if}
      {#if mac}
        <div><button class="btn sm ghost" onclick={() => (rotate = true)}><Icon name="refresh" size={14} />{t('settings.rotate')}</button></div>
      {/if}
    </section>

    <section class="card block">
      <div class="label between">
        <span class="label"><Icon name="machine" /><h2>{t('machine.title')}</h2></span>
        {#if mac}<button class="btn sm" onclick={() => (addingMachine = true)}><Icon name="plus" size={14} />{t('machine.add')}</button>{/if}
      </div>
      <p class="muted small">{t('machine.hint')}</p>
      <ul class="list">
        {#each machines as m (m.id)}
          <li>
            <div class="p-main">
              <span class="p-name">{m.label}</span>
              <span class="mstatus {m.status}">{t(`machine.status.${m.status}`)}</span>
              <code class="p-path">{m.id === 'local' ? t('machine.local') : m.sshTarget}{m.os ? ` · ${m.os}` : ''}</code>
              <span class="p-path">{t('machine.projects', { n: projectsOn(m) })}</span>
              {#if m.error && m.status !== 'online'}<span class="merr">{m.error}</span>{/if}
            </div>
            {#if m.id !== 'local'}
              <div class="m-tools">
                {#if m.status === 'needs-update'}
                  <button class="btn sm" disabled={!mac} onclick={() => machineAction(() => api.updateMachine(m.id))}>{t('machine.update')}</button>
                {:else if m.status !== 'online'}
                  <button class="btn sm" onclick={() => machineAction(() => api.reconnectMachine(m.id))}><Icon name="refresh" size={14} /><span class="rm">{t('machine.reconnect')}</span></button>
                {/if}
                <button class="btn ghost sm" aria-label={t('machine.removeLabel', { name: m.label })} onclick={() => machineAction(() => api.removeMachine(m.id))}><Icon name="trash" size={14} /></button>
              </div>
            {/if}
          </li>
        {/each}
      </ul>
    </section>

    <section class="card block">
      <div class="label between">
        <span class="label"><Icon name="folder" /><h2>{t('settings.projects')}</h2></span>
        <button class="btn sm" onclick={() => (ui.addProject = true)}><Icon name="plus" size={14} />{t('nav.addProject')}</button>
      </div>
      {#each app.data?.clients ?? [] as c (c.id)}
        {@const projects = app.data?.projects.filter((p) => p.clientId === c.id) ?? []}
        {#if projects.length}
          <h3 class="sub">{clientName(c.id)}</h3>
          <ul class="list">
            {#each projects as p (p.id)}
              <li>
                <div class="p-main">
                  <span class="p-name">{p.name}</span>
                  {#if !p.active}<a class="off" href={href({ name: 'activate', project: p.id })}>{t('nav.notActive')}</a>{/if}
                  <code class="p-path">{p.path}</code>
                </div>
                <button class="btn sm" class:ghost={removing !== p.id} class:danger={removing === p.id} aria-label={t('settings.removeProject', { name: p.name })} onclick={() => removeProject(p.id)}>
                  <Icon name="trash" size={14} /><span class="rm">{removing === p.id ? t('settings.confirmRemove') : t('settings.remove')}</span>
                </button>
              </li>
            {/each}
          </ul>
        {/if}
      {/each}
      <p class="muted small">{t('settings.removeHint')}</p>
    </section>

    <section class="card">
      <div class="row">
        <div class="label"><Icon name="terminal" /><h2>{t('settings.claude')}</h2></div>
        {#if s.claude.found}
          <span class="status ok"><Icon name="check" size={14} />{t('settings.found', { version: s.claude.version ?? '' })}</span>
        {:else}
          <span class="status err"><Icon name="x" size={14} />{t('settings.claudeMissing')}</span>
        {/if}
      </div>
      <div class="row">
        <div class="label"><Icon name="globe" /><h2>{t('settings.gh')}</h2></div>
        {#if s.gh.found && s.gh.authed}
          <span class="status ok"><Icon name="check" size={14} />{t('settings.ghOk')}</span>
        {:else}
          <span class="status err"><Icon name="x" size={14} />{s.gh.found ? t('settings.ghLogin') : t('settings.ghMissing')}</span>
        {/if}
      </div>
      {#if s.globalPluginInstalled}
        <p class="notice"><Icon name="alert" size={14} />{t('settings.globalPlugin')}</p>
      {/if}
      {#if !s.sessionsAvailable}
        <p class="notice"><Icon name="alert" size={14} />{t('settings.sessionsUnavailable')}</p>
      {/if}
      <p class="path mono muted">Truehand {s.version}{s.claude.path ? ` · ${s.claude.path}` : ''}</p>
    </section>
  </div>

  <Modal bind:open={addingMachine} title={t('machine.add')} onsubmit={addMachine}>
    <label class="field">
      <span>{t('machine.ssh')}</span>
      <input class="input mono" bind:value={sshTarget} placeholder={t('machine.sshPlaceholder')} required autocomplete="off" />
      <small>{t('machine.sshHint')}</small>
    </label>
    <label class="field">
      <span>{t('machine.label')}</span>
      <input class="input" bind:value={machineLabel} placeholder={t('machine.labelPlaceholder')} />
    </label>
    {#snippet footer()}
      <button type="button" class="btn" onclick={() => (addingMachine = false)}>{t('common.cancel')}</button>
      <button type="submit" class="btn primary" disabled={!sshTarget.trim()}>{t('machine.connect')}</button>
    {/snippet}
  </Modal>

  <Modal bind:open={rotate} title={t('settings.rotate')} onsubmit={doRotate}>
    <p>{t('settings.rotateHint')}</p>
    {#snippet footer()}
      <button type="button" class="btn" onclick={() => (rotate = false)}>{t('common.cancel')}</button>
      <button type="submit" class="btn primary">{t('settings.rotate')}</button>
    {/snippet}
  </Modal>
{/if}

<style>
  .settings {
    display: grid;
    gap: 14px;
    max-width: 720px;
    margin: 0 auto;
    padding: 22px 28px 40px;
  }
  h1 {
    font-size: 20px;
    margin-bottom: 4px;
  }
  h2 {
    font-size: 14px;
  }
  .card > .row + .row {
    border-top: 1px solid var(--border);
  }
  .row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 12px 16px;
    flex-wrap: wrap;
  }
  .block {
    display: grid;
    gap: 12px;
    padding: 14px 16px;
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
  .label small {
    display: block;
    font-size: 12px;
  }
  .between {
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 8px;
  }
  .seg {
    display: inline-flex;
    padding: 2px;
    border-radius: var(--radius);
    background: var(--surface-2);
    border: 1px solid var(--border);
  }
  .seg.wide {
    display: flex;
  }
  .seg.wide button {
    flex: 1;
  }
  .seg button {
    height: 28px;
    padding: 0 12px;
    border: 0;
    border-radius: var(--radius-sm);
    background: none;
    color: var(--text-2);
    font-size: 13px;
    font-weight: 500;
    cursor: pointer;
    white-space: nowrap;
  }
  .seg button[aria-checked='true'] {
    background: var(--surface);
    color: var(--text);
    box-shadow: var(--shadow);
  }
  .seg button:disabled {
    cursor: default;
  }
  .narrow {
    width: auto;
    min-width: 200px;
  }
  .small {
    font-size: 12.5px;
  }
  .addr {
    display: flex;
    align-items: flex-end;
    gap: 8px;
    flex-wrap: wrap;
  }
  .addr .field {
    flex: 1;
    min-width: 200px;
  }
  .r-status {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 8px 10px;
    border-radius: var(--radius-sm);
    font-size: 13px;
    overflow-wrap: anywhere;
    background: var(--idle-bg);
    color: var(--text-2);
  }
  .r-status.listening {
    background: var(--ok-bg);
    color: var(--ok);
  }
  .r-status.error {
    background: var(--err-bg);
    color: var(--err);
  }
  .kv {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    align-items: center;
    gap: 8px 12px;
  }
  .k {
    font-size: 12.5px;
    color: var(--text-2);
  }
  .v {
    padding: 5px 8px;
    border-radius: var(--radius-sm);
    background: var(--surface-2);
    overflow-wrap: anywhere;
  }
  .sub {
    font-size: 12px;
    font-weight: 600;
    color: var(--text-3);
    margin-top: 4px;
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
    align-items: baseline;
    gap: 2px 10px;
  }
  .p-name {
    font-weight: 600;
  }
  .p-path {
    font-size: 12px;
    color: var(--text-3);
    overflow-wrap: anywhere;
  }
  .tag {
    font-size: 11.5px;
    font-weight: 600;
    color: var(--info);
  }
  .off {
    font-size: 11.5px;
    color: var(--warn);
    font-weight: 600;
    text-decoration: underline;
    text-underline-offset: 2px;
  }
  .mstatus {
    font-size: 11.5px;
    font-weight: 600;
    color: var(--text-3);
  }
  .mstatus.online {
    color: var(--ok);
  }
  .mstatus.needs-update,
  .mstatus.needs-auth {
    color: var(--warn);
  }
  .merr {
    flex-basis: 100%;
    font-size: 12px;
    font-family: var(--mono);
    color: var(--err);
    overflow-wrap: anywhere;
  }
  .m-tools {
    display: flex;
    gap: 4px;
  }
  .status {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
    font-weight: 500;
  }
  .status.ok {
    color: var(--ok);
  }
  .status.err {
    color: var(--err);
  }
  .notice {
    display: flex;
    gap: 8px;
    align-items: center;
    margin: 0 16px 10px;
    padding: 8px 10px;
    border-radius: var(--radius-sm);
    background: var(--warn-bg);
    color: var(--warn);
    font-size: 12.5px;
  }
  .path {
    padding: 0 16px 12px;
    font-size: 12px;
  }
  @media (max-width: 640px) {
    .settings {
      padding: 14px 12px 32px;
    }
    h1 {
      display: none;
    }
    .rm {
      display: none;
    }
    .danger .rm {
      display: inline;
    }
    .narrow {
      width: 100%;
    }
    .seg.wide button {
      padding: 0 6px;
      font-size: 12.5px;
    }
  }
</style>
