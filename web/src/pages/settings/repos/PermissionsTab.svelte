<!-- Repository settings, "Who has access" tab: which users can download (read) or publish (write). Admins always can. -->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { api, errorMessage, type Permission, type Repo } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import * as Table from "$lib/components/ui/table";
  import * as Select from "$lib/components/ui/select";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import { Spinner } from "$lib/components/ui/spinner";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import LoadingRows from "$lib/components/LoadingRows.svelte";
  import Section from "$lib/components/Section.svelte";
  import UsersIcon from "@lucide/svelte/icons/users";

  let { repo }: { repo: Repo } = $props();

  const id = $props.id();
  const perms = new Resource(() => api.repos.permissions.list(repo.name));
  const levels: Permission["level"][] = ["read", "write"];
  /** The words users see for a level; the API keeps read and write. */
  const levelLabel: Record<Permission["level"], string> = { read: "Can download", write: "Can publish" };

  let username = $state("");
  let level = $state<Permission["level"]>("read");
  let busy = $state(false);
  let error = $state("");
  let removing = $state<Permission | null>(null);
  let removeOpen = $state(false);

  async function grant(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    error = "";
    try {
      const name = username.trim();
      await api.repos.permissions.set(repo.name, name, level);
      toast.success(`Gave ${name} access: ${levelLabel[level].toLowerCase()}`);
      username = "";
      await perms.reload();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }

  let changing = $state<{ p: Permission; to: Permission["level"] } | null>(null);
  let changeOpen = $state(false);

  function askChange(p: Permission, to: Permission["level"]) {
    if (to === p.level) return;
    changing = { p, to };
    changeOpen = true;
  }

  async function change() {
    if (!changing) return;
    const { p, to } = changing;
    await api.repos.permissions.set(repo.name, p.username, to);
    toast.success(`Changed ${p.username} to ${levelLabel[to].toLowerCase()}`);
    await perms.reload();
  }
</script>

<Section narrow title="Give someone access" description="Admins can always download from and publish to every repository. People who can publish can also download.">
  <form class="flex flex-wrap items-end gap-4" onsubmit={grant}>
    <div class="flex w-full max-w-sm flex-col gap-2">
      <Label for="{id}-user">Username</Label>
      <Input id="{id}-user" bind:value={username} autocomplete="off" spellcheck={false} required />
    </div>
    <div class="flex flex-col gap-2">
      <Label for="{id}-level">Access</Label>
      <Select.Root type="single" value={level} onValueChange={(v) => (level = v as Permission["level"])}>
        <Select.Trigger id="{id}-level" class="w-40">{levelLabel[level]}</Select.Trigger>
        <Select.Content>
          {#each levels as l (l)}<Select.Item value={l} label={levelLabel[l]}>{levelLabel[l]}</Select.Item>{/each}
        </Select.Content>
      </Select.Root>
    </div>
    <Button type="submit" disabled={busy || !username.trim()}>
      {#if busy}<Spinner />{/if}
      Give access
    </Button>
  </form>
  {#if error}<div class="mt-4"><ErrorState inline {error} /></div>{/if}
</Section>

<Section title="People with access">
  {#if perms.error && !perms.data}
    <ErrorState error={perms.error} onRetry={perms.reload} />
  {:else if !perms.loading && perms.data?.length === 0}
    <EmptyState title="Nobody has access yet" description="Only admins can use this repository. Give someone access above." icon={UsersIcon} />
  {:else}
    <div class="w-full overflow-x-auto rounded-2xl border">
      <Table.Root>
        <Table.Caption class="sr-only">Users with access to {repo.name}</Table.Caption>
        <Table.Header>
          <Table.Row class="hover:bg-transparent">
            <Table.Head>Person</Table.Head>
            <Table.Head>Access</Table.Head>
            <Table.Head class="w-px"><span class="sr-only">Actions</span></Table.Head>
          </Table.Row>
        </Table.Header>
        <Table.Body>
          {#if perms.loading && !perms.data}
            <LoadingRows rows={3} columns={3} />
          {:else}
            {#each perms.data ?? [] as p (p.username)}
              <Table.Row>
                <Table.Cell class="font-medium">{p.username}</Table.Cell>
                <Table.Cell>
                  <Select.Root type="single" bind:value={() => p.level, (v) => askChange(p, v as Permission["level"])}>
                    <Select.Trigger size="sm" class="w-40" aria-label="Access of {p.username}">{levelLabel[p.level]}</Select.Trigger>
                    <Select.Content>
                      {#each levels as l (l)}<Select.Item value={l} label={levelLabel[l]}>{levelLabel[l]}</Select.Item>{/each}
                    </Select.Content>
                  </Select.Root>
                </Table.Cell>
                <Table.Cell>
                  <Button
                    variant="destructive"
                    size="sm"
                    onclick={() => {
                      removing = p;
                      removeOpen = true;
                    }}
                  >
                    Remove access
                  </Button>
                </Table.Cell>
              </Table.Row>
            {/each}
          {/if}
        </Table.Body>
      </Table.Root>
    </div>
  {/if}
</Section>

<ConfirmDialog
  bind:open={changeOpen}
  title="Change access"
  confirmLabel={changing ? `Change to ${levelLabel[changing.to].toLowerCase()}` : "Change access"}
  destructive={changing?.to === "read"}
  changes={changing
    ? changing.to === "read"
      ? [`${changing.p.username} can no longer publish to or delete from ${repo.name}`, `${changing.p.username} can still download`]
      : [`${changing.p.username} can publish to and delete from ${repo.name}`]
    : []}
  onConfirm={change}
/>

<ConfirmDialog
  bind:open={removeOpen}
  title="Remove access"
  confirmLabel={removing ? `Remove access of ${removing.username}` : "Remove access"}
  destructive
  changes={removing
    ? [`${removing.username} can no longer ${removing.level === "write" ? "download from or publish to" : "download from"} ${repo.name}`, `Download tokens of ${removing.username} lose access to this repository`]
    : []}
  onConfirm={async () => {
    if (!removing) return;
    const name = removing.username;
    await api.repos.permissions.delete(repo.name, name);
    toast.success(`Removed access of ${name}`);
    await perms.reload();
  }}
/>
