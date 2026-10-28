<!--
  The Remote card of a proxy's Overview: the remote address, when the remote last answered (or
  its last error), and "Test the remote" (POST /api/repos/{name}/proxy/test). It also shows the
  proxy_note of the format (e.g. who signs the files). The Remote tab edits the remote.
-->
<script lang="ts">
  import { api, errorMessage, type Repo } from "$lib/api";
  import { formats } from "$lib/formats/registry.svelte";
  import Section from "$lib/components/Section.svelte";
  import CopyValue from "$lib/components/CopyValue.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import ConnectionTest, { type TestResult } from "$lib/components/ConnectionTest.svelte";
  import TriangleAlertIcon from "@lucide/svelte/icons/triangle-alert";

  let { repo, onChanged }: { repo: Repo; onChanged: () => void } = $props();

  const p = $derived(repo.proxy);
  // The last error counts when it is newer than the last answer.
  const failing = $derived(
    !!p?.last_error && (!p.last_ok_at || (!!p.last_error_at && new Date(p.last_error_at) >= new Date(p.last_ok_at))),
  );

  let testing = $state(false);
  let result = $state<TestResult | null>(null);

  async function test() {
    testing = true;
    try {
      await api.repos.testProxy(repo.name);
      result = { ok: true, text: "The remote answered." };
    } catch (e) {
      result = { ok: false, text: `${errorMessage(e)} Check the address and the credentials in the Remote tab.` };
    } finally {
      testing = false;
      onChanged();
    }
  }
</script>

{#if p}
  <Section title="Remote">
    {#snippet footer()}<ConnectionTest label="Test the remote" {testing} {result} onTest={test} />{/snippet}
    <div class="flex flex-col gap-4">
      <CopyValue value={p.remote_url} label="remote address" />
      {#if failing}
        <p class="flex items-start gap-1.5 text-body text-destructive">
          <TriangleAlertIcon aria-hidden="true" class="mt-0.5 size-4 shrink-0" />
          <span>{p.last_error}{#if p.last_error_at}<span class="text-muted-foreground">{" · "}<RelativeTime at={p.last_error_at} /></span>{/if}</span>
        </p>
      {:else if p.last_ok_at}
        <p class="text-body text-muted-foreground">Last answered <RelativeTime at={p.last_ok_at} /></p>
      {:else}
        <p class="text-body text-muted-foreground">No build has asked for a file yet.</p>
      {/if}
      {#if formats.get(repo.format)?.proxy_note}
        <p class="text-body text-muted-foreground">{formats.get(repo.format)?.proxy_note}</p>
      {/if}
    </div>
  </Section>
{/if}
