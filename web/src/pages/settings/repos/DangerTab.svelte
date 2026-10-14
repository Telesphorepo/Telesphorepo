<!-- Repository settings, Delete tab (id danger): delete the repository with all its files. -->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { api, type Repo } from "$lib/api";
  import { navigate } from "$lib/router.svelte";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import Section from "$lib/components/Section.svelte";

  let { repo }: { repo: Repo } = $props();

  const id = $props.id();
  let open = $state(false);
  let typed = $state("");

  $effect(() => {
    if (open) typed = "";
  });
</script>

<Section narrow tone="danger" title="Delete this repository" description="This deletes every file in the repository. You cannot undo it.">
  <Button variant="destructive" onclick={() => (open = true)}>Delete repository</Button>
</Section>

<ConfirmDialog
  bind:open
  title="Delete {repo.name}"
  confirmLabel="Delete repository"
  destructive
  changes={[
    `Deletes every file of ${repo.name} from the storage`,
    "Removes the access you gave people to it",
    "Builds that use it can no longer download from it",
    "Uploads to it fail while the delete runs",
  ]}
  onConfirm={async () => {
    if (typed !== repo.name) throw new Error(`Type ${repo.name} to confirm.`);
    await api.repos.delete(repo.name);
    toast.success(`Deleted repository ${repo.name}`);
    navigate("/settings/repos");
  }}
>
  <div class="flex flex-col gap-2">
    <Label for="{id}-confirm">Type <span class="font-mono font-semibold">{repo.name}</span> to confirm</Label>
    <Input id="{id}-confirm" class="max-w-sm font-mono" bind:value={typed} autocomplete="off" spellcheck={false} />
  </div>
</ConfirmDialog>
