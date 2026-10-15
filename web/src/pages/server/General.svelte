<!--
  General (General permission): the server settings that no other page holds: the public address,
  upload limits, the server log, sign-in sessions, publishing lifetimes and Activity retention.
  Storage, Sign-in options and the trusted proxies (Network and proxy) have their own pages. One
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
  import SettingsGroupForm, { type FieldLook } from "$lib/components/SettingsGroupForm.svelte";
  import SlidersIcon from "@lucide/svelte/icons/sliders-horizontal";

  let {}: PageProps = $props();

  // Groups and settings that have a page of their own.
  const ownGroups = new Set(["storage", "sso"]);
  const ownKeys = new Set(["trusted_proxies"]);

  const settings = new Resource(() => api.settings.get());
  const groups = $derived(
    (settings.data?.groups ?? [])
      .filter((g) => !ownGroups.has(g.name))
      .map((g) => ({ ...g, settings: g.settings.filter((s) => !ownKeys.has(s.key)) }))
      .filter((g) => g.settings.length > 0),
  );
  const all = $derived(groups.flatMap((g) => g.settings));
  // Section titles in the words of this page; the page title already says "General".
  const titles: Record<string, string> = { general: "Address and limits", security: "Sign-in and Activity", publishing: "Publishing" };
  const sections = $derived(groups.map((g) => ({ title: titles[g.name] ?? g.label, keys: g.settings.map((s) => s.key) })));

  const fields: Record<string, FieldLook> = {
    base_url: { label: "Public address", help: "Builds and browsers reach the server at this address, for example https://repo.example.com." },
    "log.level": { label: "Server log level", help: "The server log keeps messages of this level and more important ones." },
    max_upload_size: { label: "Largest upload", help: "The server refuses a bigger file before it stores anything." },
    max_json_size: { label: "Largest API request", help: "The server refuses a bigger request to the JSON API." },
    "nexus.sync_interval": { label: "Nexus sync interval", help: "The server syncs each Nexus server this often. The minimum is 1 minute." },
    "cookie.secure": { label: "HTTPS-only sign-in cookie", help: "Turn this off only when the server has no HTTPS." },
    session_ttl: { label: "Sign-in lifetime", help: "After this time, a person must sign in again." },
    "audit.retention": { label: "Activity history", help: "The server deletes events older than this every hour." },
    "publish.session_ttl": { label: "Publish token lifetime", help: "The maximum is 1 hour. A client ends its token when the publish is done." },
    "publish.device_ttl": { label: "Device approval lifetime", help: "After this time, the developer must approve the device again." },
  };

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
    <SettingsGroupForm settings={all} label="General settings" {sections} {fields} onSaved={saved} />
  </FormPage>
{/if}
