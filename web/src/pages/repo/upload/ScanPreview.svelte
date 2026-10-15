<!--
  What the check of a dropped folder found (POST /api/repos/{name}/upload/scan): one row per
  artifact or package with its files and problems, a checkbox to leave it out, and the files
  that are left out anyway, folded.

    <ScanPreview {answer} {skipped} bind:selected format={repo.format} />

  selected[i] says whether answer.found[i] is uploaded. An item with problems cannot be ticked.
  skipped counts the files that the browser did not send for the check; they count as left out.
-->
<script lang="ts">
  import type { ScanAnswer } from "$lib/api";
  import * as Collapsible from "$lib/components/ui/collapsible";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import { Label } from "$lib/components/ui/label";
  import { Button } from "$lib/components/ui/button";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ChevronRightIcon from "@lucide/svelte/icons/chevron-right";
  import TriangleAlertIcon from "@lucide/svelte/icons/triangle-alert";
  import PackageSearchIcon from "@lucide/svelte/icons/package-search";

  let {
    answer,
    skipped = 0,
    selected = $bindable([]),
    format,
    disabled = false,
  }: {
    answer: ScanAnswer;
    skipped?: number;
    selected?: boolean[];
    format: string;
    disabled?: boolean;
  } = $props();

  const id = $props.id();

  const kindWord: Record<string, [string, string]> = {
    maven: ["artifact", "artifacts"],
    debian: ["package", "packages"],
    file: ["file", "files"],
  };
  const word = (kind: string, n: number) => (kindWord[kind] ?? ["item", "items"])[n === 1 ? 0 : 1];
  const nFiles = (n: number) => `${n.toLocaleString()} ${n === 1 ? "file" : "files"}`;

  const ignoredWhy: Record<string, string> = {
    maven:
      "Only poms and the files that belong to them are uploaded. Checksums and signatures are left out: the server writes new checksums.",
    debian: "Only .deb packages are uploaded.",
  };

  const usable = $derived(answer.found.map((f) => f.problems.length === 0));
  const nUsable = $derived(usable.filter(Boolean).length);
  const nBad = $derived(answer.found.length - nUsable);
  const nPicked = $derived(selected.filter(Boolean).length);
  const kind = $derived(answer.found[0]?.kind ?? (format === "raw" ? "file" : format));

  const setAll = (on: boolean) => (selected = usable.map((u) => on && u));
</script>

{#if answer.found.length === 0}
  <EmptyState
    icon={PackageSearchIcon}
    title="Nothing to upload"
    description={format === "maven"
      ? "The folder has no pom. Drop a Maven repository folder (com/example/app/1.0/...) or a folder with poms and their jars."
      : format === "debian"
        ? "The folder has no .deb package."
        : "The folder has no files."}
  />
{:else}
  <div class="flex flex-col gap-3">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <p class="text-sm">
        Found {answer.found.length.toLocaleString()} {word(kind, answer.found.length)}.
        {#if nBad}<span class="text-destructive">{nBad.toLocaleString()} cannot be uploaded.</span>{/if}
      </p>
      {#if nUsable > 1}
        <div class="flex gap-2">
          <Button variant="ghost" size="sm" disabled={disabled || nPicked === nUsable} onclick={() => setAll(true)}>Select all</Button>
          <Button variant="ghost" size="sm" disabled={disabled || nPicked === 0} onclick={() => setAll(false)}>Select none</Button>
        </div>
      {/if}
    </div>

    <ul class="flex max-h-[45dvh] flex-col gap-1 overflow-y-auto rounded-lg border p-2">
      {#each answer.found as f, i (i)}
        {@const bad = f.problems.length > 0}
        {@const single = f.files.length === 1 && f.files[0] === f.title}
        <li
          class="flex items-start gap-3 rounded-lg px-2 py-2 {bad
            ? 'bg-destructive/8'
            : 'hover:bg-hover has-data-[state=checked]:bg-selected has-data-[state=checked]:text-selected-foreground'}"
        >
          <Checkbox
            id="{id}-item-{i}"
            class="mt-0.5"
            checked={!bad && !!selected[i]}
            disabled={disabled || bad}
            onCheckedChange={(v) => (selected[i] = v === true)}
            aria-describedby={bad ? `${id}-problems-${i}` : undefined}
          />
          <div class="flex min-w-0 flex-1 flex-col gap-1">
            <Label for="{id}-item-{i}" class="text-[0.8125rem] font-medium break-all">{f.title}</Label>
            {#if !single}
              {#if f.files.length === 1}
                <p class="text-xs break-all text-muted-foreground">{f.files[0]}</p>
              {:else}
                <Collapsible.Root>
                  <Collapsible.Trigger
                    class="group -mx-1 flex items-center gap-1 rounded-md px-1 text-xs text-muted-foreground hover:bg-hover hover:text-foreground"
                  >
                    <ChevronRightIcon class="size-3.5 transition-transform group-data-[state=open]:rotate-90" />
                    {nFiles(f.files.length)}
                  </Collapsible.Trigger>
                  <Collapsible.Content>
                    <ul class="mt-1 flex flex-col gap-1 text-xs text-muted-foreground">
                      {#each f.files as p (p)}<li class="break-all">{p}</li>{/each}
                    </ul>
                  </Collapsible.Content>
                </Collapsible.Root>
              {/if}
            {/if}
            {#if bad}
              <div id="{id}-problems-{i}" class="flex flex-col gap-1 text-sm text-destructive">
                <p class="flex items-center gap-2 font-medium"><TriangleAlertIcon class="size-4" />This cannot be uploaded:</p>
                <ul class="flex flex-col gap-1 pl-6">
                  {#each f.problems as p, j (j)}<li class="break-words">{p}</li>{/each}
                </ul>
              </div>
            {/if}
          </div>
        </li>
      {/each}
    </ul>
    {#if nBad}
      <p class="text-sm text-muted-foreground">Fix the files in the folder and drop it again to upload the ones that cannot go now.</p>
    {/if}
  </div>
{/if}

{#if answer.ignored.length || skipped}
  <Collapsible.Root>
    <Collapsible.Trigger
      class="group flex w-full items-center gap-2 rounded-lg px-2 py-2 text-left text-sm text-muted-foreground hover:bg-hover hover:text-foreground"
    >
      <ChevronRightIcon class="size-4 transition-transform group-data-[state=open]:rotate-90" />
      {nFiles(answer.ignored.length + skipped)} left out
    </Collapsible.Trigger>
    <Collapsible.Content>
      <div class="flex flex-col gap-2 px-2 pt-1">
        {#if ignoredWhy[format]}<p class="text-sm text-muted-foreground">{ignoredWhy[format]}</p>{/if}
        {#if skipped}
          <p class="text-sm text-muted-foreground">
            {nFiles(skipped)} of the folder {skipped === 1 ? "was" : "were"} not sent for the check, because {skipped === 1
              ? "it is not a file type"
              : "they are not file types"} that this repository takes.
          </p>
        {/if}
        {#if answer.ignored.length}<p class="text-sm text-muted-foreground">The check left out these files:</p>{/if}
        <ul class="flex max-h-48 flex-col gap-1 overflow-y-auto text-xs text-muted-foreground">
          {#each answer.ignored as p (p)}<li class="break-all">{p}</li>{/each}
        </ul>
      </div>
    </Collapsible.Content>
  </Collapsible.Root>
{/if}
