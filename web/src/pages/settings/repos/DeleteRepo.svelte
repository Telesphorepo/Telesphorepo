<!-- Delete a repository with all its files, after typing its name: the dialog of the Delete action in the header of its settings. -->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { api, type Repo } from "$lib/api";
  import { navigate } from "$lib/router.svelte";
  import { count } from "$lib/words";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";

  let { repo, open = $bindable(false) }: { repo: Repo; open?: boolean } = $props();

  const files = $derived(count(repo.file_count, "file", "files"));

  async function remove() {
    await api.repos.delete([repo.name]);
    toast.success(`Deleted the repository ${repo.name}`);
    navigate("/settings/repositories", { force: true });
  }
</script>

<ConfirmDialog
  bind:open
  title="Delete {repo.name}?"
  confirmLabel="Delete repository"
  destructive
  typeToConfirm={repo.name}
  changes={[
    `Deletes the ${files} of ${repo.name} from the storage`,
    "Removes the access that people and CI pipelines have to it, and its network rules",
    "Builds that use it can no longer download from it",
  ]}
  onConfirm={remove}
/>
