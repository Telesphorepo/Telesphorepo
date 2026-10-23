<!-- A QR code (e.g. an otpauth:// URL). White background and black modules in both themes, so phones read it. -->
<script lang="ts">
  import { qrSVG } from "$lib/qr";

  let { text }: { text: string } = $props();

  const qr = $derived(qrSVG(text));
</script>

{#await qr then { svg, px }}
  <div role="img" aria-label="QR code" class="shrink-0 overflow-hidden rounded-lg [&>svg]:block" style:width="{px}px" style:height="{px}px">
    {@html svg}
  </div>
{/await}
