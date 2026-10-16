<!--
  The Maven part of the details of a pom or a jar: how to use its artifact in a build, pinned to
  the file's version or always the newest (the format's artifact snippets).
-->
<script lang="ts">
  import ArtifactSnippets from "../../pages/repo/snippets/ArtifactSnippets.svelte";
  import type { FilePartProps } from "../types";

  let { repo, file }: FilePartProps = $props();
</script>

<ArtifactSnippets {repo} path={file.path} />
