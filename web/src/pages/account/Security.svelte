<!--
  Your account: Sign-in and security. Each area is a Section: how you sign in (the password row,
  whose Change opens a dialog), the second-step methods (authenticator apps and passkeys) as one
  list, then the recovery codes. Without a second step, that section comes first. The second step
  also confirms a publish token and a device approval. A setup-only session sees only this page.
  With ?back=<UI path> (from a device approval), the page offers a way back once a second step is on.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { href, type PageProps } from "$lib/router.svelte";
  import * as Alert from "$lib/components/ui/alert";
  import CircleCheckIcon from "@lucide/svelte/icons/circle-check";
  import ShieldCheckIcon from "@lucide/svelte/icons/shield-check";
  import { auth } from "$lib/auth.svelte";
  import { api, errorMessage, type Passkey, type TOTPFactor, type TOTPSetup } from "$lib/api";
  import { Resource, type TableSource } from "$lib/load.svelte";
  import { createPasskey, passkeysSupported } from "$lib/webauthn";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Spinner } from "$lib/components/ui/spinner";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import FormField from "$lib/components/FormField.svelte";
  import ItemList from "$lib/components/ItemList.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import CodeBlock from "$lib/components/CodeBlock.svelte";
  import CodeSnippet from "$lib/components/CodeSnippet.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import ShortFormDialog from "$lib/components/ShortFormDialog.svelte";
  import QR from "$lib/components/QR.svelte";
  import SmartphoneIcon from "@lucide/svelte/icons/smartphone";
  import KeyRoundIcon from "@lucide/svelte/icons/key-round";
  import LifeBuoyIcon from "@lucide/svelte/icons/life-buoy";
  import PlusIcon from "@lucide/svelte/icons/plus";

  let { query }: PageProps = $props();

  // The page that sent the user here to set up a second step (?back=, a UI path), such as the
  // approval of a device. It shows as a way back once the second step is on.
  const back = $derived.by(() => {
    const b = query.get("back") ?? "";
    return /^\/[^/]/.test(b) && !b.includes("//") ? b : "";
  });
  // Opened for an account that must add a second step first (right after its sign-in): the page
  // says why, and once a method is added it offers the way on.
  const required = auth.setupOnly;
  const onward = $derived(back || "/");

  const user = $derived(auth.user);
  const local = $derived(user?.kind === "local");

  const TOTAL_CODES = 10;
  const factors = new Resource(() => api.security.get());
  const apps = $derived(factors.data?.totp ?? []);
  const passkeys = $derived(factors.data?.passkeys ?? []);
  const count = $derived(apps.length + passkeys.length);
  const left = $derived(factors.data?.recovery_codes ?? 0);
  const canPasskey = passkeysSupported();
  // Without a second step, the section that adds one is the first thing on the page.
  const needsSecondStep = $derived(!!factors.data && count === 0);

  /** Reloads the factors and the user, which lifts the setup-only limit after the first method. */
  async function changed() {
    await Promise.all([factors.reload(), auth.load()]);
  }

  // --- password (local accounts), in a dialog ---
  let passwordOpen = $state(false);
  let password = $state("");
  let repeat = $state("");
  // Flag a mismatch only once the second entry can no longer become the first.
  const mismatch = $derived(repeat !== "" && password !== "" && repeat !== password && !password.startsWith(repeat));

  function startPassword() {
    password = "";
    repeat = "";
    passwordOpen = true;
  }

  async function changePassword() {
    if (!user) return;
    if (password !== repeat) throw new Error("The two passwords differ. Enter the same password twice.");
    await api.users.update(user.username, { password });
    password = "";
    repeat = "";
    toast.success("Changed your password");
  }

  // --- methods: one list of authenticator apps and passkeys ---
  type Method = { key: string; kind: "totp"; item: TOTPFactor } | { key: string; kind: "passkey"; item: Passkey };
  const methods: TableSource<Method> = {
    get items() {
      return [
        ...apps.map((t): Method => ({ key: `t${t.id}`, kind: "totp", item: t })),
        ...passkeys.map((p): Method => ({ key: `p${p.id}`, kind: "passkey", item: p })),
      ];
    },
    get loading() {
      return factors.loading && !factors.data;
    },
    get error() {
      return factors.error;
    },
    reload: factors.reload,
  };
  const kindName = (m: Method) => (m.kind === "totp" ? "Authenticator app" : "Passkey");

  // --- authenticator apps: name, then scan and confirm ---
  let totpNameOpen = $state(false);
  let totpScanOpen = $state(false);
  let totpName = $state("");
  let totpCode = $state("");
  let setup = $state<TOTPSetup | null>(null);

  function startTOTP() {
    totpName = "";
    totpCode = "";
    setup = null;
    totpNameOpen = true;
  }

  async function beginTOTP() {
    setup = await api.security.beginTOTP(totpName.trim());
    // Open the second dialog after the first has closed.
    setTimeout(() => (totpScanOpen = true), 0);
  }

  async function confirmTOTP() {
    if (!setup) return;
    await api.security.confirmTOTP(setup.setup_id, totpCode.replace(/\s/g, ""));
    toast.success(`Added the authenticator app ${totpName.trim()}`);
    setup = null;
    await changed();
  }

  // --- passkeys ---
  let passkeyOpen = $state(false);
  let passkeyName = $state("");

  function startPasskey() {
    passkeyName = "";
    passkeyOpen = true;
  }

  async function addPasskey() {
    const name = passkeyName.trim();
    const credential = await createPasskey(await api.security.beginPasskey());
    await api.security.finishPasskey(name, credential);
    toast.success(`Added the passkey ${name}`);
    await changed();
  }

  // --- removing a method ---
  let removal = $state<Method | null>(null);
  let removeOpen = $state(false);

  function askRemove(m: Method) {
    removal = m;
    removeOpen = true;
  }

  const removeChanges = $derived.by(() => {
    if (!removal) return [];
    const what = removal.kind === "totp" ? "authenticator app" : "passkey";
    const out = [`Removes the ${what} ${removal.item.name}`, `You can no longer sign in with this ${what}`];
    if (count <= 1) out.push("Your account has no second step left, so you must add a new one");
    return out;
  });

  async function remove() {
    if (!removal) return;
    const { kind, item } = removal;
    if (kind === "totp") await api.security.deleteTOTP(item.id);
    else await api.security.deletePasskey(item.id);
    toast.success(`Removed the ${kind === "totp" ? "authenticator app" : "passkey"} ${item.name}`);
    await changed();
  }

  // --- recovery codes ---
  let codes = $state<string[] | null>(null);
  let recoveryOpen = $state(false);
  let generating = $state(false);

  async function generate() {
    generating = true;
    try {
      const r = await api.security.recoveryCodes();
      codes = r.codes;
      toast.success("Generated new recovery codes");
      await factors.reload();
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      generating = false;
    }
  }
</script>

<PageHeader title="Sign-in and security" />

{#if back || required}
  <Alert.Root class="mb-6">
    {#if count}<CircleCheckIcon />{:else}<ShieldCheckIcon />{/if}
    <Alert.Title>{count ? "Two-step verification is on" : "Add a second step to go on"}</Alert.Title>
    <Alert.Description>
      {#if count}
        <p>{back ? "Go back to finish what you started." : "Your account is ready."}</p>
        <div class="mt-2"><Button href={href(onward)}>{back ? "Go back" : "Open repositories"}</Button></div>
      {:else if back}
        <p>Add an authenticator app or a passkey below. Then you can go back to finish what you started.</p>
      {:else}
        <p>Your account signs in with a password and one more step. Add an authenticator app or a passkey below to use Telesphorepo.</p>
      {/if}
    </Alert.Description>
  </Alert.Root>
{/if}

{#if needsSecondStep}{@render secondStep()}{/if}

<Section title="Sign-in">
  {#if local}
    <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
      <p class="min-w-0 flex-1 basis-40 text-label">Password</p>
      <Button variant="outline" size="sm" aria-label="Change password" onclick={startPassword}>Change</Button>
    </div>
  {:else}
    <div class="flex flex-wrap items-center gap-x-4 gap-y-1">
      <p class="text-label">Single sign-on</p>
      <p class="min-w-0 flex-1 basis-56 text-meta text-muted-foreground">Change your password with your identity provider.</p>
    </div>
  {/if}
</Section>

{#if !needsSecondStep}{@render secondStep()}{/if}

{#if count || codes}
  <Section title="Recovery codes" description="Each code signs you in once when your app or passkey is not at hand.">
    {#if codes}
      <div class="mb-4 flex flex-col gap-4 rounded-lg bg-panel p-5 ring-1 ring-border" aria-live="polite">
        <div class="flex flex-col gap-1">
          <p class="text-label">Your new recovery codes</p>
          <p class="text-meta text-muted-foreground">Save them now, for example in your password manager. You see them only once.</p>
        </div>
        <CodeBlock code={codes.join("\n")} />
        <div><Button variant="outline" onclick={() => (codes = null)}>Done</Button></div>
      </div>
    {/if}
    {#if left}
      <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
        <div class="flex min-w-0 flex-1 basis-48 flex-wrap items-center gap-x-3 gap-y-1">
          <p class="text-label tabular-nums">{left} of {TOTAL_CODES} left</p>
          <span class="flex items-center gap-0.5" role="img" aria-label="{left} of {TOTAL_CODES} codes left">
            {#each { length: TOTAL_CODES }, i (i)}
              <span class="h-3 w-1 rounded-full {i < left ? (left <= 3 ? 'bg-warning' : 'bg-success') : 'bg-input'}"></span>
            {/each}
          </span>
          {#if left <= 3}<span class="text-meta text-warning">Generate new codes soon</span>{/if}
        </div>
        <Button variant="outline" size="sm" disabled={generating} onclick={() => (recoveryOpen = true)}>Generate new codes</Button>
      </div>
    {:else if !codes}
      <CompactEmpty icon={LifeBuoyIcon} text="None yet. Keep them in a safe place, for example in your password manager.">
        <Button variant="outline" size="sm" disabled={generating} onclick={generate}>{#if generating}<Spinner />{/if}Generate codes</Button>
      </CompactEmpty>
    {/if}
  </Section>
{/if}

{#snippet secondStep()}
  <Section title="Two-step verification" description="The second step confirms each sign-in, each publish token and each new device.">
    {#snippet actions()}
      <!-- A kind that is not set up has its Add in its own row below; every Add looks the same. -->
      {#if apps.length}<Button variant="outline" size="sm" onclick={startTOTP}><PlusIcon />Add authenticator app</Button>{/if}
      {#if passkeys.length && canPasskey}<Button variant="outline" size="sm" onclick={startPasskey}><PlusIcon />Add passkey</Button>{/if}
    {/snippet}
    {#if factors.loading && !factors.data}
      <Loading rows={2} />
    {:else}
      <!-- One row each, parted by thin dividers: the methods, then each kind that is not set up. -->
      <div class="flex flex-col divide-y [&>div]:py-4 [&>div:first-child]:pt-0 [&>div:last-child]:pb-0">
        {#if count || factors.error}
          <div>
            <ItemList source={methods} caption="Your second-step methods" key={(m) => m.key}>
              {#snippet leading(m)}
                {#if m.kind === "totp"}<SmartphoneIcon class="size-5" />{:else}<KeyRoundIcon class="size-5" />{/if}
              {/snippet}
              {#snippet title(m)}{m.item.name}{/snippet}
              {#snippet meta(m)}
                {kindName(m)} · added <RelativeTime at={m.item.created_at} />{#if m.kind === "passkey"}
                  {" · "}{#if m.item.last_used_at}used <RelativeTime at={m.item.last_used_at} />{:else}not used yet{/if}{/if}
              {/snippet}
              {#snippet trailing(m)}
                <Button
                  variant="ghost"
                  size="sm"
                  class="hover:bg-destructive/10 hover:text-destructive"
                  aria-label="Remove {m.item.name}"
                  onclick={() => askRemove(m)}>Remove</Button
                >
              {/snippet}
            </ItemList>
          </div>
        {/if}
        {#if factors.data && !apps.length}
          <div>
            <CompactEmpty icon={SmartphoneIcon} title="Authenticator app" state="Not set up" text="An app such as 1Password, Aegis or Google Authenticator shows a 6-digit code.">
              <Button variant="outline" size="sm" onclick={startTOTP}><PlusIcon />Add</Button>
            </CompactEmpty>
          </div>
        {/if}
        {#if factors.data && !passkeys.length}
          <div>
            <CompactEmpty
              icon={KeyRoundIcon}
              title="Passkey"
              state="Not set up"
              text={canPasskey ? "Sign in with your fingerprint, your face or a security key." : "This browser cannot create passkeys. Passkeys need HTTPS or localhost."}
            >
              {#if canPasskey}<Button variant="outline" size="sm" onclick={startPasskey}><PlusIcon />Add</Button>{/if}
            </CompactEmpty>
          </div>
        {/if}
      </div>
    {/if}
  </Section>
{/snippet}

<ShortFormDialog bind:open={passwordOpen} title="Change your password" submitLabel="Change password" canSubmit={!!password && !!repeat && !mismatch} onSubmit={changePassword}>
  <input type="text" autocomplete="username" value={user?.username ?? ""} hidden readonly />
  <FormField label="New password" for="new-password" hint="The password needs at least 8 characters.">
    <Input id="new-password" type="password" autocomplete="new-password" bind:value={password} />
  </FormField>
  <FormField label="New password again" for="repeat-password" error={mismatch ? "The two passwords differ." : undefined}>
    <Input id="repeat-password" type="password" autocomplete="new-password" bind:value={repeat} aria-invalid={mismatch || undefined} />
  </FormField>
</ShortFormDialog>

<ShortFormDialog bind:open={totpNameOpen} title="Add an authenticator app" submitLabel="Continue" canSubmit={!!totpName.trim()} onSubmit={beginTOTP}>
  <FormField label="Name" for="totp-name" hint="The name tells your apps apart, for example Phone.">
    <Input id="totp-name" bind:value={totpName} />
  </FormField>
</ShortFormDialog>

<ShortFormDialog
  bind:open={totpScanOpen}
  title="Scan the code"
  description="Scan the code with your authenticator app, then enter the 6-digit code that it shows."
  submitLabel="Add app"
  canSubmit={!!totpCode.trim()}
  onSubmit={confirmTOTP}
>
  {#if setup}
    <div class="flex flex-wrap items-start gap-5">
      <QR text={setup.otpauth_url} />
      <div class="flex min-w-0 flex-1 basis-48 flex-col gap-3">
        <CodeSnippet label="Or enter this key by hand" value={setup.secret} />
        <a class="text-body text-link underline-offset-4 hover:underline" href={setup.otpauth_url}>Open the setup link on the device with the app</a>
      </div>
    </div>
    <FormField label="Code from the app" for="totp-code">
      <Input id="totp-code" class="tracking-wider tabular-nums" inputmode="numeric" autocomplete="one-time-code" placeholder="123 456" bind:value={totpCode} />
    </FormField>
  {/if}
</ShortFormDialog>

<ShortFormDialog
  bind:open={passkeyOpen}
  title="Add a passkey"
  description="Your browser asks where to keep the passkey."
  submitLabel="Add passkey"
  canSubmit={!!passkeyName.trim()}
  onSubmit={addPasskey}
>
  <FormField label="Name" for="passkey-name" hint="The name tells your passkeys apart, for example Laptop.">
    <Input id="passkey-name" bind:value={passkeyName} />
  </FormField>
</ShortFormDialog>

<ConfirmDialog
  bind:open={removeOpen}
  title={removal?.kind === "passkey" ? "Remove this passkey?" : "Remove this authenticator app?"}
  confirmLabel={removal?.kind === "passkey" ? "Remove passkey" : "Remove app"}
  destructive
  changes={removeChanges}
  onConfirm={remove}
/>

<ConfirmDialog
  bind:open={recoveryOpen}
  title="Generate new recovery codes?"
  confirmLabel="Generate new codes"
  destructive
  changes={["Makes 10 new codes the only valid ones", "Every unused code you have now stops working"]}
  onConfirm={generate}
/>
