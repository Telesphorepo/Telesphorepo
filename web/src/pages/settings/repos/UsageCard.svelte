<!--
  The Overview card of a group or a proxy that shows how a build uses it: the address, then the
  format plugin's "use" snippets (GET /api/repos/{name}/usage). A group and a proxy take no
  publishes, so the card shows no publish snippets.
-->
<script lang="ts">
  import type { Repo } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import Section from "$lib/components/Section.svelte";
  import CodeSnippet from "$lib/components/CodeSnippet.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import SnippetTabs from "../../repo/snippets/SnippetTabs.svelte";
  import { api } from "$lib/api";

  let { repo, title }: { repo: Repo; title: string } = $props();

  /* svelte-ignore state_referenced_locally */
  const usage = new Resource(() => api.repos.usage(repo.name));
  const base = $derived((usage.data?.base_url || location.origin).replace(/\/+$/, ""));
  const address = $derived(`${base}/${repo.format}/${repo.name}`);
  const use = $derived((usage.data?.snippets ?? []).filter((s) => s.purpose === "use"));
</script>

<Section {title}>
  <div class="flex flex-col gap-6">
    <CodeSnippet value={address} label="Address" />
    {#if usage.loading && !usage.data}
      <Loading rows={4} />
    {:else if usage.error && !usage.data}
      <ErrorState error={usage.error} title="The instructions did not load" onRetry={usage.reload} />
    {:else if use.length}
      <SnippetTabs snippets={use} tools={usage.data?.tools} label="Build tool" />
    {/if}
  </div>
</Section>
