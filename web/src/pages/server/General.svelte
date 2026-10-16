<!--
  General: the settings groups that no page of their own edits (their page is ""), each with the
  label and the help the server gives: the public address and limits, publishing lifetimes,
  Activity retention and the Nexus sync interval. A person sees the groups whose
  permission they hold. Storage, Sign-in options and Network and proxy have their own pages. One
  form saves them all, with a section per settings group.
-->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { api, type Settings, type SettingsMoveStarted } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import FormPage from "$lib/components/FormPage.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import SettingsGroupForm from "$lib/components/SettingsGroupForm.svelte";
  import SlidersIcon from "@lucide/svelte/icons/sliders-horizontal";

  let {}: PageProps = $props();

  // The groups that no page of their own edits (page ""), in module order, one section each.
  const settings = new Resource(() => api.settings.get());
  const groups = $derived((settings.data?.groups ?? []).filter((g) => g.page === "" && g.settings.length > 0));
  const all = $derived(groups.flatMap((g) => g.settings));
  const sections = $derived(groups.map((g) => ({ title: g.label, keys: g.settings.map((s) => s.key) })));

  function saved(result: Settings | SettingsMoveStarted) {
    if ("groups" in result) settings.data = result;
    else settings.reload();
  }
</script>

<PageHeader title="General" description="Limits and lifetimes of the whole server." />

{#if settings.loading && !settings.data}
  <Loading rows={8} />
{:else if settings.error && !settings.data}
  <ErrorState error={settings.error} onRetry={settings.reload} />
{:else if all.length === 0}
  <CompactEmpty icon={SlidersIcon} text="The server reports no general settings." />
{:else}
  <FormPage>
    <SettingsGroupForm settings={all} label="General settings" {sections} onSaved={saved} />
  </FormPage>
{/if}
