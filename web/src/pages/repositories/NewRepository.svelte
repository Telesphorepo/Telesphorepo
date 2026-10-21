<!--
  New repository (All repositories). The first choice is what to create: a repository, a proxy of a
  remote repository, or a group of repositories. Each choice offers only the formats that take it
  (GET /api/formats, `kinds`).

  A repository: name, format, description and who can download, the format's options (the
  fields of config in its descriptor), and a section for each repository field of the format
  (repo_fields), with the control that a UI module registers for it: the signing key of the
  signing module is a key that exists, or a new key that the page creates first (POST
  /api/signing-keys, then POST /api/repos).

  A proxy: the remote address (with well-known remotes as quick choices) and optional credentials.
  The server checks the remote before it creates the proxy. When the remote does not answer, the
  page shows the error and "Create anyway" sends the same request with force.

  A group: its members, picked from the hosted repositories and proxies of the format. The page
  orders them with the hosted repositories first, so a remote cannot shadow your own files. The
  group's settings change the order later.
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import type { PageProps } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { href, navigate, repoPath, repoSettingsPath } from "$lib/router.svelte";
  import { api, ApiError, errorMessage, type NewRepo, type Repo, type RepoConfig, type RepoKind } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import { normalizeRemote, remoteError } from "$lib/remotes";
  import { repoField } from "../../modules";
  import type { RepoFieldState } from "../../modules/types";
  import * as RadioGroup from "$lib/components/ui/radio-group";
  import { Button } from "$lib/components/ui/button";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import { Input } from "$lib/components/ui/input";
  import { Spinner } from "$lib/components/ui/spinner";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import FormPage from "$lib/components/FormPage.svelte";
  import FormSection from "$lib/components/FormSection.svelte";
  import FormGrid from "$lib/components/FormGrid.svelte";
  import FormRow from "$lib/components/FormRow.svelte";
  import FormatIcon from "$lib/components/FormatIcon.svelte";
  import OptionRow from "$lib/components/OptionRow.svelte";
  import RepoKindIcon from "$lib/components/RepoKindIcon.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import VisibilityChoice from "$lib/components/VisibilityChoice.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import ConfigEditor from "$lib/formats/ConfigEditor.svelte";
  import PackageIcon from "@lucide/svelte/icons/package";
  import CloudDownloadIcon from "@lucide/svelte/icons/cloud-download";
  import LayersIcon from "@lucide/svelte/icons/layers";

  let {}: PageProps = $props();

  const id = $props.id();
  const listHref = href("/settings/repositories");
  // The server's rule for names (repos.NameRE).
  const nameRE = /^[a-z0-9][a-z0-9._-]{0,63}$/;
  const maxMembers = 50;

  const formats = new Resource(() => api.formats.list());
  // The repositories a group can read; loaded when the person chooses a group.
  const repos = new Resource(() => api.repos.list(), { lazy: true });

  const kindChoices: { kind: RepoKind; label: string; description: string; icon: typeof PackageIcon }[] = [
    { kind: "hosted", label: "A repository", description: "You publish your own packages here.", icon: PackageIcon },
    {
      kind: "proxy",
      label: "A proxy of a remote repository",
      description: "The server gets packages from a remote repository on first request, keeps them and serves them from here.",
      icon: CloudDownloadIcon,
    },
    {
      kind: "group",
      label: "A group of repositories",
      description: "One address reads several repositories of one format, in the order you choose.",
      icon: LayersIcon,
    },
  ];

  let kind = $state<RepoKind>("hosted");
  let name = $state("");
  let formatName = $state("");
  let description = $state("");
  let anonymous = $state(false);
  // The state of each repository field control, by field name.
  let fieldStates = $state<Record<string, RepoFieldState>>({});
  // undefined: the format's default config; null: the editor holds an invalid config.
  let config = $state<RepoConfig | null | undefined>(undefined);
  // A proxy.
  let remoteURL = $state("");
  let username = $state("");
  let password = $state("");
  // A group: the chosen member names.
  let chosen = $state<string[]>([]);
  let busy = $state(false);
  let error = $state("");
  // The remote did not answer: the server created nothing, and "Create anyway" can force it.
  let remoteFailed = $state(false);

  const kindsOf = (f: { kinds?: RepoKind[] }) => f.kinds ?? ["hosted"];
  const offered = $derived(kindChoices.filter((c) => (formats.data ?? []).some((f) => kindsOf(f).includes(c.kind))));
  const kindFormats = $derived((formats.data ?? []).filter((f) => kindsOf(f).includes(kind)));

  // Keep a format that takes the chosen kind.
  $effect(() => {
    if (kindFormats.length && !kindFormats.some((f) => f.name === formatName)) {
      formatName = kindFormats[0].name;
      config = undefined;
    }
  });
  $effect(() => {
    if (kind === "group" && !repos.data && !repos.loading && !repos.error) repos.reload();
  });

  const format = $derived(kindFormats.find((f) => f.name === formatName));
  const options = $derived(kind === "hosted" && !!format && format.config.fields.length > 0);
  const presets = $derived(format?.remote_presets ?? []);
  // The repository fields of the format and kind that a UI module has a control for.
  const fieldDefs = $derived((format?.repo_fields[kind] ?? []).map(repoField).filter((d) => !!d));
  const fieldsReady = $derived(fieldDefs.every((d) => fieldStates[d.field]?.ready === true));
  // The last section holds the submit row.
  const lastSection = $derived(fieldDefs.length ? "field" : options ? "options" : "visibility");

  // The repositories a group of this format can read: hosted ones first, then proxies, each by name.
  const rank = (r: Repo) => (r.kind === "proxy" ? 1 : 0);
  const candidates = $derived(
    (repos.data ?? [])
      .filter((r) => r.format === formatName && (r.kind ?? "hosted") !== "group")
      .sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name)),
  );
  // The members in read order: the order of the list, so hosted repositories come first.
  const members = $derived(candidates.filter((r) => chosen.includes(r.name)).map((r) => r.name));

  const nameError = $derived(
    name && !nameRE.test(name) ? "Use lowercase letters, digits, dots, dashes and underscores, and start with a letter or a digit." : "",
  );
  const urlError = $derived(kind === "proxy" ? remoteError(remoteURL) : "");
  const credentialsError = $derived(
    kind === "proxy" && !!password && !username.trim() ? "Enter the username that goes with the password." : "",
  );
  const kindReady = $derived(
    kind === "proxy"
      ? !!remoteURL.trim() && !urlError && !credentialsError
      : kind === "group"
        ? members.length > 0 && members.length <= maxMembers
        : fieldsReady && config !== null,
  );
  const canCreate = $derived(!!format && !!name && !nameError && kindReady && !busy);
  const submitLabel = $derived(kind === "proxy" ? "Create proxy" : kind === "group" ? "Create group" : "Create repository");

  function chooseKind(v: string) {
    kind = v as RepoKind;
    error = "";
    remoteFailed = false;
  }

  function toggleMember(n: string, on: boolean) {
    chosen = on ? [...chosen, n] : chosen.filter((x) => x !== n);
  }

  async function create(e: SubmitEvent) {
    e.preventDefault();
    await send(false);
  }

  async function send(force: boolean) {
    if (!format || !canCreate) return;
    busy = true;
    error = "";
    remoteFailed = false;
    // The field controls whose prepare ran; each undoes it when the repository is not created, so a
    // retry can do it again.
    const prepared: RepoFieldState[] = [];
    try {
      const body: NewRepo = { name, format: format.name, description: description.trim(), anonymous_read: anonymous, kind };
      if (kind === "hosted") {
        body.config = config ?? format.config.default ?? {};
        const values: Record<string, unknown> = {};
        for (const d of fieldDefs) {
          const s = fieldStates[d.field];
          if (!s) continue;
          if (s.prepare) {
            await s.prepare();
            prepared.push(s);
          }
          if (s.value !== undefined) values[d.field] = s.value;
        }
        if (Object.keys(values).length) body.fields = values;
      } else if (kind === "proxy") {
        body.proxy = { remote_url: normalizeRemote(remoteURL) };
        if (username.trim()) {
          body.proxy.username = username.trim();
          body.proxy.password = password;
        }
        if (force) body.force = true;
      } else {
        body.members = members;
      }
      const repo = await api.repos.create(body);
      if (kind === "hosted") {
        toast.success(`Created the repository ${repo.name}`);
        navigate(repoPath(repo.name, "setup"));
      } else {
        toast.success(`Created the ${kind === "proxy" ? "proxy" : "group"} ${repo.name}`);
        navigate(repoSettingsPath(repo.name, "overview"));
      }
    } catch (err) {
      error = errorMessage(err);
      remoteFailed = kind === "proxy" && err instanceof ApiError && err.status === 502;
      for (const s of prepared) {
        const note = s.undo ? await s.undo() : "";
        if (note) error += ` ${note}`;
      }
    } finally {
      busy = false;
    }
  }
</script>

<PageHeader title="New repository" description="A repository holds one kind of package. Its name, format and kind stay fixed." />

{#snippet submitRow()}
  <Button type="submit" disabled={!canCreate}>
    {#if busy}<Spinner />{/if}
    {submitLabel}
  </Button>
  <Button variant="ghost" href={listHref}>Cancel</Button>
  {#if error}
    <div class="flex w-full flex-wrap items-center gap-x-3 gap-y-2">
      <ErrorState inline {error} />
      {#if remoteFailed}
        <Button type="button" variant="outline" size="sm" disabled={busy} onclick={() => send(true)}>Create anyway</Button>
      {/if}
    </div>
  {/if}
{/snippet}

{#if formats.loading && !formats.data}
  <Loading rows={6} />
{:else if formats.error && !formats.data}
  <ErrorState error={formats.error} title="The formats did not load" onRetry={formats.reload} />
{:else}
  <FormPage>
    <form onsubmit={create} autocomplete="off">
      {#if offered.length > 1}
        <FormSection title="What to create">
          <RadioGroup.Root class="gap-1" aria-label="What to create" value={kind} onValueChange={chooseKind}>
            {#each offered as c (c.kind)}
              <OptionRow id="{id}-kind-{c.kind}" label={c.label} description={c.description}>
                {#snippet control()}
                  <span class="flex items-center gap-3">
                    <RadioGroup.Item id="{id}-kind-{c.kind}" value={c.kind} />
                    <c.icon aria-hidden="true" class="size-5 text-muted-foreground" />
                  </span>
                {/snippet}
              </OptionRow>
            {/each}
          </RadioGroup.Root>
        </FormSection>
      {/if}

      <FormSection title={kind === "proxy" ? "Proxy" : kind === "group" ? "Group" : "Repository"}>
        <FormGrid>
          <FormRow label="Name" for="{id}-name" help="Builds and clients use the name in the address of the repository." error={nameError || undefined}>
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
            chosen = [];
          }}
        >
          {#each kindFormats as f (f.name)}
            <OptionRow id="{id}-format-{f.name}" label={f.title} description={f.description}>
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

      {#if kind === "proxy"}
        <FormSection title="Remote">
          <FormGrid>
            <FormRow
              label="Remote address"
              for="{id}-remote"
              help="The server gets files from this address the first time a build asks for them."
              error={urlError || undefined}
              full
            >
              <Input
                id="{id}-remote"
                type="url"
                spellcheck={false}
                placeholder="https://"
                aria-describedby="{id}-remote-help"
                aria-invalid={urlError ? true : undefined}
                bind:value={remoteURL}
              />
              {#if presets.length}
                <div class="mt-3 flex flex-wrap gap-2" role="group" aria-label="Well-known remotes">
                  {#each presets as p (p.url)}
                    <Button type="button" variant="outline" size="sm" onclick={() => (remoteURL = p.url)}>{p.label}</Button>
                  {/each}
                </div>
              {/if}
            </FormRow>
            <FormRow label="Username" for="{id}-user" help="Leave it empty when the remote needs no sign-in." error={credentialsError || undefined}>
              <Input id="{id}-user" spellcheck={false} autocomplete="off" aria-describedby="{id}-user-help" bind:value={username} />
            </FormRow>
            <FormRow label="Password" for="{id}-pass" help="The server keeps it encrypted and never shows it again.">
              <Input id="{id}-pass" type="password" autocomplete="new-password" aria-describedby="{id}-pass-help" bind:value={password} />
            </FormRow>
          </FormGrid>
        </FormSection>
      {:else if kind === "group"}
        <FormSection title="Members" description="The group reads these repositories in this order. You can change the order in the group's settings.">
          {#if repos.loading && !repos.data}
            <Loading rows={3} />
          {:else if repos.error && !repos.data}
            <ErrorState error={repos.error} title="The repositories did not load" onRetry={repos.reload} />
          {:else if candidates.length}
            <div class="flex flex-col gap-1" role="group" aria-label="Members">
              {#each candidates as r (r.name)}
                {@const n = members.indexOf(r.name) + 1}
                <OptionRow id="{id}-member-{r.name}" label={r.name} description={r.description || undefined}>
                  {#snippet control()}
                    <span class="flex items-center gap-3">
                      <Checkbox id="{id}-member-{r.name}" checked={n > 0} onCheckedChange={(v) => toggleMember(r.name, v === true)} />
                      <span class="w-5 text-right text-label tabular-nums" aria-hidden="true">{n || ""}</span>
                      <RepoKindIcon kind={r.kind} iconClass="size-4" />
                    </span>
                  {/snippet}
                </OptionRow>
              {/each}
            </div>
            {#if members.length > maxMembers}
              <p class="mt-4 text-meta text-destructive">A group reads at most {maxMembers} repositories. Remove {members.length - maxMembers}.</p>
            {/if}
          {:else}
            <CompactEmpty
              icon={LayersIcon}
              title="{format?.title ?? formatName} repositories"
              state="None yet"
              text="A group reads repositories of its format. Create a repository or a proxy first."
            />
          {/if}
        </FormSection>
      {/if}

      <FormSection title="Who can download" footer={lastSection === "visibility" ? submitRow : undefined}>
        <VisibilityChoice bind:anonymous />
      </FormSection>

      {#if options && format}
        <FormSection title="{format.title} options" footer={lastSection === "options" ? submitRow : undefined}>
          <FormGrid>
            {#key format.name}
              <ConfigEditor fields={format.config.fields} value={format.config.default} onChange={(v) => (config = v)} />
            {/key}
          </FormGrid>
        </FormSection>
      {/if}
      {#if format}
        {#each fieldDefs as d, i (format.name + d.field)}
          <FormSection title={d.label} footer={i === fieldDefs.length - 1 ? submitRow : undefined}>
            {#await d.component() then mod}
              <mod.default
                id="{id}-field-{d.field}"
                repoName={name}
                {format}
                onchange={(s) => (fieldStates = { ...fieldStates, [d.field]: s })}
              />
            {/await}
          </FormSection>
        {/each}
      {/if}
    </form>
  </FormPage>
{/if}
