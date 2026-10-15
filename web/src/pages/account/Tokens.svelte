<!--
  Your account: Tokens. Two sections on one page. Read tokens: a build or a script downloads with
  your username and a read token; the secret shows once. Publish tokens: every publish needs one.
  The Gradle plugin, the Maven extension and CI get theirs by themselves; for a script, create one
  here for one repository after a fresh second step. Creating one prints a felt ticket; ending it
  punches the ticket. A single sign-on account has neither: it shows why, once per section.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import type { PageProps } from "$lib/router.svelte";
  import { href, repoPath } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { api, type Proof, type PublishSession, type Token } from "$lib/api";
  import { Keyset, Resource, listOf } from "$lib/load.svelte";
  import * as Table from "$lib/components/ui/table";
  import * as Select from "$lib/components/ui/select";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import DataTable from "$lib/components/DataTable.svelte";
  import Cell from "$lib/components/Cell.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import FeltTicket from "$lib/components/FeltTicket.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import CodeSnippet from "$lib/components/CodeSnippet.svelte";
  import FormField from "$lib/components/FormField.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import ShortFormDialog from "$lib/components/ShortFormDialog.svelte";
  import SecondFactorPrompt from "$lib/components/SecondFactorPrompt.svelte";
  import KeyRoundIcon from "@lucide/svelte/icons/key-round";
  import UploadIcon from "@lucide/svelte/icons/upload";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import { tokenSource, tokenState } from "./words";

  let {}: PageProps = $props();

  const sso = $derived(auth.user?.kind === "oidc");

  // --- read tokens ---
  const tokens = new Resource(() => api.tokens.list());

  let createOpen = $state(false);
  let name = $state("");
  let days = $state("");
  let createdRead = $state<{ name: string; token: string } | null>(null);

  function startRead() {
    name = "";
    days = "";
    createOpen = true;
  }

  async function createRead() {
    const n = name.trim();
    const t = await api.tokens.create(n, days ? Number(days) : undefined);
    createdRead = { name: n, token: t.token };
    toast.success(`Created the read token ${n}`);
    await tokens.reload();
  }

  let revoking = $state<Token | null>(null);
  let revokeOpen = $state(false);

  async function revoke() {
    if (!revoking) return;
    await api.tokens.delete(revoking.id);
    toast.success(`Revoked the read token ${revoking.name}`);
    await tokens.reload();
  }

  // --- publish tokens: choose the repository, then confirm with the second step ---
  const USER = "publish";
  const repos = new Resource(() => api.repos.list());
  const sessions = new Keyset((p) => api.publish.sessions(p));

  const writable = $derived(
    (repos.data ?? [])
      .filter((r) => auth.isAdmin || r.permission === "write" || r.permission === "admin")
      .sort((a, b) => a.name.localeCompare(b.name)),
  );

  let chooseOpen = $state(false);
  let factorOpen = $state(false);
  let chosen = $state("");
  let created = $state<PublishSession | null>(null);
  let ended = $state(false);
  let endOpen = $state(false);

  function startPublish() {
    chosen = writable[0]?.name ?? "";
    chooseOpen = true;
  }

  function choose() {
    // Open the second dialog after the first has closed.
    setTimeout(() => (factorOpen = true), 0);
  }

  async function createPublish(proof: Proof) {
    ended = false;
    created = await api.publish.manualSession(chosen, proof);
    toast.success(`Created a publish token for ${created.repo}`);
    await sessions.reload();
  }

  const createdRepo = $derived(created ? (repos.data ?? []).find((r) => r.name === created!.repo) : undefined);
  const uploadCmd = $derived.by(() => {
    if (!created || !createdRepo) return "";
    const url = `${location.origin}/${createdRepo.format}/${createdRepo.name}`;
    return createdRepo.format === "debian"
      ? `curl -f -u ${USER}:${created.token} -T FILE.deb ${url}/upload/DIST/COMPONENT`
      : `curl -f -u ${USER}:${created.token} -T FILE ${url}/PATH/FILE`;
  });

  // Ending a token punches the ticket; it stays until Done.
  async function end() {
    if (!created) return;
    await api.publish.closeSession(created.id, created.token);
    toast.success(`Ended the publish token for ${created.repo}`);
    ended = true;
    await sessions.reload();
  }

  function done() {
    created = null;
    ended = false;
  }

  const until = (at: string) => new Date(at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  const noRepo = $derived(!!repos.data && writable.length === 0);
</script>

<PageHeader title="Tokens" />

<Section title="Read tokens" description="A build or a script downloads with your username and a read token.">
  {#snippet actions()}
    {#if !sso && tokens.data?.length}<Button variant="outline" size="sm" onclick={startRead}><PlusIcon />New read token</Button>{/if}
  {/snippet}
  {#if sso}
    <CompactEmpty
      icon={KeyRoundIcon}
      text="You sign in with single sign-on, so this account has no read tokens. Builds use an account with a password."
    />
  {:else}
    {#if createdRead}
      <div class="mb-4 flex flex-col gap-4 rounded-lg bg-panel p-5 ring-1 ring-border" aria-live="polite">
        <p class="text-label">Your new read token {createdRead.name}</p>
        <CodeSnippet value={createdRead.token} label="Token" secret />
        <div><Button variant="outline" onclick={() => (createdRead = null)}>Done</Button></div>
      </div>
    {/if}
    <DataTable
      source={listOf(tokens)}
      caption="Read tokens"
      key={(t) => t.id}
      columns={[
        { label: "Name", grow: true },
        { label: "Created", align: "right", hideOnPhone: true },
        { label: "Expires", align: "right" },
        { label: "Last used", align: "right", hideOnPhone: true },
        { label: "", srLabel: "Actions" },
      ]}
    >
      {#snippet row(t)}
        <Cell grow strong text={t.name} />
        <Cell num muted hideOnPhone><RelativeTime at={t.created_at} /></Cell>
        <Cell num muted><RelativeTime at={t.expires_at} empty="Never" /></Cell>
        <Cell num muted hideOnPhone><RelativeTime at={t.last_used_at} empty="Never" /></Cell>
        <Cell actions>
          <Button
            variant="ghost"
            size="sm"
            class="text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
            aria-label="Revoke {t.name}"
            onclick={() => {
              revoking = t;
              revokeOpen = true;
            }}>Revoke</Button
          >
        </Cell>
      {/snippet}
      {#snippet empty()}
        <CompactEmpty icon={KeyRoundIcon} text="None yet. Create one for each build or script that downloads.">
          <Button size="sm" variant="outline" onclick={startRead}><PlusIcon />New read token</Button>
        </CompactEmpty>
      {/snippet}
    </DataTable>
  {/if}
</Section>

<Section title="Publish tokens" description="A script publishes to one repository for a short time with a publish token.">
  {#snippet actions()}
    {#if !sso && sessions.items.length && writable.length}<Button variant="outline" size="sm" onclick={startPublish}><PlusIcon />New publish token</Button>{/if}
  {/snippet}
  {#if sso}
    <CompactEmpty
      icon={UploadIcon}
      text="You sign in with single sign-on, so this account cannot publish. Publish from CI, or with an account with a password."
    />
  {:else}
    {#if created}
      <div class="mb-6 flex flex-col gap-x-8 gap-y-6 rounded-lg bg-panel p-5 ring-1 ring-border lg:flex-row lg:items-center">
        <div class="w-80 max-w-full shrink-0 self-center sm:w-96 lg:self-start">
          <FeltTicket
            top={created.repo}
            bottom={ended ? "Ended" : `Works until ${until(created.expires_at)}`}
            serial="No {String(created.id).padStart(6, '0')}"
            moment={ended ? "punch" : "print"}
            label={ended ? `Ended publish token for ${created.repo}` : `Publish token for ${created.repo}`}
          />
        </div>
        <div class="flex min-w-0 flex-1 flex-col gap-4" aria-live="polite">
          {#if ended}
            <div class="flex flex-col gap-1">
              <p class="text-section">Token ended</p>
              <p class="text-body text-muted-foreground">The publish token for {created.repo} no longer works. Uploads that use it fail.</p>
            </div>
            <div><Button variant="outline" onclick={done}>Done</Button></div>
          {:else}
            <div class="flex flex-col gap-1">
              <p class="text-section">Your publish token for {created.repo}</p>
              <p class="text-body text-muted-foreground">
                It stops working <RelativeTime at={created.expires_at} />. Use it as the password, with any username.
              </p>
            </div>
            <CodeSnippet value={created.token} label="Token" secret />
            {#if uploadCmd}<CodeSnippet value={uploadCmd} label="Upload one file" />{/if}
            <p class="text-meta text-muted-foreground">
              To publish from a build or from CI, see
              <a href={href(repoPath(created.repo, "publish"))} class="text-link underline-offset-4 hover:underline">how to publish to {created.repo}</a>.
            </p>
            <div class="flex flex-wrap gap-2">
              <Button variant="outline" onclick={done}>Done</Button>
              <Button variant="ghost" class="text-muted-foreground hover:bg-destructive/10 hover:text-destructive" onclick={() => (endOpen = true)}>End it now</Button>
            </div>
          {/if}
        </div>
      </div>
    {/if}
    <DataTable
      source={sessions}
      caption="Your publish tokens"
      key={(s) => s.id}
      href={(s) => href(repoPath(s.repo))}
      columns={[
        { label: "Repository", grow: true },
        { label: "From", hideOnPhone: true },
        { label: "Started", align: "right", hideOnPhone: true },
        { label: "Ends", align: "right", hideOnPhone: true },
        { label: "Status" },
      ]}
    >
      {#snippet row(s)}
        {@const st = tokenState(s)}
        <Table.Cell class="w-full max-w-0 min-w-40">
          <a href={href(repoPath(s.repo))} class="block truncate rounded-sm font-medium outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50">{s.repo}</a>
        </Table.Cell>
        <Cell hideOnPhone muted text={tokenSource(s)} />
        <Cell num muted hideOnPhone><RelativeTime at={s.created_at} /></Cell>
        <Cell num muted hideOnPhone><RelativeTime at={s.closed_at ?? s.expires_at} /></Cell>
        <Cell><StatusBadge tone={st.tone}>{st.word}</StatusBadge></Cell>
      {/snippet}
      {#snippet empty()}
        <CompactEmpty
          icon={UploadIcon}
          text={noRepo
            ? "You cannot publish to a repository yet. Ask an administrator for access."
            : "A token shows here each time you, your build tool or this site publishes."}
        >
          {#if writable.length}<Button variant="outline" size="sm" onclick={startPublish}><PlusIcon />New publish token</Button>{/if}
        </CompactEmpty>
      {/snippet}
    </DataTable>
  {/if}
</Section>

<ShortFormDialog bind:open={createOpen} title="New read token" submitLabel="Create token" canSubmit={!!name.trim()} onSubmit={createRead}>
  <FormField label="Name" for="token-name" hint="Name the build or script that uses the token, for example CI runner.">
    <Input id="token-name" bind:value={name} />
  </FormField>
  <FormField label="Expires after (days)" for="token-days" hint="Leave it empty for a token that does not expire.">
    <Input id="token-days" type="number" min="1" bind:value={days} />
  </FormField>
</ShortFormDialog>

<ConfirmDialog
  bind:open={revokeOpen}
  title="Revoke this read token?"
  confirmLabel="Revoke token"
  destructive
  changes={[`Stops the read token ${revoking?.name ?? ""} at once`, "Builds and scripts that use it can no longer download"]}
  onConfirm={revoke}
/>

<ShortFormDialog
  bind:open={chooseOpen}
  title="New publish token"
  description="The token works for one repository. Next, confirm it is you."
  submitLabel="Continue"
  canSubmit={!!chosen}
  onSubmit={choose}
>
  <FormField label="Repository" for="token-repo">
    <Select.Root type="single" bind:value={chosen}>
      <Select.Trigger id="token-repo" class="w-full">
        <span class="truncate">{chosen || "Choose a repository"}</span>
      </Select.Trigger>
      <Select.Content>
        {#each writable as r (r.name)}
          <Select.Item value={r.name} label={r.name} />
        {/each}
      </Select.Content>
    </Select.Root>
  </FormField>
</ShortFormDialog>

<SecondFactorPrompt bind:open={factorOpen} title="Confirm it is you" actionLabel="Create token" onProof={createPublish} />

<ConfirmDialog
  bind:open={endOpen}
  title="End this publish token?"
  confirmLabel="End token"
  destructive
  changes={[`The token for ${created?.repo ?? ""} stops working at once`, "Uploads that use it fail"]}
  onConfirm={end}
/>
