<!-- Admin: single sign-on settings (settings group "sso") and the discovery test. -->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { api, errorMessage, type Settings, type SettingsMoveStarted } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import { Button } from "$lib/components/ui/button";
  import { Spinner } from "$lib/components/ui/spinner";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import LoadingRows from "$lib/components/LoadingRows.svelte";
  import SettingsGroupForm from "$lib/components/SettingsGroupForm.svelte";
  import CircleCheckIcon from "@lucide/svelte/icons/circle-check";

  let {}: PageProps = $props();

  const settings = new Resource(() => api.settings.get());
  const group = $derived(settings.data?.groups.find((g) => g.name === "sso"));

  // Settings of the group that no section names go to the first section.
  const sections = [
    {
      title: "Identity provider",
      description: "Set the issuer to turn on \"Sign in with SSO\". Saving checks the issuer's discovery document.",
      keys: ["oidc.issuer", "oidc.client_id", "oidc.client_secret", "oidc.redirect_url", "oidc.scopes"],
    },
    {
      title: "Admin rights",
      description: "An SSO account is an admin when its token matches a claim value or a verified email below.",
      keys: ["oidc.admin_claim", "oidc.admin_values", "oidc.admin_emails"],
    },
    {
      title: "Sessions and passwords",
      keys: ["oidc.session_ttl", "admin_password_login"],
    },
  ];

  const effects = (patch: Record<string, unknown>) =>
    patch["admin_password_login"] === false ? ["Local admins can no longer sign in to the UI with a password"] : [];

  function saved(result: Settings | SettingsMoveStarted) {
    if ("groups" in result) settings.data = result;
    else settings.reload();
    testOk = false;
  }

  let testing = $state(false);
  let testOk = $state(false);
  let testError = $state("");

  async function test(patch: Record<string, unknown>) {
    testing = true;
    testOk = false;
    testError = "";
    try {
      const r = await api.settings.testOIDC(patch);
      if (r.ok) testOk = true;
      else testError = r.error ?? "The issuer did not answer as expected.";
    } catch (e) {
      testError = errorMessage(e);
    } finally {
      testing = false;
    }
  }
</script>

<PageHeader title="Single sign-on" description="Let administrators sign in with your OpenID Connect identity provider." />

{#if settings.loading && !settings.data}
  <LoadingRows rows={6} />
{:else if settings.error && !settings.data}
  <ErrorState error={settings.error} onRetry={settings.reload} />
{:else if group}
  <Section narrow>
    <SettingsGroupForm settings={group.settings} label="SSO settings" {sections} {effects} onSaved={saved}>
      {#snippet extra(patch)}
        <Button type="button" variant="outline" disabled={testing} onclick={() => test(patch())}>
          {#if testing}<Spinner />{/if}
          Test the connection
        </Button>
      {/snippet}
    </SettingsGroupForm>
    <div class="mt-4" aria-live="polite">
      {#if testOk}
        <p class="flex items-center gap-2 text-sm">
          <CircleCheckIcon class="size-4 shrink-0 text-link" />
          The issuer answered and the settings are valid.
        </p>
      {:else if testError}
        <ErrorState inline error={testError} />
      {/if}
    </div>
  </Section>
{:else}
  <ErrorState error="The server sent no SSO settings." title="SSO is not available" onRetry={settings.reload} />
{/if}
