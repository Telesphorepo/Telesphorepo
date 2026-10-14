<!--
  The "New repository" dialog: name, format (from /api/formats), description, "Anyone can download" (anonymous_read) and
  the format's config (its editor starts from the format's default config).
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { api, errorMessage, type Repo, type RepoConfig } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import * as Dialog from "$lib/components/ui/dialog";
  import * as Select from "$lib/components/ui/select";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import { Textarea } from "$lib/components/ui/textarea";
  import { Switch } from "$lib/components/ui/switch";
  import { Spinner } from "$lib/components/ui/spinner";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import LoadingRows from "$lib/components/LoadingRows.svelte";
  import ConfigEditor from "$lib/formats/ConfigEditor.svelte";

  let { open = $bindable(false), onCreated }: { open?: boolean; onCreated: (repo: Repo) => void } = $props();

  const id = $props.id();
  // Same rule as repos.NameRE on the server (escaped for the "v" flag that browsers use).
  const namePattern = "[a-z0-9][a-z0-9._\\-]{0,63}";
  const formats = new Resource(() => api.formats.list());

  let name = $state("");
  let formatName = $state("");
  let description = $state("");
  let anonymous = $state(false);
  // undefined: the default config of the format; null: the editor holds an invalid config.
  let config = $state<RepoConfig | null | undefined>(undefined);
  let busy = $state(false);
  let error = $state("");

  const format = $derived(formats.data?.find((f) => f.name === formatName) ?? formats.data?.[0]);

  function reset() {
    name = "";
    formatName = "";
    description = "";
    anonymous = false;
    config = undefined;
    error = "";
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!format || config === null) return;
    busy = true;
    error = "";
    try {
      const repo = await api.repos.create({
        name: name.trim(),
        format: format.name,
        description: description.trim(),
        anonymous_read: anonymous,
        config: config ?? format.default_config,
      });
      toast.success(`Created repository ${repo.name}`);
      open = false;
      reset();
      onCreated(repo);
    } catch (e) {
      error = errorMessage(e);
    } finally {
      busy = false;
    }
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-lg">
    <Dialog.Header>
      <Dialog.Title>New repository</Dialog.Title>
      <Dialog.Description>A repository holds one kind of package. You cannot change the name or the format later.</Dialog.Description>
    </Dialog.Header>

    {#if formats.loading}
      <LoadingRows rows={4} />
    {:else if formats.error}
      <ErrorState error={formats.error} onRetry={formats.reload} />
    {:else if format}
      <form class="flex flex-col gap-4" onsubmit={submit}>
        <div class="flex flex-col gap-2">
          <Label for="{id}-name">Name</Label>
          <Input
            id="{id}-name"
            class="max-w-sm font-mono"
            bind:value={name}
            pattern={namePattern}
            title="Lowercase letters, digits, dots, dashes and underscores. At most 64 characters."
            autocomplete="off"
            spellcheck={false}
            required
          />
          <p class="text-sm text-muted-foreground">Lowercase letters, digits, dots, dashes and underscores.</p>
        </div>

        <div class="flex flex-col gap-2">
          <Label for="{id}-format">Format</Label>
          <Select.Root
            type="single"
            value={format.name}
            onValueChange={(v) => {
              formatName = v;
              config = undefined;
            }}
          >
            <Select.Trigger id="{id}-format" class="w-44">{format.title}</Select.Trigger>
            <Select.Content>
              {#each formats.data ?? [] as f (f.name)}
                <Select.Item value={f.name} label={f.title}>{f.title}</Select.Item>
              {/each}
            </Select.Content>
          </Select.Root>
        </div>

        <div class="flex flex-col gap-2">
          <Label for="{id}-desc">Description (optional)</Label>
          <Textarea id="{id}-desc" rows={2} maxlength={500} bind:value={description} />
        </div>

        {#key format.name}
          <ConfigEditor format={format.name} value={format.default_config} onChange={(v) => (config = v)} />
        {/key}

        <div class="flex items-start gap-3">
          <Switch id="{id}-anon" class="mt-0.5" bind:checked={anonymous} aria-describedby="{id}-anon-help" />
          <div class="flex flex-col gap-1">
            <Label for="{id}-anon">Anyone can download</Label>
            <p id="{id}-anon-help" class="text-sm text-muted-foreground">
              People and builds can download without signing in. Publishing still needs access. Off: only people you give access can download.
            </p>
          </div>
        </div>

        {#if error}<ErrorState inline {error} />{/if}
        <Dialog.Footer>
          <Button type="button" variant="outline" disabled={busy} onclick={() => (open = false)}>Cancel</Button>
          <Button type="submit" disabled={busy || config === null || !name.trim()}>
            {#if busy}<Spinner />{/if}
            Create repository
          </Button>
        </Dialog.Footer>
      </form>
    {/if}
  </Dialog.Content>
</Dialog.Root>
