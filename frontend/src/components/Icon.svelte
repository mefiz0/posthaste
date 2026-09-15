<script lang="ts">
  import type { IconName } from '../lib/icons';

  let { name, fill = false }: { name: IconName; fill?: boolean } = $props();
</script>

<svg class="ic" class:fill aria-hidden="true" focusable="false">
  <use href="#i-{name}" />
</svg>
