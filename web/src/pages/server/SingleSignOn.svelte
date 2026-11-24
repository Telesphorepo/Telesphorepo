<!--
  Sign-in options (Sign-in options permission): the "sso" settings group as three steps: connect your
  provider (the return address to copy, the provider's details and its Test action), server
  permissions (a choice: set on this server, or from groups at the provider with the group field and
  the server permissions of each group), and how people sign in. Anyone at the provider can sign in with single
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
  import SettingsGroupForm, { type FieldLook, type GroupMap, type SectionDraft } from "$lib/components/SettingsGroupForm.svelte";
  import CodeSnippet from "$lib/components/CodeSnippet.svelte";
  import FormRow from "$lib/components/FormRow.svelte";
  import OptionRow from "$lib/components/OptionRow.svelte";
  import PermissionMapInput from "$lib/components/PermissionMapInput.svelte";
  import * as RadioGroup from "$lib/components/ui/radio-group";
  import { Input } from "$lib/components/ui/input";
  import { Button } from "$lib/components/ui/button";
  import ExternalLinkIcon from "@lucide/svelte/icons/external-link";
  import type { PermId } from "$lib/api";

  let {}: PageProps = $props();

  const settings = new Resource(() => api.settings.get());
  const group = $derived(settings.data?.groups.find((g) => g.id === "sso"));
  const value = (key: string) => group?.settings.find((s: Setting) => s.key === key)?.value;

  const issuer = $derived(String(value("oidc.issuer") ?? ""));
  const clientID = $derived(String(value("oidc.client_id") ?? ""));
  const secretSet = $derived((value("oidc.client_secret") as { set?: boolean } | null)?.set === true);
  const claim = $derived(String(value("oidc.permission_claim") ?? ""));
  // The choice of the Server permissions step until the group field says it: "provider" with an
  // empty group field shows its fields.
  // Settings that a step shows with its own controls, not as fields.
  const ownControls = ["oidc.redirect_url", "oidc.permission_claim", "oidc.permission_map"];
  let picked = $state<"server" | "provider" | null>(null);
  const idBase = $props.id();
  const savedMap = $derived((value("oidc.permission_map") ?? {}) as GroupMap);
  const permsLocked = $derived(!!group?.settings.some((s: Setting) => (s.key === "oidc.permission_claim" || s.key === "oidc.permission_map") && s.locked));

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
  // The return address is fixed, so it shows above the fields as a value to copy, not as a field: the one the server uses, which the provider must hold. It is
  // oidc.redirect_url when set (only through the API or an environment variable), else the
  // public address + /api/auth/oidc/callback. A person who cannot read Settings › General gets no
  // base_url and sees this address.
  const baseURL = $derived(settings.data?.groups.flatMap((g) => g.settings).find((s: Setting) => s.key === "base_url"));
  const publicAddress = $derived(baseURL ? String(baseURL.value ?? "") : location.origin);
  const returnAddress = $derived(
    String(value("oidc.redirect_url") ?? "") || (publicAddress ? publicAddress.replace(/\/$/, "") + "/api/auth/oidc/callback" : ""),
  );
  const fields: Record<string, FieldLook> = {
    "oidc.issuer": {
      label: "Provider address",
      help: "Your provider publishes its sign-in details at this address.",
      aka: "Issuer URL",
      placeholder: "https://login.example.com",
    },
    "oidc.client_id": { label: "App ID", help: "Your provider shows this ID when you add the app.", aka: "Client ID" },
    "oidc.client_secret": { label: "App secret", help: "Your provider shows this secret with the app ID.", aka: "Client secret" },
    "oidc.scopes": { label: "Requested details", help: "Telesphorepo always asks for openid, also when the list leaves it out.", aka: "Scopes" },
    // Drawn by the Server permissions step; the labels name them in the confirm dialog and errors.
    "oidc.permission_claim": { label: "Group field" },
    "oidc.permission_map": { label: "Permissions of each group" },
    "oidc.session_ttl": { label: "Sign-in lifetime", help: "A person who does not use the app for this time must sign in with single sign-on again." },
    "signin.password_login": {
      label: "Password sign-in",
      help: "With people who publish only, a person who manages (who holds a permission that needs a second factor) signs in with single sign-on.",
    },
  };

  const sections = $derived([
    {
      title: "Connect your provider",
      description: "Add Telesphorepo as an app at your identity provider, then copy its details here.",
      keys: ["oidc.issuer", "oidc.client_id", "oidc.client_secret", "oidc.scopes"],
      intro: returnIntro,
      done: tested?.ok === true && !missing,
    },
    {
      title: "Server permissions",
      description: "Anyone at your provider can sign in. Choose who sets what they may do on the whole server; access to one repository is always given on its Access tab.",
      keys: ["oidc.permission_claim", "oidc.permission_map"],
      intro: permissionsIntro,
    },
    { title: "How people sign in", keys: ["oidc.session_ttl", "oidc.approval_max_age", "signin.password_login"] },
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

{#snippet returnIntro(_draft: SectionDraft)}
  <FormRow label="Return address" help="Copy it into the app at your provider." aka="Redirect URI" akaLabel="In your provider">
    {#if returnAddress}
      <CodeSnippet value={returnAddress} />
    {:else}
      <p class="text-body text-muted-foreground">Set the public address on <a class="underline underline-offset-2" href="#/settings/general">General</a> first: the return address starts with it.</p>
    {/if}
  </FormRow>
{/snippet}

<!--
  Who sets the server permissions of single sign-on accounts. The choice is not a setting of its
  own: "From groups at your provider" is a group field that is set. Choosing "Set on this server"
  clears the group field and the groups; choosing the provider again brings back the saved ones.
-->
{#snippet permissionsIntro(draft: SectionDraft)}
  {@const fromProvider = draft.get("oidc.permission_claim") !== "" || picked === "provider"}
  <RadioGroup.Root
    class="gap-1"
    aria-label="Who sets server permissions"
    value={fromProvider ? "provider" : "server"}
    disabled={permsLocked}
    onValueChange={(v) => {
      picked = v as "server" | "provider";
      draft.set("oidc.permission_claim", v === "provider" ? claim : "");
      draft.set("oidc.permission_map", v === "provider" ? savedMap : {});
    }}
  >
    <OptionRow id="{idBase}-server" label="Set on this server" description="You give each person their server permissions on the People page.">
      {#snippet control()}<RadioGroup.Item id="{idBase}-server" value="server" />{/snippet}
    </OptionRow>
    <OptionRow
      id="{idBase}-provider"
      label="From groups at your provider"
      description="Each sign-in sets the person's server permissions from their groups at your provider. Nobody can change them here."
    >
      {#snippet control()}<RadioGroup.Item id="{idBase}-provider" value="provider" />{/snippet}
    </OptionRow>
  </RadioGroup.Root>

  {#if fromProvider}
    <div class="mt-6 flex flex-col gap-6">
      <FormRow
        label="Group field"
        for="{idBase}-claim"
        help="The field of the sign-in token that lists the person's groups, such as groups."
        aka="Claim"
        akaLabel="In your provider"
        error={draft.error("oidc.permission_claim")}
      >
        <Input
          id="{idBase}-claim"
          placeholder="groups"
          autocomplete="off"
          spellcheck={false}
          disabled={permsLocked}
          aria-describedby="{idBase}-claim-help"
          value={draft.get("oidc.permission_claim") as string}
          oninput={(e) => draft.set("oidc.permission_claim", e.currentTarget.value)}
        />
      </FormRow>
      <FormRow
        label="Permissions of each group"
        for="{idBase}-map"
        help="A person gets the permissions of every group they are in. A group that is not listed gives none."
        error={draft.error("oidc.permission_map")}
      >
        <PermissionMapInput
          id="{idBase}-map"
          describedby="{idBase}-map-help"
          disabled={permsLocked}
          value={draft.get("oidc.permission_map") as Record<string, PermId[]>}
          onchange={(v) => draft.set("oidc.permission_map", v)}
        />
      </FormRow>
    </div>
  {/if}
{/snippet}

<PageHeader title="Sign-in options" description="Single sign-on with an OpenID Connect provider, and the server permissions its accounts get.">
  {#snippet actions()}
    <Button variant="outline" href="https://docs.telesphorepo.com/single-sign-on/" target="_blank" rel="noopener noreferrer">
      Setup guide<ExternalLinkIcon aria-hidden="true" />
    </Button>
  {/snippet}
</PageHeader>

{#if settings.loading && !settings.data}
  <Loading rows={6} />
{:else if settings.error && !settings.data}
  <ErrorState error={settings.error} onRetry={settings.reload} />
{:else if group}
  <FormPage>
    <SettingsGroupForm settings={group.settings} visible={(s) => !ownControls.includes(s.key)} label="Single sign-on settings" {sections} steps extraAt={0} {fields} akaLabel="In your provider" errors={tested?.fields} {effects} onSaved={saved}>
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
