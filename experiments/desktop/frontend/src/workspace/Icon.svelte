<script lang="ts" module>
  const paths = {
    session: 'M3 5h18v14H3z M7 9l3 3-3 3 M13 15h4',
    logs: 'M4 6h16 M4 12h16 M4 18h11',
    monitor: 'M12 3l8 3v6c0 4-4 7-8 9-4-2-8-5-8-9V6z M8 12l3 2 5-6',
    group: 'M3 5h18v14H3z M12 5v14',
    chevron: 'M6 9l6 6 6-6',
    close: 'M6 6l12 12 M18 6L6 18',
    plus: 'M12 5v14 M5 12h14',
    more: 'M5 12h.01 M12 12h.01 M19 12h.01',
    grip: 'M9 6h.01 M15 6h.01 M9 12h.01 M15 12h.01 M9 18h.01 M15 18h.01',
  };
  export type IconName = keyof typeof paths;
</script>

<script lang="ts">
  let { name }: { name: IconName } = $props();
</script>

<svg class="glyph" class:dots={name === 'more' || name === 'grip'} viewBox="0 0 24 24" aria-hidden="true"><path d={paths[name]} /></svg>
