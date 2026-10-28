<script lang="ts">
  import { markdown } from '../markdown'

  let { source, class: cls = '' }: { source: string; class?: string } = $props()
  const html = $derived(markdown(source))
</script>

<div class="md {cls}">{@html html}</div>
