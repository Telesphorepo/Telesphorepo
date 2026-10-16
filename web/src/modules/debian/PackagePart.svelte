<!--
  The Debian part of the details of a .deb in the pool: the apt setup and the install of its
  package, pinned to the file's version or always the newest (the format's artifact snippets).
-->
<script lang="ts">
  import ArtifactSnippets from "../../pages/repo/snippets/ArtifactSnippets.svelte";
  import type { FilePartProps } from "../types";

  let { repo, file }: FilePartProps = $props();
</script>

<ArtifactSnippets {repo} path={file.path} />
