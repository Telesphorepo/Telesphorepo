<!--
  The notice of Settings › Overview for a person who can open Edge delivery: while edge delivery is
  on and the deployed Worker needs a deploy (GET /api/edge/status deploy_needed), it says why, with
  a link to the page. Without a need, or when the status does not load, it shows nothing.
-->
<script lang="ts">
  import * as Alert from "$lib/components/ui/alert";
  import { Button } from "$lib/components/ui/button";
  import CloudUploadIcon from "@lucide/svelte/icons/cloud-upload";
  import { api } from "$lib/api";
  import { href } from "$lib/router.svelte";
  import { Resource } from "$lib/load.svelte";

  const status = new Resource(() => api.edge.status());
  const st = $derived(status.data);
  const shown = $derived(!!st && st.mode !== "off" && st.deploy_needed);
  const why = $derived(
    st?.worker_outdated ||
      (st?.deploy_reason ? `A ${st.deploy_reason} needs a new deploy of the edge Worker.` : "The edge Worker needs a new deploy: the buckets it reads changed."),
  );
</script>

{#if shown}
  <Alert.Root role="status" class="mb-6">
    <CloudUploadIcon />
    <Alert.Description class="text-foreground">
      {why}
      <div class="mt-3"><Button size="sm" variant="outline" href={href("/settings/edge")}>Open Edge delivery</Button></div>
    </Alert.Description>
  </Alert.Root>
{/if}
