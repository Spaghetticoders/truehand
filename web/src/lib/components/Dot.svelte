<script lang="ts">
  import { t } from '../i18n/index.svelte'
  import type { Dot } from '../stores/app.svelte'

  let { status, size = 8 }: { status: Dot; size?: number } = $props()
</script>

<span class="dot {status}" style:--s="{size}px" role="img" aria-label={t(`dot.${status}`)} title={t(`dot.${status}`)}></span>

<style>
  .dot {
    display: inline-block;
    flex: none;
    width: var(--s);
    height: var(--s);
    border-radius: 50%;
    background: var(--dot-idle);
  }
  .working {
    background: var(--dot-working);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--dot-working) 22%, transparent);
    animation: pulse 1.6s ease-in-out infinite;
  }
  .waiting {
    background: var(--dot-waiting);
  }
  .error {
    background: var(--err);
  }
  @keyframes pulse {
    50% {
      box-shadow: 0 0 0 1px color-mix(in srgb, var(--dot-working) 10%, transparent);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .working {
      animation: none;
    }
  }
</style>
