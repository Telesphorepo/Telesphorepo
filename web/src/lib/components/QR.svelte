<!-- A QR code (e.g. an otpauth:// URL). White background and black modules in both themes, so phones read it. -->
<script lang="ts">
  import { qrMatrix } from "$lib/qr";

  let { text, px = 200 }: { text: string; px?: number } = $props();

  const q = 4;
  const m = $derived(qrMatrix(text));
  const n = $derived(m ? m.length + 2 * q : 0);
  const d = $derived.by(() => {
    let s = "";
    m?.forEach((row, y) => row.forEach((dark, x) => {
      if (dark) s += `M${x + q},${y + q}h1v1h-1z`;
    }));
    return s;
  });
</script>

{#if m}
  <svg width={px} height={px} viewBox="0 0 {n} {n}" shape-rendering="crispEdges" role="img" aria-label="QR code" class="rounded-lg">
    <rect width={n} height={n} fill="#fff" />
    <path {d} fill="#000" />
  </svg>
{/if}
