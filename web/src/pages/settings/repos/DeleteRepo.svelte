<!-- Delete a repository with all its files, after typing its name: the dialog of the Delete action in the header of its settings. -->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { api, type Repo } from "$lib/api";
  import { navigate } from "$lib/router.svelte";
  import { formatCount } from "$lib/format";
  import { Input } from "$lib/components/ui/input";
  import FormField from "$lib/components/FormField.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";

  let { repo, open = $bindable(false) }: { repo: Repo; open?: boolean } = $props();

  const id = $props.id();
  let typed = $state("");
  const files = $derived(`${formatCount(repo.file_count)} ${repo.file_count === 1 ? "file" : "files"}`);

  $effect(() => {
    if (open) typed = "";
  });

  async function remove() {
    if (typed !== repo.name) throw new Error(`Type ${repo.name} to confirm.`);
    await api.repos.delete(repo.name);
    toast.success(`Deleted the repository ${repo.name}`);
    navigate("/settings/repositories", { force: true });
  }
</script>

<ConfirmDialog
  bind:open
  title="Delete {repo.name}?"
  confirmLabel="Delete repository"
  destructive
  changes={[
    repo.kind === "group"
      ? `Deletes the group; its ${formatCount(repo.member_count ?? 0)} ${repo.member_count === 1 ? "repository keeps its" : "repositories keep their"} files`
      : `Deletes the ${files} of ${repo.name} from the storage`,
    "Removes the access that people and CI pipelines have to it, and its network rules",
    ...(repo.groups?.length ? [`Removes it from the groups ${repo.groups.join(", ")}`] : []),
    "Builds that use it can no longer download from it",
  ]}
  onConfirm={remove}
>
  <FormField label="Type {repo.name} to confirm" for="{id}-confirm">
    <Input id="{id}-confirm" autocomplete="off" spellcheck={false} bind:value={typed} />
  </FormField>
</ConfirmDialog>
