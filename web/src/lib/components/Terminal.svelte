<script lang="ts">
  import { onMount } from 'svelte'
  import { Terminal, type ITheme } from '@xterm/xterm'
  import { FitAddon } from '@xterm/addon-fit'
  import '@xterm/xterm/css/xterm.css'
  import Icon, { type IconName } from './Icon.svelte'
  import { api, type PtyConnection, type SessionStatus } from '../api'
  import { t } from '../i18n/index.svelte'

  let { sessionId, label, live, write = false }: { sessionId: string; label: string; live: boolean; write?: boolean } = $props()

  const narrow = window.matchMedia('(max-width: 640px)').matches
  // On a phone the soft keyboard would cover the output: read-only until "Scrivi".
  let writing = $state(!narrow)
  let host: HTMLDivElement
  let term = $state.raw<Terminal>()
  let conn: PtyConnection | undefined
  let fit: FitAddon | undefined
  let ended = $state<SessionStatus | null>(null)

  const canWrite = $derived(live && !ended && writing)

  const KEYS: { label?: string; icon?: IconName; aria: string; seq: string }[] = [
    { label: 'Esc', aria: 'Esc', seq: '\x1b' },
    { label: 'Ctrl-C', aria: 'Ctrl-C', seq: '\x03' },
    { icon: 'arrow-up', aria: '↑', seq: '\x1b[A' },
    { icon: 'arrow-down', aria: '↓', seq: '\x1b[B' },
    { icon: 'arrow-left', aria: '←', seq: '\x1b[D' },
    { icon: 'arrow-right', aria: '→', seq: '\x1b[C' },
    { label: 'Tab', aria: 'Tab', seq: '\t' },
    { label: t('terminal.enter'), aria: t('terminal.enter'), seq: '\r' },
  ]

  function themeFromTokens(): ITheme {
    const css = getComputedStyle(document.documentElement)
    const v = (name: string) => css.getPropertyValue(name).trim()
    return {
      background: v('--term-bg'),
      foreground: v('--term-fg'),
      cursor: v('--term-cursor'),
      selectionBackground: v('--term-selection'),
      black: v('--term-bg'),
      brightBlack: v('--term-dim'),
      red: v('--term-red'),
      brightRed: v('--term-red'),
      green: v('--term-green'),
      brightGreen: v('--term-green'),
      yellow: v('--term-yellow'),
      brightYellow: v('--term-yellow'),
      blue: v('--term-blue'),
      brightBlue: v('--term-blue'),
      magenta: v('--term-magenta'),
      brightMagenta: v('--term-magenta'),
      cyan: v('--term-cyan'),
      brightCyan: v('--term-cyan'),
      white: v('--term-fg'),
      brightWhite: v('--term-fg'),
    }
  }

  function send(seq: string) {
    if (!canWrite || !conn || !term) return
    conn.resize(term.cols, term.rows)
    conn.send(seq)
    term.focus()
  }

  // "Rispondi qui" opens the terminal ready to type, also on a phone.
  $effect.pre(() => {
    if (write && live) writing = true
  })

  let wasWriting = false
  $effect(() => {
    if (!term) return
    term.options.disableStdin = !canWrite
    term.options.cursorBlink = canWrite
    // Focus only when writing starts, so a phone opens its keyboard then and not on load.
    if (canWrite && !wasWriting) term.focus()
    wasWriting = canWrite
  })

  function toggleWriting() {
    writing = !writing
    if (!term) return
    term.options.disableStdin = !canWrite
    // Inside the tap, or mobile browsers keep the keyboard closed.
    if (canWrite) term.focus()
    else term.blur()
  }

  onMount(() => {
    const t0 = new Terminal({
      fontFamily: getComputedStyle(document.documentElement).getPropertyValue('--mono'),
      fontSize: narrow ? 11 : 12.5,
      lineHeight: 1.2,
      cursorBlink: canWrite,
      cursorInactiveStyle: 'none',
      scrollback: 5000,
      disableStdin: !canWrite,
      theme: themeFromTokens(),
    })
    fit = new FitAddon()
    t0.loadAddon(fit)
    t0.open(host)
    fit.fit()
    t0.textarea?.setAttribute('aria-label', label)
    wasWriting = canWrite && !write
    term = t0

    conn = api.pty(sessionId, {
      onData: (chunk) => term?.write(chunk),
      onExit: (e) => (ended = e.status),
    })
    const input = t0.onData((d) => send(d))

    const resize = new ResizeObserver(() => {
      fit?.fit()
      if (canWrite && term) conn?.resize(term.cols, term.rows)
    })
    resize.observe(host)

    const retheme = () => term && (term.options.theme = themeFromTokens())
    const scheme = window.matchMedia('(prefers-color-scheme: dark)')
    scheme.addEventListener('change', retheme)
    const attrs = new MutationObserver(retheme)
    attrs.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })

    return () => {
      conn?.close()
      input.dispose()
      resize.disconnect()
      attrs.disconnect()
      scheme.removeEventListener('change', retheme)
      t0.dispose()
      term = undefined
    }
  })
</script>

<div class="wrap">
  <div class="term" class:readonly={!canWrite} role="region" aria-label={label}>
    <div class="host" bind:this={host}></div>
  </div>
  {#if live && !ended}
    <div class="bar">
      <button class="btn sm" class:primary={!writing} aria-pressed={writing} onclick={toggleWriting}>
        <Icon name={writing ? 'lock' : 'keyboard'} size={14} />{writing ? t('terminal.stopWriting') : t('terminal.write')}
      </button>
      {#if writing}
        <span class="hint writing">{t('terminal.writingHint')}</span>
        <div class="keys" role="group" aria-label={t('terminal.keys')}>
          {#each KEYS as k (k.aria)}
            <button class="key" aria-label={k.aria} onclick={() => send(k.seq)}>
              {#if k.icon}<Icon name={k.icon} size={14} />{:else}{k.label}{/if}
            </button>
          {/each}
        </div>
      {:else}
        <span class="hint">{t('terminal.readOnlyHint')}</span>
      {/if}
    </div>
  {/if}
</div>

<style>
  .wrap {
    height: 100%;
    min-height: 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .term {
    flex: 1;
    min-height: 0;
    background: var(--term-bg);
    border-radius: var(--radius);
    padding: 10px 4px 10px 12px;
    overflow: hidden;
    box-shadow: inset 0 0 0 1px transparent;
  }
  .term:not(.readonly) {
    box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--term-cursor) 45%, transparent);
  }
  .host {
    height: 100%;
  }
  .bar {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }
  .keys {
    display: flex;
    gap: 4px;
    flex-wrap: wrap;
  }
  .key {
    min-width: 36px;
    height: 32px;
    padding: 0 9px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border-radius: var(--radius-sm);
    border: 1px solid var(--border-strong);
    background: var(--surface-2);
    font-family: var(--mono);
    font-size: 12px;
    cursor: pointer;
  }
  .key:active {
    background: var(--surface-hover);
  }
  .hint {
    font-size: 12px;
    color: var(--text-3);
  }
  @media (min-width: 641px) {
    .key {
      height: 28px;
    }
  }
  @media (max-width: 640px) {
    .bar {
      display: grid;
    }
    .keys {
      display: grid;
      grid-template-columns: repeat(4, 1fr);
    }
    .key {
      height: 38px;
    }
    .bar > .btn {
      justify-self: start;
    }
  }
</style>
