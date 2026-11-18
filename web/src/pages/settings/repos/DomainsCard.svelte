<!--
  The Overview card of a repository's domains: domains that serve its files at their root (the
  lines of the general setting repo_domains that name this repository). Someone who can read the
  general settings sees it; Add domain and Remove change that setting, so the server checks a new
  domain as it does any save of it. A domain set by TR_REPO_DOMAINS is read-only. Docker
  repositories have no domains, so the card is not shown for them.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { api, type Repo, type Setting } from "$lib/api";
  import { Resource, type TableSource } from "$lib/load.svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import Section from "$lib/components/Section.svelte";
  import ItemList from "$lib/components/ItemList.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import ShortFormDialog from "$lib/components/ShortFormDialog.svelte";
  import FormField from "$lib/components/FormField.svelte";
  import StatusBadge from "$lib/components/StatusBadge.svelte";
  import GlobeIcon from "@lucide/svelte/icons/globe";
  import PlusIcon from "@lucide/svelte/icons/plus";

  let { repo }: { repo: Repo } = $props();

  const settings = new Resource(() => api.settings.get());
  const setting = $derived<Setting | undefined>(settings.data?.groups.flatMap((g) => g.settings).find((s) => s.key === "repo_domains"));
  const lines = $derived(((setting?.value as string[] | null) ?? []).filter(Boolean));
  const mine = $derived(lines.filter((l) => l.split("=")[1] === repo.name).map((l) => l.split("=")[0]));
  const canEdit = $derived(!!setting && !setting.locked);

  const source: TableSource<string> = {
    get items() {
      return mine;
    },
    get loading() {
      return settings.loading && !settings.data;
    },
    get error() {
      return settings.error ?? "";
    },
    reload: settings.reload,
  };

  async function save(next: string[]) {
    const result = await api.settings.patch({ repo_domains: next });
    if ("groups" in result) settings.data = result;
    else await settings.reload();
  }

  let addOpen = $state(false);
  let domain = $state("");
  function startAdd() {
    domain = "";
    addOpen = true;
  }
  async function add() {
    const d = domain.trim().toLowerCase();
    await save([...lines, `${d}=${repo.name}`]);
    toast.success(`${d} now serves ${repo.name}`);
  }

  let removing = $state<string | null>(null);
  let confirmOpen = $state(false);
  async function remove() {
    if (!removing) return;
    const d = removing;
    await save(lines.filter((l) => l.split("=")[0] !== d));
    toast.success(`Removed the domain ${d}`);
  }
</script>

{#if setting && repo.format !== "docker"}
  <Section title="Domains" description="Domains that serve the files of {repo.name} at their root, for downloads only. Point the domain at this server first.">
    {#snippet actions()}
      {#if setting.locked}
        <StatusBadge title="The environment variable {setting.env} sets the domains. Remove it to change them here.">Set by {setting.env}</StatusBadge>
      {:else if mine.length}
        <Button variant="outline" size="sm" onclick={startAdd}><PlusIcon />Add domain</Button>
      {/if}
    {/snippet}
    <ItemList {source} caption="Domains of {repo.name}" key={(d) => d}>
      {#snippet leading()}<GlobeIcon class="size-5" />{/snippet}
      {#snippet title(d)}{d}{/snippet}
      {#snippet trailing(d)}
        {#if canEdit}
          <Button
            variant="ghost"
            size="sm"
            class="hover:bg-destructive/10 hover:text-destructive"
            onclick={() => {
              removing = d;
              confirmOpen = true;
            }}>Remove</Button
          >
        {/if}
      {/snippet}
      {#snippet empty()}
        <CompactEmpty icon={GlobeIcon} text="None yet. A domain such as updater.example.com can serve this repository's files at its root.">
          {#if canEdit}<Button size="sm" variant="outline" onclick={startAdd}><PlusIcon />Add domain</Button>{/if}
        </CompactEmpty>
      {/snippet}
    </ItemList>
  </Section>
{/if}

<ShortFormDialog bind:open={addOpen} title="Add a domain to {repo.name}" submitLabel="Add domain" canSubmit={!!domain.trim()} onSubmit={add}>
  <FormField label="Domain" for="repo-domain" hint="A domain that points at this server, such as updater.example.com.">
    <Input id="repo-domain" bind:value={domain} autocomplete="off" spellcheck={false} />
  </FormField>
</ShortFormDialog>

<ConfirmDialog
  bind:open={confirmOpen}
  title="Remove the domain {removing ?? ''}?"
  confirmLabel="Remove domain"
  destructive
  changes={removing ? [`${removing} stops serving ${repo.name}`] : []}
  onConfirm={remove}
/>
