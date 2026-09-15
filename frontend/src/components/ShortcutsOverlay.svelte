<script lang="ts">
  import { focusTrap } from '../lib/focus';
  import { app } from '../lib/stores.svelte';
  import { formatChordParts, getKeymap, type KeyBinding } from '../lib/keys';
  import { overlayFade } from '../lib/transitions';

  const GROUP_ORDER = ['Navigation', 'Message', 'Compose', 'Global'] as const;
  type GroupName = (typeof GROUP_ORDER)[number];

  const groups = $derived.by(() => {
    const keymap = getKeymap(app.settings?.keymap);
    const byGroup = new Map<GroupName, KeyBinding[]>();
    for (const binding of keymap) {
      const bucket = byGroup.get(binding.group) ?? [];
      bucket.push(binding);
      byGroup.set(binding.group, bucket);
    }
    return GROUP_ORDER.filter((group) => byGroup.has(group)).map((group) => ({
      title: group,
      bindings: byGroup.get(group) as KeyBinding[],
    }));
  });
</script>

<div class="overlay open" transition:overlayFade role="presentation">
  <button
    class="backdrop"
    aria-label="Close keyboard shortcuts"
    onclick={() => (app.shortcutsOpen = false)}
  ></button>
  <div
    class="dialog shortcuts"
    role="dialog"
    aria-modal="true"
    aria-label="Keyboard shortcuts"
    use:focusTrap={{ autofocusSelector: 'h2' }}
  >
    <h2>Keyboard shortcuts</h2>
    <div class="sc-grid">
      {#each groups as group (group.title)}
        <div class="sc-sec">
          <div class="sc-title">{group.title}</div>
          {#each group.bindings as binding (binding.id)}
            <div class="sc-row">
              <span>{binding.label}</span>
              <span class="sc-keys">
                {#each binding.chords as alternative, alternativeIndex (alternativeIndex)}
                  {#each alternative as chord (chord.key + String(chord.ctrl) + String(chord.shift) + String(chord.alt))}
                    {#each formatChordParts(chord) as part, index (index)}
                      <kbd>{part}</kbd>
                    {/each}
                  {/each}
                {/each}
              </span>
            </div>
          {/each}
        </div>
      {/each}
    </div>
  </div>
</div>
