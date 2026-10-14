<!--
  A repo as a file tree. params.name is the repo; params.path is a folder (ends with "/", "" is
  the root) or a file, which opens the file panel over its folder.
-->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import RepoView from "./repo/RepoView.svelte";

  let { params }: PageProps = $props();

  const rest = $derived(params.path ?? "");
  const isFile = $derived(rest !== "" && !rest.endsWith("/"));
  const folder = $derived(isFile ? rest.slice(0, rest.lastIndexOf("/") + 1) : rest);
</script>

<!-- A new repo starts fresh; folders and files of one repo keep the loaded repo. -->
{#key params.name}
  <RepoView name={params.name} {folder} file={isFile ? rest : ""} />
{/key}
