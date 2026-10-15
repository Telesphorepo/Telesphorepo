<!--
  A repository: /repo/:name/*rest. rest "" or "files/<path>" is the Files tab, "use" the Use this
  repository tab, "publish" the Publish to this repository tab, "upload" the upload page. The
  repository loads once per name; its tabs and sub-pages share it.
-->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import RepoView from "./RepoView.svelte";

  let { params, query }: PageProps = $props();
</script>

{#key params.name}
  <RepoView name={params.name} rest={params.rest ?? ""} {query} />
{/key}
