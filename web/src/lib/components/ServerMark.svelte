<!-- The mark of the server: its logo (GET /api/identity) at `size` px tall and at most four times
     as wide, or the brand's flat mark (Logo) when it has none. Decorative: the name stands beside it. -->
<script lang="ts">
  import Logo from "./Logo.svelte";
  import { brand } from "$lib/brand.svelte";

  let { size = 28, class: className = "" }: { size?: number; class?: string } = $props();
</script>

{#if brand.logo}
  <img src={brand.logo} alt="" style:height="{size}px" style:max-width="{size * 4}px" class="w-auto object-contain {className}" />
{:else}
  <Logo {size} class={className} />
{/if}
