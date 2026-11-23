<script lang="ts">
  import { isGroup } from '../core/workspace/layout.ts';
  import type { Node } from '../core/workspace/layout.ts';
  import type { Workspace } from './state.svelte.ts';
  import Diagram from './Diagram.svelte';

  // A diagram of a layout with each pane's actual title. The docking preview
  // and the keyboard move dialog both show it.
  let { ws, node, incoming, target, weight = 1 }: { ws: Workspace; node: Node; incoming: string; target?: string; weight?: number } = $props();
</script>

{#if isGroup(node)}
  <div class="schematic groupmini {node.dir}" class:incoming={node.id === incoming} class:targetmark={node.id === target} style:flex="{weight} 1 0">
    {#each node.children as child, i (child.id)}
      <Diagram {ws} node={child} {incoming} {target} weight={node.weights[i]} />
    {/each}
  </div>
{:else}
  <div class="leafmini" class:incoming={node.id === incoming} class:targetmark={node.id === target} style:flex="{weight} 1 0">{ws.paneTitle(node)}</div>
{/if}
