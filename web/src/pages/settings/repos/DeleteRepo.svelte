<!--
  Delete a repository with all its files, after typing its name: the dialog of the Delete action in the header of its settings.
  The server deletes it in the background; the dialog waits until it is gone, with the progress in a toast.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { api, type Repo } from "$lib/api";
  import { navigate } from "$lib/router.svelte";
  import { count } from "$lib/words";
  import { waitForDeletes } from "$lib/deletes";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";

  let { repo, open = $bindable(false) }: { repo: Repo; open?: boolean } = $props();

  const files = $derived(count(repo.file_count, "file", "files"));

  async function remove() {
    await api.repos.delete([repo.name]);
    await waitForDeletes([repo.name]);
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
    repo.kind === "group"
      ? `Deletes the group; its ${count(repo.member_count ?? 0, "repository keeps its", "repositories keep their")} files`
      : `Deletes the ${files} of ${repo.name} from the storage`,
    "Removes the access that people and CI pipelines have to it, and its network rules",
    ...(repo.groups?.length ? [`Removes it from the groups ${repo.groups.join(", ")}`] : []),
    "Builds that use it can no longer download from it",
  ]}
  onConfirm={remove}
/>
