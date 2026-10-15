<!--
  How to use one artifact in a build: the format plugin's snippets for an artifact or version
  folder (or a file in one), with the client tabs and a version choice. "This version" pins the
  version (the default); "Always the newest" names Gradle's latest.release or Maven's RELEASE,
  with the plugin's note that builds stop being reproducible. Each snippet includes the
  repository block it needs, so it works when pasted. Renders nothing for a path that is not an
  artifact.
-->
<script lang="ts">
  import type { Repo } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import SnippetTabs from "./SnippetTabs.svelte";
  import Segmented from "./Segmented.svelte";
  import { browse, type VersionMode } from "../browse";

  let { repo, path }: { repo: Repo; path: string } = $props();

  let mode = $state<VersionMode>("pinned");
  /* svelte-ignore state_referenced_locally */
  const snippets = new Resource(() => browse.snippets(repo.name, path, mode));

  let first = true;
  $effect(() => {
    void mode;
    if (first) {
      first = false;
      return;
    }
    snippets.reload();
  });

  const data = $derived(snippets.data);
  // A parent POM takes a fixed version, so it gets no choice.
  const choice = $derived(!!data?.artifact && data.artifact.kind !== "parent");
  // The pinned version, kept while "Always the newest" shows.
  let pinnedSeen = $state("");
  $effect(() => {
    if (data?.version_mode === "pinned" && data.version) pinnedSeen = data.version;
  });
</script>

{#if snippets.loading && !data}
  <Loading rows={3} />
{:else if snippets.error && !data}
  <ErrorState inline error={snippets.error} />
{:else if data?.artifact && data.snippets.length}
  <section class="flex min-w-0 flex-col gap-4" aria-label="Use {data.artifact.title} in a build">
    <h3 class="text-section">Use it in a build</h3>
    {#if choice}
      <Segmented
        bind:value={mode}
        label="Version in the snippet"
        options={[
          { value: "pinned", label: pinnedSeen || "This version", title: "Pin this version: builds stay reproducible" },
          { value: "latest", label: "Always the newest", title: "Take the newest version at each build" },
        ]}
      />
    {/if}
    <div class={snippets.loading ? "opacity-60 transition-opacity" : "transition-opacity"}>
      <SnippetTabs snippets={data.snippets} label="Build tool" fill />
    </div>
    {#if snippets.error}<ErrorState inline error={snippets.error} />{/if}
  </section>
{/if}
