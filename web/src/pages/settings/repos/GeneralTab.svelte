<!-- Repository settings, General tab: description and anonymous read. -->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import { api, errorMessage, type Repo } from "$lib/api";
  import { Button } from "$lib/components/ui/button";
  import { Label } from "$lib/components/ui/label";
  import { Switch } from "$lib/components/ui/switch";
  import { Textarea } from "$lib/components/ui/textarea";
  import { Spinner } from "$lib/components/ui/spinner";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Section from "$lib/components/Section.svelte";

  let { repo, onChanged }: { repo: Repo; onChanged: () => void } = $props();

  const id = $props.id();
  let description = $state(untrack(() => repo.description));
  let anonymous = $state(untrack(() => repo.anonymous_read));
  let busy = $state(false);
  let error = $state("");
  let confirming = $state(false);

  const anonymousChanged = $derived(anonymous !== repo.anonymous_read);
  const dirty = $derived(anonymousChanged || description.trim() !== repo.description);

  async function save() {
    await api.repos.update(repo.name, { description: description.trim(), anonymous_read: anonymous });
    toast.success("Saved the settings of " + repo.name);
    onChanged();
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (anonymousChanged) {
      confirming = true;
      return;
    }
    busy = true;
    error = "";
    try {
      await save();
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }
</script>

<Section narrow title="Details">
  <form class="flex flex-col gap-4" onsubmit={submit}>
    <div class="flex flex-col gap-2">
      <Label for="{id}-desc">Description</Label>
      <Textarea id="{id}-desc" rows={3} maxlength={500} bind:value={description} />
      <p class="text-sm text-muted-foreground">Shown in the repository list. {description.length} of 500 characters.</p>
    </div>

    <div class="flex items-start gap-3">
      <Switch id="{id}-anon" class="mt-0.5" bind:checked={anonymous} />
      <div class="flex flex-col gap-1">
        <Label for="{id}-anon">Anonymous read</Label>
        <p class="text-sm text-muted-foreground">
          Anyone can list and download the files without signing in. Only users with write access can publish.
        </p>
      </div>
    </div>

    {#if error}<ErrorState inline {error} />{/if}
    <div class="flex gap-2">
      <Button type="submit" disabled={busy || !dirty}>
        {#if busy}<Spinner />{/if}
        Save changes
      </Button>
    </div>
  </form>
</Section>

<ConfirmDialog
  bind:open={confirming}
  title={anonymous ? "Allow anonymous read" : "Require sign-in to read"}
  confirmLabel={anonymous ? "Allow anonymous read" : "Require sign-in"}
  destructive={anonymous}
  changes={anonymous
    ? [`Anyone can list and download every file of ${repo.name} without signing in`, "Search results and the file tree show the files to anonymous visitors"]
    : [`Visitors who are not signed in can no longer read ${repo.name}`, "Builds that use the repository without a read token fail"]}
  onConfirm={save}
/>
