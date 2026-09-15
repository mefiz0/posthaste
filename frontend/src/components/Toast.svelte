<script lang="ts">
  import { app } from '../lib/stores.svelte';

  const current = $derived(app.toasts[app.toasts.length - 1] ?? null);
</script>

<div class="toast" class:show={current != null} class:error={current?.level === 'error'} role="status" aria-live="polite">
  {current?.message ?? ''}
</div>
