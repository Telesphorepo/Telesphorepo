<!-- Account: read tokens. A token downloads packages and cannot publish. The secret shows once. -->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import type { PageProps } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { api, errorMessage, type Token } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import Section from "$lib/components/Section.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import LoadingRows from "$lib/components/LoadingRows.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import CopyField from "$lib/components/CopyField.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import * as Table from "$lib/components/ui/table";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import { Spinner } from "$lib/components/ui/spinner";
  import KeyIcon from "@lucide/svelte/icons/key-round";

  let { params, query }: PageProps = $props();

  const sso = $derived(auth.user?.kind === "oidc");
  const tokens = new Resource(() => api.tokens.list());

  let name = $state("");
  let days = $state("");
  let busy = $state(false);
  let formError = $state("");
  let created = $state<{ name: string; token: string } | null>(null);

  let target = $state<Token | null>(null);
  let confirmOpen = $state(false);

  async function create(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    formError = "";
    try {
      const n = name.trim();
      const t = await api.tokens.create(n, days ? Number(days) : undefined);
      created = { name: n, token: t.token };
      name = "";
      days = "";
      await tokens.reload();
    } catch (err) {
      formError = errorMessage(err);
    } finally {
      busy = false;
    }
  }

  function askRevoke(t: Token) {
    target = t;
    confirmOpen = true;
  }

  async function revoke() {
    if (!target) return;
    await api.tokens.delete(target.id);
    toast.success(`Revoked the token ${target.name}`);
    await tokens.reload();
  }
</script>

<PageHeader
  title="Read tokens"
  description="A read token downloads packages from the repositories you can read. It cannot publish. Publishing needs a publish session."
/>

{#if sso}
  <EmptyState
    icon={KeyIcon}
    title="Tokens are not available for this account"
    description="You sign in through single sign-on, so this account has no read tokens. Package clients use a publisher account."
  />
{:else}
  <Section title="Create a token" narrow>
    <form class="flex flex-col gap-4" onsubmit={create}>
      <div class="flex flex-col gap-2">
        <Label for="token-name">Name</Label>
        <Input id="token-name" class="max-w-sm" placeholder="CI runner" required bind:value={name} disabled={busy} />
      </div>
      <div class="flex flex-col gap-2">
        <Label for="token-days">Expires after (days)</Label>
        <Input id="token-days" type="number" min="1" class="w-44" placeholder="never" bind:value={days} disabled={busy} />
      </div>
      {#if formError}<ErrorState inline error={formError} />{/if}
      <div class="flex gap-2">
        <Button type="submit" disabled={busy || !name.trim()}>
          {#if busy}<Spinner />{/if}
          Create token
        </Button>
      </div>
    </form>
    {#if created}
      <div class="mt-4 flex flex-col gap-2">
        <p class="text-sm">Token <span class="font-medium">{created.name}</span> is ready.</p>
        <CopyField value={created.token} label="Token" secret />
        <div><Button variant="outline" size="sm" onclick={() => (created = null)}>Hide the token</Button></div>
      </div>
    {/if}
  </Section>

  <Section title="Your tokens">
    {#if tokens.data}
      {#if tokens.data.length === 0}
        <EmptyState title="No read tokens yet" description="Create a token above to let a client download packages." />
      {:else}
        <div class="w-full overflow-x-auto rounded-2xl border">
          <Table.Root>
            <Table.Caption class="sr-only">Read tokens</Table.Caption>
            <Table.Header>
              <Table.Row class="hover:bg-transparent">
                <Table.Head>Name</Table.Head>
                <Table.Head>Created</Table.Head>
                <Table.Head>Expires</Table.Head>
                <Table.Head>Last used</Table.Head>
                <Table.Head class="w-0"><span class="sr-only">Actions</span></Table.Head>
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {#each tokens.data as t (t.id)}
                <Table.Row>
                  <Table.Cell class="font-medium">{t.name}</Table.Cell>
                  <Table.Cell><RelativeTime at={t.created_at} /></Table.Cell>
                  <Table.Cell><RelativeTime at={t.expires_at} empty="never" /></Table.Cell>
                  <Table.Cell><RelativeTime at={t.last_used_at} empty="never" /></Table.Cell>
                  <Table.Cell class="text-right">
                    <Button variant="destructive" size="sm" onclick={() => askRevoke(t)}>Revoke</Button>
                  </Table.Cell>
                </Table.Row>
              {/each}
            </Table.Body>
          </Table.Root>
        </div>
      {/if}
    {:else if tokens.error}
      <ErrorState error={tokens.error} onRetry={tokens.reload} />
    {:else}
      <LoadingRows rows={3} />
    {/if}
  </Section>

  <ConfirmDialog
    bind:open={confirmOpen}
    title="Revoke token"
    confirmLabel="Revoke token"
    destructive
    changes={[
      `Stops the token ${target?.name ?? ""} at once`,
      "Clients that use it can no longer download packages",
    ]}
    onConfirm={revoke}
  />
{/if}
