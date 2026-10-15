<!--
  New repository (administrators): name, format, description and who can download. A Debian
  repository also gets its architectures and its signing key: a key that exists, or a new key that
  the page creates first (POST /api/signing-keys, then POST /api/repos). Other formats with options
  show their format editor.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import type { PageProps } from "$lib/router.svelte";
  import { href, navigate, repoPath } from "$lib/router.svelte";
  import { api, errorMessage, type RepoConfig } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import { formatLook } from "$lib/formats";
  import * as RadioGroup from "$lib/components/ui/radio-group";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Spinner } from "$lib/components/ui/spinner";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import FormPage from "$lib/components/FormPage.svelte";
  import FormSection from "$lib/components/FormSection.svelte";
  import FormGrid from "$lib/components/FormGrid.svelte";
  import FormRow from "$lib/components/FormRow.svelte";
  import FormatIcon from "$lib/components/FormatIcon.svelte";
  import ListInput from "$lib/components/ListInput.svelte";
  import OptionRow from "$lib/components/OptionRow.svelte";
  import VisibilityChoice from "$lib/components/VisibilityChoice.svelte";
  import SigningKeyChoice, { type KeyChoice } from "$lib/components/SigningKeyChoice.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ConfigEditor from "$lib/formats/ConfigEditor.svelte";

  let {}: PageProps = $props();

  const id = $props.id();
  const listHref = href("/settings/repositories");
  // The server's rule for names (repos.NameRE).
  const nameRE = /^[a-z0-9][a-z0-9._-]{0,63}$/;
  const archRE = /^[a-z0-9-]+$/;

  const formats = new Resource(() => api.formats.list());
  const keys = new Resource(() => api.signingKeys.list());

  let name = $state("");
  let formatName = $state("");
  let description = $state("");
  let anonymous = $state(false);
  let archList = $state(["amd64", "arm64"]);
  let keyChoice = $state<KeyChoice>({ mode: "existing", name: "" });
  // undefined: the format's default config; null: the editor holds an invalid config.
  let config = $state<RepoConfig | null | undefined>(undefined);
  let busy = $state(false);
  let error = $state("");

  $effect(() => {
    if (!formatName && formats.data?.length) formatName = formats.data[0].name;
  });

  const format = $derived(formats.data?.find((f) => f.name === formatName));
  const debian = $derived(formatName === "debian");
  const otherOptions = $derived(!!format && !debian && Object.keys(format.default_config ?? {}).length > 0);

  const nameError = $derived(
    name && !nameRE.test(name) ? "Use lowercase letters, digits, dots, dashes and underscores, and start with a letter or a digit." : "",
  );
  const archError = $derived.by(() => {
    if (!debian) return "";
    if (archList.length === 0) return "Enter at least one architecture, for example amd64.";
    const bad = archList.find((a) => !archRE.test(a) || a === "all");
    if (bad) return `${bad} is not an architecture name. Use names such as amd64 or arm64.`;
   return "";
  });
  const keyMissing = $derived(debian && !keyChoice.name);
  const canCreate = $derived(!!format && !!name && !nameError && !archError && !keyMissing && config !== null && !busy);

  async function create(e: SubmitEvent) {
    e.preventDefault();
    if (!format || !canCreate) return;
    busy = true;
    error = "";
    // The key this attempt created; deleted again when the repository is not created, so a retry
    // can create it again.
    let createdKey = "";
    try {
      let cfg: RepoConfig = config ?? format.default_config ?? {};
      if (debian) {
        if (keyChoice.mode === "new") createdKey = (await api.signingKeys.create({ name: keyChoice.name.trim() })).name;
        cfg = { ...format.default_config, architectures: archList, signing_key: keyChoice.name.trim() };
      }
      const repo = await api.repos.create({
        name,
        format: format.name,
        description: description.trim(),
        anonymous_read: anonymous,
        config: cfg,
      });
      toast.success(`Created the repository ${repo.name}`);
      navigate(repoPath(repo.name, "setup"));
    } catch (err) {
      error = errorMessage(err);
      if (createdKey) {
        try {
          await api.signingKeys.delete(createdKey);
        } catch (e) {
          error += ` The new signing key ${createdKey} stays; delete it in Signing keys if you do not need it (${errorMessage(e)}).`;
        }
      }
      keys.reload();
    } finally {
      busy = false;
    }
  }
</script>

<PageHeader title="New repository" description="A repository holds one kind of package. Its name and format stay fixed." />

{#snippet submitRow()}
  <Button type="submit" disabled={!canCreate}>
    {#if busy}<Spinner />{/if}
    Create repository
  </Button>
  <Button variant="ghost" href={listHref}>Cancel</Button>
  {#if error}<div class="w-full"><ErrorState inline {error} /></div>{/if}
{/snippet}

{#if formats.loading && !formats.data}
  <Loading rows={6} />
{:else if formats.error && !formats.data}
  <ErrorState error={formats.error} title="The formats did not load" onRetry={formats.reload} />
{:else}
  <FormPage>
    <form onsubmit={create} autocomplete="off">
      <FormSection title="Repository">
        <FormGrid>
          <FormRow label="Name" for="{id}-name" help="Builds and apt use the name in the address of the repository." error={nameError || undefined}>
            <Input id="{id}-name" spellcheck={false} aria-describedby="{id}-name-help" aria-invalid={nameError ? true : undefined} bind:value={name} />
          </FormRow>
          <FormRow label="Description" for="{id}-desc" help="The list of repositories shows this text after the name.">
            <Input id="{id}-desc" maxlength={500} aria-describedby="{id}-desc-help" bind:value={description} />
          </FormRow>
        </FormGrid>
      </FormSection>

      <FormSection title="Format">
        <RadioGroup.Root
          class="grid gap-1 sm:grid-cols-2"
          aria-label="Format"
          value={formatName}
          onValueChange={(v) => {
            formatName = v;
            config = undefined;
          }}
        >
          {#each formats.data ?? [] as f (f.name)}
            <OptionRow id="{id}-format-{f.name}" label={f.title} description={formatLook(f.name).description}>
              {#snippet control()}
                <span class="flex items-center gap-3">
                  <RadioGroup.Item id="{id}-format-{f.name}" value={f.name} />
                  <FormatIcon format={f.name} class="size-5 text-muted-foreground" />
                </span>
              {/snippet}
            </OptionRow>
          {/each}
        </RadioGroup.Root>
      </FormSection>

      <FormSection title="Who can download" footer={debian || otherOptions ? undefined : submitRow}>
        <VisibilityChoice bind:anonymous />
      </FormSection>

      {#if debian}
        <FormSection title="Debian packages">
          <FormGrid>
            <FormRow label="Architectures" for="{id}-arch" help="Machines with apt get packages for these processor types." error={archError || undefined}>
              <ListInput id="{id}-arch" values={archList} onchange={(v) => (archList = v)} normalize={(s) => s.toLowerCase()} placeholder="Type an architecture, then Enter" invalid={!!archError} describedby="{id}-arch-help" label="architecture" />
            </FormRow>
          </FormGrid>
        </FormSection>

        <FormSection title="Signing key" footer={submitRow}>
          {#if keys.loading && !keys.data}
            <Loading rows={2} />
          {:else if keys.error && !keys.data}
            <ErrorState error={keys.error} title="The signing keys did not load" onRetry={keys.reload} />
          {:else}
            <SigningKeyChoice keys={keys.data ?? []} repoName={name} bind:choice={keyChoice} />
          {/if}
        </FormSection>
      {:else if otherOptions && format}
        <FormSection title="{format.title} options" footer={submitRow}>
          <FormGrid>
            {#key format.name}
              <ConfigEditor format={format.name} value={format.default_config} onChange={(v) => (config = v)} />
            {/key}
          </FormGrid>
        </FormSection>
      {/if}
    </form>
  </FormPage>
{/if}