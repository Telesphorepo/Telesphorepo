<!--
  After a merge: the setup line that points a project at the merged repository (the Set up a
  project card of its Publish tab), for the projects that still publish to the old name.
-->
<script lang="ts">
  import { api, type Repo } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ProjectSetup from "../../repo/ProjectSetup.svelte";

  let { name, query }: { name: string; query: URLSearchParams } = $props();

  // svelte-ignore state_referenced_locally
  const repo = new Resource<Repo>(() => api.repos.get(name));
  // svelte-ignore state_referenced_locally
  const usage = new Resource(() => api.repos.usage(name));
  const computer = $derived((usage.data?.snippets ?? []).filter((s) => s.purpose === "publish" && s.via === "computer"));
</script>

{#if (repo.loading && !repo.data) || (usage.loading && !usage.data)}
  <Loading rows={3} />
{:else if repo.error || usage.error}
  <ErrorState error={repo.error || usage.error} title="The setup did not load" onRetry={() => (repo.reload(), usage.reload())} />
{:else if repo.data && usage.data}
  <ProjectSetup repo={repo.data} baseURL={usage.data.base_url || location.origin} {computer} tools={usage.data.tools ?? []} {query} />
{/if}
