<!--
  A choice of one person, or everyone, for a filter: a field with the chosen person's avatar that
  asks the server for the people whose name starts with what you type, and a menu of them, each
  with a small `UserAvatar`. `value` is "" for everyone. A name from the address that the search
  does not return still shows as the choice.

    <PersonPicker search={(q) => api.auditPeople(q)} value={actor} onchange={(a) => setFilter({ actor: a })} />
-->
<script lang="ts">
  import { Combobox } from "bits-ui";
  import * as Select from "$lib/components/ui/select";
  import type { Page, PersonName } from "$lib/api";
  import UserAvatar from "./UserAvatar.svelte";
  import UsersIcon from "@lucide/svelte/icons/users";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import { cn } from "$lib/utils";

  let {
    search: find,
    value,
    onchange,
    label = "Person",
    allLabel = "Everyone",
    class: className,
  }: {
    search: (q: string) => Promise<Page<PersonName>>;
    value: string;
    onchange: (username: string) => void;
    label?: string;
    allLabel?: string;
    class?: string;
  } = $props();

  const all = "*";
  let open = $state(false);
  let search = $state("");
  let anchor = $state<HTMLElement | null>(null);
  let found = $state<string[]>([]);
  let failed = $state(false);

  // The people of the typed prefix, asked again on each change while the menu is open; an answer
  // that a newer one overtook is dropped.
  let asked = 0;
  $effect(() => {
    if (!open) return;
    const q = search.trim().toLowerCase();
    const n = ++asked;
    find(q).then(
      (p) => {
        if (n === asked) {
          found = p.items.map((x) => x.username);
          failed = false;
        }
      },
      () => {
        if (n === asked) failed = true;
      },
    );
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
  <div bind:this={anchor} class={cn("relative w-full sm:w-52", className)}>
    <span class="pointer-events-none absolute top-1/2 left-2 flex -translate-y-1/2 text-muted-foreground">
      {#if value && !open}<UserAvatar name={value} />{:else}<span class="flex size-6 items-center justify-center"><UsersIcon class="size-4" /></span>{/if}
    </span>
    <Combobox.Input
      aria-label={label}
      placeholder="Find a person"
      oninput={(e) => (search = e.currentTarget.value)}
      onfocus={(e) => e.currentTarget.select()}
      class="h-9 w-full min-w-0 truncate rounded-4xl border border-input bg-input/30 pr-9 pl-10 text-sm transition-colors outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 dark:hover:bg-input/50"
    />
    <Combobox.Trigger
      aria-label="Show the people"
      class="absolute top-1/2 right-1 flex size-7 -translate-y-1/2 items-center justify-center rounded-full text-muted-foreground outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
    >
      <ChevronDownIcon class="size-4" />
    </Combobox.Trigger>
  </div>
  <Select.Content class="max-h-80" customAnchor={anchor} align="start">
    <!-- "Everyone" keeps the avatar's width empty, so every name starts on one line. -->
    {#if !search.trim()}
      <Select.Item value={all} label={allLabel} class="pl-2">
        {#snippet icon()}<span class="size-6" aria-hidden="true"></span>{/snippet}
      </Select.Item>
    {/if}
    {#if value && !found.includes(value) && !search.trim()}
      <Select.Item value={value} label={value} class="pl-2">
        {#snippet icon()}<UserAvatar name={value} />{/snippet}
      </Select.Item>
    {/if}
    {#each found as name (name)}
      <Select.Item value={name} label={name} class="pl-2">
        {#snippet icon()}<UserAvatar {name} />{/snippet}
      </Select.Item>
    {:else}
      {#if failed}
        <p class="px-3 py-2 text-sm text-muted-foreground">The people did not load. Type again to try again.</p>
      {:else if search.trim()}
        <p class="px-3 py-2 text-sm text-muted-foreground">No person's name starts with “{search.trim()}”.</p>
      {/if}
    {/each}
  </Select.Content>
</Combobox.Root>
