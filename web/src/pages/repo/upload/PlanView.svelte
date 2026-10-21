<!--
  What an upload plan holds (POST /api/repos/{name}/upload/plan): one row per artifact or package
  with the path each file gets and the problems that refuse it, a checkbox to leave it out when
  there are several, and the files that are left out, folded with the reason of the format.

    <PlanView {plan} {skipped} upload={format.upload} bind:selected />

  selected[i] says whether plan.items[i] is uploaded. An item with problems cannot be ticked.
  skipped counts the files of a folder that the browser did not send; they count as left out.
-->
<script lang="ts">
  import type { FormatUpload, UploadPlan } from "$lib/api";
  import * as Collapsible from "$lib/components/ui/collapsible";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import { Label } from "$lib/components/ui/label";
  import { Button } from "$lib/components/ui/button";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ChevronRightIcon from "@lucide/svelte/icons/chevron-right";
  import TriangleAlertIcon from "@lucide/svelte/icons/triangle-alert";
  import PackageSearchIcon from "@lucide/svelte/icons/package-search";

  let {
    plan,
    upload,
    skipped = 0,
    selected = $bindable([]),
    disabled = false,
  }: {
    plan: UploadPlan;
    upload: FormatUpload;
    skipped?: number;
    selected?: boolean[];
    disabled?: boolean;
  } = $props();

  const id = $props.id();
  const word = (n: number) => (n === 1 ? upload.one : upload.many);
  const nFiles = (n: number) => `${n.toLocaleString()} ${n === 1 ? "file" : "files"}`;

  const usable = $derived(plan.items.map((f) => f.problems.length === 0));
  const nUsable = $derived(usable.filter(Boolean).length);
  const nBad = $derived(plan.items.length - nUsable);
  const nPicked = $derived(selected.filter(Boolean).length);
  const many = $derived(plan.items.length > 1);

  const setAll = (on: boolean) => (selected = usable.map((u) => on && u));
</script>

{#if plan.items.length === 0}
  <EmptyState icon={PackageSearchIcon} title="Nothing to upload" description={upload.empty || "The upload has no file that this repository takes."} />
{:else}
  <div class="flex flex-col gap-3">
    {#if many}
      <div class="flex flex-wrap items-center justify-between gap-2">
        <p class="text-sm">
          Found {plan.items.length.toLocaleString()} {word(plan.items.length)}.
          {#if nBad}<span class="text-destructive">{nBad.toLocaleString()} cannot be uploaded.</span>{/if}
        </p>
        {#if nUsable > 1}
          <div class="flex gap-2">
            <Button variant="ghost" size="sm" disabled={disabled || nPicked === nUsable} onclick={() => setAll(true)}>Select all</Button>
            <Button variant="ghost" size="sm" disabled={disabled || nPicked === 0} onclick={() => setAll(false)}>Select none</Button>
          </div>
        {/if}
      </div>
    {/if}

    <ul class="flex max-h-[45dvh] flex-col gap-1 overflow-y-auto rounded-lg border p-2">
      {#each plan.items as item, i (i)}
        {@const bad = item.problems.length > 0}
        {@const single = item.files.length === 1 && item.files[0].path === item.title}
        <li
          class="flex items-start gap-3 rounded-lg px-2 py-2 {bad
            ? 'bg-destructive/8'
            : 'hover:bg-hover has-data-[state=checked]:bg-selected has-data-[state=checked]:text-selected-foreground'}"
        >
          {#if many}
            <Checkbox
              id="{id}-item-{i}"
              class="mt-0.5"
              checked={!bad && !!selected[i]}
              disabled={disabled || bad}
              onCheckedChange={(v) => (selected[i] = v === true)}
              aria-describedby={bad ? `${id}-problems-${i}` : undefined}
            />
          {/if}
          <div class="flex min-w-0 flex-1 flex-col gap-1">
            {#if many}<Label for="{id}-item-{i}" class="text-[0.875rem] font-medium break-all">{item.title}</Label>{:else}<span class="text-[0.875rem] font-medium break-all">{item.title}</span>{/if}
            {#if single && item.files[0].target && item.files[0].target !== item.title}
              <p class="text-xs break-all text-muted-foreground">Stored as {item.files[0].target}</p>
            {:else if !single}
              <Collapsible.Root>
                <Collapsible.Trigger
                  class="group -mx-1 flex items-center gap-1 rounded-md px-1 text-xs text-muted-foreground hover:bg-hover hover:text-foreground"
                >
                  <ChevronRightIcon class="size-3.5 transition-transform group-data-[state=open]:rotate-90" />
                  {nFiles(item.files.length)}
                </Collapsible.Trigger>
                <Collapsible.Content>
                  <ul class="mt-1 flex flex-col gap-1 text-xs text-muted-foreground">
                    {#each item.files as f (f.path)}
                      <li class="break-all">{f.path}{#if f.target} → {f.target}{/if}</li>
                    {/each}
                  </ul>
                </Collapsible.Content>
              </Collapsible.Root>
            {/if}
            {#if bad}
              <div id="{id}-problems-{i}" class="flex flex-col gap-1 text-sm text-destructive">
                <p class="flex items-center gap-2 font-medium"><TriangleAlertIcon class="size-4" />This cannot be uploaded:</p>
                <ul class="flex flex-col gap-1 pl-6">
                  {#each item.problems as p, j (j)}<li class="break-words">{p}</li>{/each}
                </ul>
              </div>
            {/if}
          </div>
        </li>
      {/each}
    </ul>
    {#if nBad && many}
      <p class="text-sm text-muted-foreground">Fix the files and add them again to upload the ones that cannot go now.</p>
    {/if}
  </div>
{/if}

{#if plan.ignored.length || skipped}
  <Collapsible.Root>
    <Collapsible.Trigger
      class="group flex w-full items-center gap-2 rounded-lg px-2 py-2 text-left text-sm text-muted-foreground hover:bg-hover hover:text-foreground"
    >
      <ChevronRightIcon class="size-4 transition-transform group-data-[state=open]:rotate-90" />
      {nFiles(plan.ignored.length + skipped)} left out
    </Collapsible.Trigger>
    <Collapsible.Content>
      <div class="flex flex-col gap-2 px-2 pt-1">
        {#if upload.ignored}<p class="text-sm text-muted-foreground">{upload.ignored}</p>{/if}
        {#if skipped}
          <p class="text-sm text-muted-foreground">
            {nFiles(skipped)} of the folder {skipped === 1 ? "was" : "were"} not sent, because {skipped === 1
              ? "it is not a file type"
              : "they are not file types"} that this repository takes.
          </p>
        {/if}
        <ul class="flex max-h-48 flex-col gap-1 overflow-y-auto text-xs text-muted-foreground">
          {#each plan.ignored as p (p)}<li class="break-all">{p}</li>{/each}
        </ul>
      </div>
    </Collapsible.Content>
  </Collapsible.Root>
{/if}
