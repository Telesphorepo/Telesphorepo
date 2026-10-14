<!-- Repository settings, "How to use" tab (id status): the client setup snippets, and what the format reports. -->
<script lang="ts">
  import { api, type Repo } from "$lib/api";
  import { formatCount } from "$lib/format";
  import { Resource } from "$lib/load.svelte";
  import CopyField from "$lib/components/CopyField.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import LoadingRows from "$lib/components/LoadingRows.svelte";
  import Section from "$lib/components/Section.svelte";

  let { repo }: { repo: Repo } = $props();

  const status = new Resource(() => api.repos.status(repo.name));
  const usage = new Resource(() => api.repos.usage(repo.name));

  const label = (k: string) => {
    const s = k.replace(/_/g, " ");
    return s.charAt(0).toUpperCase() + s.slice(1);
  };
  const show = (v: unknown): string =>
    typeof v === "number"
      ? formatCount(v)
      : typeof v === "boolean"
        ? v
          ? "Yes"
          : "No"
        : typeof v === "string"
          ? v || "None"
          : JSON.stringify(v);
  const entries = $derived(Object.entries(status.data ?? {}));
</script>

<Section narrow title="How to use" description="Copy these into a build or a system to download from {repo.name}.">
  {#if usage.loading}
    <LoadingRows rows={3} />
  {:else if usage.error}
    <ErrorState error={usage.error} onRetry={usage.reload} />
  {:else}
    <div class="flex flex-col gap-4">
      <CopyField label="Server address" value={usage.data?.base_url ?? ""} />
      {#each usage.data?.snippets ?? [] as s (s.title)}
        <CopyField label={s.title} value={s.code} block />
      {/each}
    </div>
  {/if}
</Section>

<Section narrow title="Repository status" description="What the {repo.format} format reports about this repository.">
  {#if status.loading}
    <LoadingRows rows={2} />
  {:else if status.error}
    <ErrorState error={status.error} onRetry={status.reload} />
  {:else if entries.length === 0}
    <EmptyState title="Nothing to report" description="The {repo.format} format reports no status for a repository." />
  {:else}
    <dl class="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
      {#each entries as [k, v] (k)}
        <dt class="text-muted-foreground">{label(k)}</dt>
        <dd class="min-w-0 font-medium break-words">{show(v)}</dd>
      {/each}
    </dl>
  {/if}
</Section>