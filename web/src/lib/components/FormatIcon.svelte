<!--
  The icon of a format, from the format plugin (GET /api/formats): the Maven feather, the Debian
  swirl, the raw file. `kind` picks an artifact kind's icon when the format has one: a Gradle
  plugin (kind "gradle-plugin") in a Maven repo shows the Gradle elephant. The icon is one color
  (currentColor) and 16px by default; the space is reserved before the formats load, so nothing
  moves. It marks meaning only: never put it in a tinted square.

    <FormatIcon format={repo.format} />
    <FormatIcon format="maven" kind={artifact.kind} class="size-5" />
-->
<script lang="ts">
  import { formats } from "$lib/formats/registry.svelte";
  import { cn } from "$lib/utils";

  let {
    format,
    kind,
    label,
    class: className,
  }: {
    format: string;
    kind?: string;
    /** Accessible name; leave it out when a title is next to the icon. */
    label?: string;
    class?: string;
  } = $props();

  formats.load();
  const svg = $derived(formats.icon(format, kind));
</script>

<span
  role={label ? "img" : undefined}
  aria-label={label}
  aria-hidden={label ? undefined : "true"}
  class={cn("inline-flex size-4 shrink-0 items-center justify-center [&>svg]:size-full", className)}
>
  <!-- The SVG is the format plugin's icon, embedded in the server binary; it is not user data. -->
  {#if svg}{@html svg}{/if}
</span>
