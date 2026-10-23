<!--
  A choice of one repository, or all of them, for a filter: a field with the chosen repository's
  format icon that filters the list as you type, and a menu of the repositories with their icons.
  `value` is "" for all repositories. A name that is not among `repos` (from the address) still
  shows as the choice.

    <RepoPicker repos={repos.data ?? []} value={repo} onchange={(r) => setFilter({ repo: r })} />
-->
<script lang="ts">
  import { Combobox } from "bits-ui";
  import * as Select from "$lib/components/ui/select";
  import FormatIcon from "./FormatIcon.svelte";
  import DatabaseIcon from "@lucide/svelte/icons/database";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import { cn } from "$lib/utils";

  let {
    repos,
    value,
    onchange,
    label = "Repository",
    allLabel = "All repositories",
    class: className,
  }: {
    repos: readonly { name: string; format: string }[];
    value: string;
    onchange: (name: string) => void;
    label?: string;
    allLabel?: string;
    class?: string;
  } = $props();

  const all = "*";
  let open = $state(false);
  let search = $state("");
  let anchor = $state<HTMLElement | null>(null);

  const sorted = $derived([...repos].sort((a, b) => a.name.localeCompare(b.name)));
  const chosen = $derived(sorted.find((r) => r.name === value));
  const shown = $derived.by(() => {
    const t = search.trim().toLowerCase();
    return t ? sorted.filter((r) => r.name.toLowerCase().includes(t)) : sorted;
  });
</script>

<Combobox.Root
  type="single"
  value={value || all}
  onValueChange={(v) => {
    search = "";
    onchange(v === all ? "" : v);
  }}
  bind:open
  onOpenChange={(o) => {
    if (!o) search = "";
  }}
  inputValue={open ? search : value || allLabel}
>
  <div bind:this={anchor} class={cn("relative w-full sm:w-56", className)}>
    <span class="pointer-events-none absolute top-1/2 left-3 flex -translate-y-1/2 text-muted-foreground">
      {#if chosen}<FormatIcon format={chosen.format} class="text-foreground" />{:else}<DatabaseIcon class="size-4" />{/if}
    </span>
    <Combobox.Input
      aria-label={label}
      placeholder="Find a repository"
      oninput={(e) => (search = e.currentTarget.value)}
      onfocus={(e) => e.currentTarget.select()}
      class="h-9 w-full min-w-0 truncate rounded-4xl border border-input bg-input/30 pr-9 pl-9 text-sm transition-colors outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 dark:hover:bg-input/50"
    />
    <Combobox.Trigger
      aria-label="Show the repositories"
      class="absolute top-1/2 right-1 flex size-7 -translate-y-1/2 items-center justify-center rounded-full text-muted-foreground outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
    >
      <ChevronDownIcon class="size-4" />
    </Combobox.Trigger>
  </div>
  <Select.Content class="max-h-80" customAnchor={anchor} align="start">
    {#if !search.trim()}<Select.Item value={all} label={allLabel} inset />{/if}
    {#if value && !chosen}<Select.Item value={value} label={value} inset />{/if}
    {#each shown as r (r.name)}
      <Select.Item value={r.name} label={r.name}>
        {#snippet icon()}<FormatIcon format={r.format} />{/snippet}
      </Select.Item>
    {:else}
      {#if search.trim()}<p class="px-3 py-2 text-sm text-muted-foreground">No repository matches “{search.trim()}”.</p>{/if}
    {/each}
  </Select.Content>
</Combobox.Root>
