<!--
  The sentence of an audit event (wording.ts): the words in the ink of the text, the person and
  the target in medium weight, the repository with its format icon (a word joiner keeps the icon
  on the line of the name). Inline content only, so it reads in a row of Activity and in the title
  of an ItemList row (the Settings overview).

    <EventSentence parts={wordEvent(e, action).sentence} {formatOf} />
-->
<script lang="ts">
  import FormatIcon from "$lib/components/FormatIcon.svelte";
  import type { Part } from "./wording";

  let { parts, formatOf }: { parts: Part[]; /** The format of a repository, "" while unknown. */ formatOf: (repo: string) => string } = $props();
</script>

{#each parts as p, i (i)}{#if p.t === "text"}{p.text}{:else if p.t === "repo"}<span class="font-medium [overflow-wrap:anywhere]"
      >{#if formatOf(p.name)}<FormatIcon format={formatOf(p.name)} class="mr-1 align-[-3px]" />{"\u2060"}{/if}{p.name}</span
    >{:else if p.t === "value"}{p.text}{:else}<span class={["font-medium", p.t === "target" && "[overflow-wrap:anywhere]"]}>{p.text}</span>{/if}{/each}
