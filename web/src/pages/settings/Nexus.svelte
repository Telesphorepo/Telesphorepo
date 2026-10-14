<!-- Admin: import from Nexus. Nexus sources are the servers whose repositories this server copies and keeps in sync. -->
<script lang="ts">
  import type { PageProps } from "$lib/router.svelte";
  import { href, navigate } from "$lib/router.svelte";
  import { api, errorMessage, type NexusSource } from "$lib/api";
  import { Keyset } from "$lib/load.svelte";
  import { Button } from "$lib/components/ui/button";
  import { Badge } from "$lib/components/ui/badge";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import { Spinner } from "$lib/components/ui/spinner";
  import * as Dialog from "$lib/components/ui/dialog";
  import * as Table from "$lib/components/ui/table";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import KeysetTable from "$lib/components/KeysetTable.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import RelativeTime from "$lib/components/RelativeTime.svelte";
  import ArrowRightLeftIcon from "@lucide/svelte/icons/arrow-right-left";
  import PlusIcon from "@lucide/svelte/icons/plus";

  let { params, query }: PageProps = $props();

  const list = new Keyset((p) => api.nexus.list(p));

  let open = $state(false);
  let name = $state("");
  let baseURL = $state("");
  let username = $state("");
  let password = $state("");
  let busy = $state(false);
  let error = $state("");

  function openDialog() {
    name = baseURL = username = password = error = "";
    open = true;
  }

  async function create(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    error = "";
    try {
      const s = await api.nexus.create({ name: name.trim(), base_url: baseURL.trim(), username, password });
      open = false;
      navigate(`/settings/nexus/${s.id}`);
    } catch (err) {
      error = errorMessage(err);
    } finally {
      password = "";
      busy = false;
    }
  }

  const stateLabel = (s: NexusSource["state"]) => (s === "cut_over" ? "Switched over" : "Syncing");
</script>

<PageHeader
  title="Import from Nexus"
  description="Copy the repositories of a Nexus server to this server and keep them in sync. When every build uses this server, switch over to it."
>
  {#snippet actions()}
    <Button onclick={openDialog}><PlusIcon /> Add a Nexus server</Button>
  {/snippet}
</PageHeader>

<KeysetTable {list} columns={4} caption="Nexus servers">
  {#snippet head()}
    <Table.Head>Name</Table.Head>
    <Table.Head>Address</Table.Head>
    <Table.Head>State</Table.Head>
    <Table.Head>Last sync</Table.Head>
  {/snippet}
  {#snippet row(s)}
    <Table.Row>
      <Table.Cell class="font-medium">
        <a class="text-link underline-offset-4 hover:underline" href={href(`/settings/nexus/${s.id}`)}>{s.name}</a>
      </Table.Cell>
      <Table.Cell class="font-mono text-[0.8125rem] break-all whitespace-normal">{s.base_url}</Table.Cell>
      <Table.Cell>
        <Badge variant={s.state === "cut_over" ? "secondary" : "default"}>{stateLabel(s.state)}</Badge>
      </Table.Cell>
      <Table.Cell class="whitespace-normal">
        {#if s.last_sync}
          <RelativeTime at={s.last_sync.finished_at ?? s.last_sync.started_at} />
        {:else}
          <span class="text-muted-foreground">No sync yet</span>
        {/if}
        {#if s.last_error}<div class="mt-1 text-sm break-words text-destructive">{s.last_error}</div>{/if}
      </Table.Cell>
    </Table.Row>
  {/snippet}
  {#snippet empty()}
    <EmptyState
      icon={ArrowRightLeftIcon}
      title="No Nexus server yet"
      description="Add the address of a Nexus server and an account that can read it. Then choose the repositories to copy."
    >
      <Button onclick={openDialog}><PlusIcon /> Add a Nexus server</Button>
    </EmptyState>
  {/snippet}
</KeysetTable>

<Dialog.Root bind:open>
  <Dialog.Content class="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-lg">
    <Dialog.Header>
      <Dialog.Title>Add a Nexus server</Dialog.Title>
      <Dialog.Description>Telesphorepo stores the password encrypted and uses it only to read the repositories.</Dialog.Description>
    </Dialog.Header>
    <form class="flex flex-col gap-4" onsubmit={create} autocomplete="off">
      <div class="flex flex-col gap-2">
        <Label for="nexus-name">Name</Label>
        <Input id="nexus-name" class="max-w-sm" required bind:value={name} />
      </div>
      <div class="flex flex-col gap-2">
        <Label for="nexus-url">Address</Label>
        <Input id="nexus-url" class="max-w-form font-mono" type="url" required placeholder="https://nexus.example.com" bind:value={baseURL} />
      </div>
      <div class="flex flex-col gap-2">
        <Label for="nexus-user">Username</Label>
        <Input id="nexus-user" class="max-w-sm" required autocomplete="off" bind:value={username} />
      </div>
      <div class="flex flex-col gap-2">
        <Label for="nexus-pass">Password</Label>
        <Input id="nexus-pass" class="max-w-sm" type="password" required autocomplete="new-password" bind:value={password} />
      </div>
      {#if error}<ErrorState inline {error} />{/if}
      <Dialog.Footer>
        <Button type="button" variant="outline" disabled={busy} onclick={() => (open = false)}>Cancel</Button>
        <Button type="submit" disabled={busy}>
          {#if busy}<Spinner />{/if}
          Add server
        </Button>
      </Dialog.Footer>
    </form>
  </Dialog.Content>
</Dialog.Root>
