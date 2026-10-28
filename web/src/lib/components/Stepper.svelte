<script lang="ts">
  import { TASK_STATES, type TaskState } from '../api'
  import { t } from '../i18n/index.svelte'

  let { state, merged = false }: { state: TaskState; merged?: boolean } = $props()
  const current = $derived(TASK_STATES.indexOf(state))
</script>

<ol class="stepper" aria-label={t('step.label', { state: merged ? t('state.merged') : t(`state.${state}`) })}>
  {#each TASK_STATES as s, i (s)}
    <li class:done={i < current || merged} class:current={i === current && !merged} aria-current={i === current ? 'step' : undefined}>
      <span class="mark" aria-hidden="true"></span>
      <span class="label">{t(`step.${s}`)}</span>
    </li>
  {/each}
</ol>

<style>
  .stepper {
    display: flex;
    list-style: none;
    margin: 0;
    padding: 0;
    gap: 2px;
    min-width: 0;
  }
  li {
    flex: 1 1 0;
    min-width: 0;
    display: grid;
    gap: 5px;
    font-size: 11.5px;
    color: var(--text-3);
  }
  .mark {
    height: 4px;
    border-radius: 2px;
    background: var(--border);
  }
  .done .mark {
    background: var(--ok);
  }
  .current .mark {
    background: var(--accent);
  }
  .current {
    color: var(--text);
    font-weight: 600;
  }
  .done {
    color: var(--text-2);
  }
  .label {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  @media (max-width: 640px) {
    .label {
      display: none;
    }
    .current .label {
      display: block;
      overflow: visible;
    }
    li.current {
      flex: 2.4 1 0;
    }
  }
</style>
