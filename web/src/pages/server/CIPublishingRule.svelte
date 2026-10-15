<!--
  A CI pipeline: /settings/ci-pipelines/:id, or a new one at /settings/ci-pipelines/new. A new
  one takes ?repo=<name> (that repository starts chosen) and ?back=<path> (a Settings page that
  Create and Cancel return to).
-->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import RuleForm from "./ci/RuleForm.svelte";

  let { params, query }: PageProps = $props();
</script>

{#key params.id ?? "new"}
  <RuleForm id={params.id ? Number(params.id) : null} startRepo={query.get("repo")} back={query.get("back")} />
{/key}
