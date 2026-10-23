<!--
  Text with every place that holds `term` (any case) marked: the part of a path that the search
  matched.
-->
<script lang="ts">
  let { text, term }: { text: string; term: string } = $props();

  const parts = $derived.by(() => {
    const out: { s: string; hit: boolean }[] = [];
    const t = term.toLowerCase();
    if (!t) return [{ s: text, hit: false }];
    const low = text.toLowerCase();
    let i = 0;
    for (let j = low.indexOf(t); j >= 0; j = low.indexOf(t, i)) {
      if (j > i) out.push({ s: text.slice(i, j), hit: false });
      out.push({ s: text.slice(j, j + t.length), hit: true });
      i = j + t.length;
    }
    if (i < text.length) out.push({ s: text.slice(i), hit: false });
    return out;
  });
</script>

{#each parts as p, i (i)}{#if p.hit}<mark class="rounded-none bg-selected text-selected-foreground">{p.s}</mark>{:else}{p.s}{/if}{/each}
