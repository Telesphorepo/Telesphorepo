<!--
  Sign-in options (Sign-in options permission): the "sso" settings group as three steps: connect your
  provider (with its Test action), permissions from the provider (a claim and its values, each
  with server permissions), and password sign-in. Anyone at the provider can sign in with single
  sign-on. With a claim, the provider sets the server permissions of every SSO account at each
  sign-in; without one, people set them on this server. The page tests the saved settings when it
  opens and after a save, and shows that result only when the test fails; Test checks the unsaved
  edits.
-->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { api, ApiError, errorMessage, type Setting, type Settings, type SettingsMoveStarted } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import FormPage from "$lib/components/FormPage.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ConnectionTest, { type TestResult } from "$lib/components/ConnectionTest.svelte";
  import SettingsGroupForm, { type FieldLook } from "$lib/components/SettingsGroupForm.svelte";

  let {}: PageProps = $props();

  const settings = new Resource(() => api.settings.get());
  const group = $derived(settings.data?.groups.find((g) => g.id === "sso"));
  const value = (key: string) => group?.settings.find((s: Setting) => s.key === key)?.value;

  const issuer = $derived(String(value("oidc.issuer") ?? ""));
  const clientID = $derived(String(value("oidc.client_id") ?? ""));
  const secretSet = $derived((value("oidc.client_secret") as { set?: boolean } | null)?.set === true);
  const claim = $derived(String(value("oidc.permission_claim") ?? ""));

  // --- the connection test ---
  let testing = $state(false);
  let tested = $state<{ ok: boolean; error?: string; fields: Record<string, string>; quiet: boolean } | null>(null);

  // The server's field messages, in the words of this page.
  function plain(key: string, msg: string) {
    if (key === "oidc.permission_map" && /claim/.test(msg)) return "Set the claim first, or leave this empty.";
    if (/required/.test(msg)) return "Single sign-on needs this.";
    const discovery = msg.match(/OIDC discovery failed: (.*)$/);
    if (discovery) return `The provider did not answer at this address: ${discovery[1]}`;
    return msg;
  }

  // `quiet`: a test the page ran on its own, whose success needs no words.
  async function test(patch: Record<string, unknown> = {}, quiet = false) {
    testing = !quiet;
    try {
      await api.settings.test("sso", patch);
      tested = { ok: true, fields: {}, quiet };
    } catch (e) {
      // A failed test is a 400 with the fields that are wrong.
      const raw = e instanceof ApiError ? (e.fields ?? {}) : {};
      const fields = Object.fromEntries(Object.entries(raw).map(([k, m]) => [k, plain(k, m)]));
      const n = Object.keys(fields).length;
      const error = fields["oidc.issuer"]?.startsWith("The provider")
        ? "The provider did not answer. Check the provider address."
        : n
          ? `${n === 1 ? "One detail needs" : `${n} details need`} your attention below.`
          : errorMessage(e);
      tested = { ok: false, error, fields, quiet };
    } finally {
      testing = false;
    }
  }

  // Test the saved settings once they load.
  let first = true;
  $effect(() => {
    if (first && group && issuer) {
      first = false;
      test({}, true);
    }
  });

  const missing = $derived(!clientID || !secretSet);
  const target = $derived.by(() => {
    try {
      const u = new URL(issuer);
      return (u.host + u.pathname).replace(/\/$/, "");
    } catch {
      return issuer;
    }
  });
  const result = $derived<TestResult | null>(
    !tested || (tested.ok && tested.quiet) ? null : tested.ok ? { ok: true, text: `Connected to ${target}` } : { ok: false, text: tested.error ?? "The test failed." },
  );
  const origin = location.origin;
  const fields: Record<string, FieldLook> = {
    "oidc.issuer": {
      label: "Provider address",
      help: "Your provider publishes its sign-in details at this address.",
      aka: "Issuer URL",
      placeholder: "https://login.example.com/realms/main",
    },
    "oidc.client_id": { label: "App ID", help: "Your provider shows this ID when you add the app.", aka: "Client ID" },
    "oidc.client_secret": { label: "App secret", help: "Your provider shows this secret with the app ID.", aka: "Client secret" },
    "oidc.redirect_url": {
      label: "Return address",
      help: "Add this address to the app at your provider.",
      aka: "Redirect URI",
      placeholder: `${origin}/api/auth/oidc/callback`,
    },
    "oidc.scopes": { label: "Requested details", help: "Telesphorepo always asks for openid, also when the list leaves it out.", aka: "Scopes" },
    "oidc.permission_claim": {
      label: "Group field",
      help: "The sign-in token lists the groups in this field, for example groups. For Keycloak realm roles, enter realm_access.roles. Leave it empty to set permissions on this server.",
      aka: "Claim",
    },
    "oidc.permission_map": {
      label: "Permissions by group",
      help: "Each group gives its server permissions. A person gets the permissions of each of their groups.",
      permissionMap: true,
    },
    "oidc.session_ttl": { label: "Sign-in lifetime", help: "After this time, a person must sign in with single sign-on again." },
    "signin.password_login": {
      label: "Password sign-in",
      help: "With people who publish only, a person who manages (who holds a permission that needs a second factor) signs in with single sign-on.",
    },
  };

  const sections = $derived([
    {
      title: "Connect your provider",
      description: "Add Telesphorepo as an app at your identity provider (Okta, Entra ID, Keycloak, Google), then copy its details here.",
      keys: ["oidc.issuer", "oidc.client_id", "oidc.client_secret", "oidc.redirect_url", "oidc.scopes"],
      done: tested?.ok === true && !missing,
    },
    {
      title: "Permissions from the provider",
      description:
        "Anyone at your provider can sign in. With a group field, the provider sets the server permissions of each such person at every sign-in, and nobody changes them here. Without one, you give permissions on the People page.",
      keys: ["oidc.permission_claim", "oidc.permission_map"],
      done: claim !== "",
    },
    { title: "How people sign in", keys: ["oidc.session_ttl", "signin.password_login"] },
  ]);

  const effects = (patch: Record<string, unknown>) => [
    ...(patch["signin.password_login"] === "publishers"
      ? [
          "People who manage can no longer sign in with a password",
          "To allow password sign-in for everyone again without single sign-on, set TR_SIGNIN_PASSWORD_LOGIN=all and restart the server",
        ]
      : []),
    ...("oidc.permission_claim" in patch || "oidc.permission_map" in patch
      ? ["The server permissions of single sign-on accounts change at their next sign-in"]
      : []),
  ];

  function saved(result: Settings | SettingsMoveStarted) {
    if ("groups" in result) settings.data = result;
    else settings.reload();
    tested = null;
    if (issuer) test({}, true);
  }
</script>

<PageHeader title="Sign-in options" description="How people sign in: with single sign-on, a password, or both." />

{#if settings.loading && !settings.data}
  <Loading rows={6} />
{:else if settings.error && !settings.data}
  <ErrorState error={settings.error} onRetry={settings.reload} />
{:else if group}
  <FormPage>
    <SettingsGroupForm settings={group.settings} label="Single sign-on settings" {sections} steps extraAt={0} {fields} akaLabel="In your provider" errors={tested?.fields} {effects} onSaved={saved}>
      {#snippet extra(patch)}
        {@const edits = patch()}
        <ConnectionTest
          {testing}
          {result}
          label={Object.keys(edits).length > 0 ? "Test your edits" : "Test connection"}
          disabled={!issuer && !edits["oidc.issuer"]}
          onTest={() => test(patch())}
        />
      {/snippet}
    </SettingsGroupForm>
  </FormPage>
{:else}
  <ErrorState error="The server sent no single sign-on settings." onRetry={settings.reload} />
{/if}
