<!--
  A choice of one repository, or all of them for a filter: a field with the chosen repository's
  format icon that asks the server for the repositories whose name starts with what you type
  (GET /api/repos?q=, 20 a time), and a menu of them with their icons. `value` is "" for all
  repositories. `filter` narrows the server's list (`perm`, `kind`); `keep` drops a repository
  from the menu (Search leaves out groups). With `all={false}` the choice is required: there is no
  "All repositories" row (the repository of a publish token). A name from the address that the
  search does not return still shows as the choice.

    <RepoPicker value={repo} onchange={(r) => setFilter({ repo: r })} />
-->
<script lang="ts">
  import { Combobox } from "bits-ui";
  import * as Select from "$lib/components/ui/select";
  import { api, type Repo, type RepoFilter } from "$lib/api";
  import FormatIcon from "./FormatIcon.svelte";
  import DatabaseIcon from "@lucide/svelte/icons/database";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import { cn } from "$lib/utils";

  let {
    value,
    onchange,
    filter = {},
    keep,
    format,
    all: offerAll = true,
    id,
    label = "Repository",
    allLabel = "All repositories",
    class: className,
  }: {
    value: string;
    onchange: (name: string) => void;
    /** What the server keeps (the typed prefix is added). */
    filter?: RepoFilter;
    /** Which of the server's repositories the menu shows; undefined: all of them. */
    keep?: (r: Repo) => boolean;
    /** The format of `value` when the page knows it, for the icon before a search found it. */
    format?: string;
    all?: boolean;
    id?: string;
    label?: string;
    allLabel?: string;
    class?: string;
  } = $props();

  const anyRepo = "*";
  let open = $state(false);
  let search = $state("");
  let anchor = $state<HTMLElement | null>(null);
  let found = $state<Repo[]>([]);
  let failed = $state(false);
  // The formats of the repositories the menu showed, for the icon of the chosen one.
  let formatsOf = $state<Record<string, string>>({});

  // The repositories of the typed prefix, asked again on each change while the menu is open; an
  // answer that a newer one overtook is dropped.
  let asked = 0;
  $effect(() => {
    if (!open) return;
    const q = search.trim().toLowerCase();
    const n = ++asked;
    api.repos.list({ ...filter, q }, { limit: 20 }).then(
      (p) => {
        if (n !== asked) return;
        found = keep ? p.items.filter(keep) : p.items;
        formatsOf = { ...formatsOf, ...Object.fromEntries(found.map((r) => [r.name, r.format])) };
        failed = false;
      },
      () => {
        if (n === asked) failed = true;
      },
    );
  });

  const chosenFormat = $derived(value ? (formatsOf[value] ?? format ?? "") : "");
  const placeholder = $derived(offerAll ? allLabel : "Choose a repository");
</script>

<Combobox.Root
  type="single"
  value={value || (offerAll ? anyRepo : "")}
  onValueChange={(v) => {
    search = "";
    if (v) onchange(v === anyRepo ? "" : v);
  }}
  bind:open
  onOpenChange={(o) => {
    if (!o) search = "";
  }}
  inputValue={open ? search : value || (offerAll ? allLabel : "")}
>
  <div bind:this={anchor} class={cn("relative w-full sm:w-56", className)}>
    <span class="pointer-events-none absolute top-1/2 left-3 flex -translate-y-1/2 text-muted-foreground">
      {#if chosenFormat && !open}<FormatIcon format={chosenFormat} class="text-foreground" />{:else}<DatabaseIcon class="size-4" />{/if}
    </span>
    <Combobox.Input
      {id}
      aria-label={label}
      placeholder={open ? "Find a repository" : placeholder}
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
    {#if offerAll && !search.trim()}<Select.Item value={anyRepo} label={allLabel} inset />{/if}
    {#if value && !search.trim() && !found.some((r) => r.name === value)}
      <Select.Item value={value} label={value} inset={!chosenFormat}>
        {#snippet icon()}{#if chosenFormat}<FormatIcon format={chosenFormat} />{/if}{/snippet}
      </Select.Item>
    {/if}
    {#each found as r (r.name)}
      <Select.Item value={r.name} label={r.name}>
        {#snippet icon()}<FormatIcon format={r.format} />{/snippet}
      </Select.Item>
    {:else}
      {#if failed}
        <p class="px-3 py-2 text-sm text-muted-foreground">The repositories did not load. Type again to try again.</p>
      {:else if search.trim()}
        <p class="px-3 py-2 text-sm text-muted-foreground">No repository's name starts with “{search.trim()}”.</p>
      {/if}
    {/each}
  </Select.Content>
</Combobox.Root>
