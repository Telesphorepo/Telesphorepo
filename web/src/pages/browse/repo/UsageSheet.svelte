<!--
  "How to use": a side sheet with the server address and the client setup of one repo
  (GET /api/repos/{name}/usage). The repo page loads the usage once and passes it in.
-->
<script lang="ts">
  import type { Repo, Usage } from "$lib/api";
  import type { Resource } from "$lib/load.svelte";
  import { href } from "$lib/router.svelte";
  import * as Sheet from "$lib/components/ui/sheet";
  import CopyField from "$lib/components/CopyField.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import LoadingRows from "$lib/components/LoadingRows.svelte";

  let { open = $bindable(false), repo, usage }: { open?: boolean; repo: Repo; usage: Resource<Usage> } = $props();
</script>

<Sheet.Root bind:open>
  <Sheet.Content side="right" class="gap-0 overflow-y-auto data-[side=right]:w-full data-[side=right]:sm:max-w-xl">
    <Sheet.Header class="border-b pr-12">
      <Sheet.Title>Use {repo.name}</Sheet.Title>
      <Sheet.Description>Copy these into your build or your system to download from this repository.</Sheet.Description>
    </Sheet.Header>
    <div class="flex flex-col gap-4 p-6">
      {#if usage.loading}
        <LoadingRows rows={4} />
      {:else if usage.error}
        <ErrorState error={usage.error} title="The setup did not load" onRetry={usage.reload} />
      {:else if usage.data}
        <CopyField label="Repository address" value={usage.data.base_url.replace(/\/+$/, "") + "/" + repo.format + "/" + repo.name} />
        {#each usage.data.snippets as s (s.title)}
          <CopyField label={s.title} value={s.code} block />
        {/each}
        {#if usage.data.snippets.length === 0}
          <EmptyState title="No setup to show" description="This format has no client setup. Open a file to see how to download it." />
        {/if}
        {#if !repo.anonymous_read}
          <p class="text-sm text-muted-foreground">
            This repository is private. Your build signs in with your username and a
            <a href={href("/settings/tokens")} class="text-link underline-offset-4 hover:underline">download token</a>.
          </p>
        {/if}
      {/if}
    </div>
  </Sheet.Content>
</Sheet.Root>
