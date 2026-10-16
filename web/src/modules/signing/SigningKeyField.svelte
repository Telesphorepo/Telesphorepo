<!--
  The signing key of a new repository (the repo field signing_key): a key that exists, or a new key
  that New repository creates before the repository (prepare) and deletes again when the create
  fails (undo).
-->
<script lang="ts">
  import { api, errorMessage } from "$lib/api";
  import { auth } from "$lib/auth.svelte";
  import { Resource } from "$lib/load.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import SigningKeyChoice, { type KeyChoice } from "$lib/components/SigningKeyChoice.svelte";
  import type { RepoFieldProps } from "../types";

  let { repoName, onchange }: RepoFieldProps = $props();

  const keys = new Resource(() => api.signingKeys.list());
  let choice = $state<KeyChoice>({ mode: "existing", name: "" });

  $effect(() => {
    const name = choice.name.trim();
    const create = choice.mode === "new";
    let created = "";
    onchange({
      value: name || undefined,
      ready: name !== "",
      prepare: create
        ? async () => {
            created = (await api.signingKeys.create({ name })).name;
          }
        : undefined,
      undo: create
        ? async () => {
            if (!created) return "";
            try {
              await api.signingKeys.delete(created);
              return "";
            } catch (e) {
              return `The new signing key ${created} stays; delete it in Signing keys if you do not need it (${errorMessage(e)}).`;
            } finally {
              created = "";
              keys.reload();
            }
          }
        : undefined,
    });
  });
</script>

{#if keys.loading && !keys.data}
  <Loading rows={2} />
{:else if keys.error && !keys.data}
  <ErrorState error={keys.error} title="The signing keys did not load" onRetry={keys.reload} />
{:else}
  <SigningKeyChoice keys={keys.data ?? []} {repoName} canCreate={auth.can("signing.manage")} bind:choice />
{/if}
