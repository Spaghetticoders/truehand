<script lang="ts">
  import { revealParts } from '../invisible'

  let { text }: { text: string } = $props()
</script>

{#each revealParts(text) as part, i (i)}{#if part.hidden}<mark class="hidden-char">{part.text}</mark>{:else}{part.text}{/if}{/each}

<style>
  .hidden-char {
    padding: 0 3px;
    margin: 0 1px;
    border-radius: 3px;
    background: var(--warn);
    color: var(--warn-bg);
    font-family: var(--mono);
    font-size: 0.85em;
  }
</style>
