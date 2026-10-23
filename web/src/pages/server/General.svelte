<!--
  General: the settings groups that no page of their own edits (their page is ""), each with the
  label and the help the server gives: the server identity (name and logo), the public address and
  limits, publishing lifetimes, Activity retention and the Nexus sync interval. A person sees the
  groups whose permission they hold. Storage, Sign-in options and Network and proxy have their own
  pages. One form saves them all, with a section per settings group; Server identity comes first.
  The name saves with the form; the logo is its own action (LogoField) that applies at once.
-->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { api, type Setting, type Settings, type SettingsMoveStarted } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import { brand } from "$lib/brand.svelte";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import FormPage from "$lib/components/FormPage.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import SettingsGroupForm from "$lib/components/SettingsGroupForm.svelte";
  import LogoField from "./general/LogoField.svelte";
  import SlidersIcon from "@lucide/svelte/icons/sliders-horizontal";

  let {}: PageProps = $props();

  // The settings of the server identity, in their own section at the top.
  const identityKeys = ["server_name", "server_logo"];

  // The groups that no page of their own edits (page ""), in module order, one section each.
  const settings = new Resource(() => api.settings.get());
  const groups = $derived((settings.data?.groups ?? []).filter((g) => g.page === "" && g.settings.length > 0));
  const all = $derived(groups.flatMap((g) => g.settings));
  const sections = $derived.by(() => {
    const identity = all.filter((s) => identityKeys.includes(s.key)).map((s) => s.key);
    const rest = groups
      .map((g) => ({ title: g.label, keys: g.settings.map((s) => s.key).filter((k) => !identityKeys.includes(k)) }))
      .filter((g) => g.keys.length > 0);
    return identity.length ? [{ title: "Server identity", keys: identity }, ...rest] : rest;
  });

  function saved(result: Settings | SettingsMoveStarted) {
    if ("groups" in result) settings.data = result;
    else settings.reload();
    api.identity.get().then((id) => brand.set(id), () => {});
  }
</script>

{#snippet logo(s: Setting, id: string)}
  <LogoField {id} disabled={s.locked} describedby={s.help ? `${id}-help` : undefined} onchange={settings.reload} />
{/snippet}

<PageHeader title="General" />

{#if settings.loading && !settings.data}
  <Loading rows={8} />
{:else if settings.error && !settings.data}
  <ErrorState error={settings.error} onRetry={settings.reload} />
{:else if all.length === 0}
  <CompactEmpty icon={SlidersIcon} text="This server has no general settings." />
{:else}
  <FormPage>
    <SettingsGroupForm settings={all} label="General settings" {sections} fields={{ server_logo: { control: logo } }} onSaved={saved} />
  </FormPage>
{/if}
