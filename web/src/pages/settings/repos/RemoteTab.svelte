<!--
  Repository settings, Remote (a proxy only): the remote address with well-known remotes as quick
  choices, the credentials, and the two times, saved from the save bar (PATCH /api/repos/{name}
  with `proxy`). "Test the remote" in the card's footer checks the saved remote.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import { api, errorMessage, type ProxyInput, type Repo, type RepoProxy } from "$lib/api";
  import { Draft } from "$lib/unsaved.svelte";
  import { DURATION_UNITS, joinSeconds, splitSeconds, type Parts } from "$lib/units";
  import { normalizeRemote, remoteError, remotePresets } from "$lib/remotes";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Separator } from "$lib/components/ui/separator";
  import FormSection from "$lib/components/FormSection.svelte";
  import FormGrid from "$lib/components/FormGrid.svelte";
  import FormRow from "$lib/components/FormRow.svelte";
  import UnitInput from "$lib/components/UnitInput.svelte";
  import CredentialsInput, { type Credentials } from "$lib/components/CredentialsInput.svelte";
  import ConnectionTest, { type TestResult } from "$lib/components/ConnectionTest.svelte";
  import SaveBar from "$lib/components/SaveBar.svelte";

  let { repo, onChanged }: { repo: Repo; onChanged: () => void } = $props();

  const id = $props.id();

  type Form = { url: string; creds: Credentials | null | undefined; meta: Parts; missing: Parts };
  const formOf = (p: RepoProxy | undefined): Form => ({
    url: p?.remote_url ?? "",
    creds: undefined,
    meta: splitSeconds(p?.metadata_ttl ?? 1800),
    missing: splitSeconds(p?.not_found_ttl ?? 300),
  });
  const draft = new Draft<Form>(untrack(() => formOf(repo.proxy)));
  const presets = $derived(remotePresets[repo.format] ?? []);

  const urlError = $derived(draft.value.url.trim() ? remoteError(draft.value.url) : "Enter the address of the remote repository.");
  const credsError = $derived(
    draft.value.creds && !draft.value.creds.username.trim() ? "Enter the username, or Cancel to keep the saved credentials." : "",
  );
  const metaError = $derived(joinSeconds(draft.value.meta) === null ? "Enter a time greater than zero." : "");
  const missingError = $derived(joinSeconds(draft.value.missing) === null ? "Enter a time greater than zero." : "");

  async function save() {
    const v = draft.value;
    const problem = urlError || credsError || metaError || missingError;
    if (problem) throw new Error(problem);
    const s = draft.saved;
    const patch: ProxyInput = {};
    if (normalizeRemote(v.url) !== normalizeRemote(s.url)) patch.remote_url = normalizeRemote(v.url);
    const meta = joinSeconds(v.meta)!;
    const missing = joinSeconds(v.missing)!;
    if (meta !== joinSeconds(s.meta)) patch.metadata_ttl = meta;
    if (missing !== joinSeconds(s.missing)) patch.not_found_ttl = missing;
    if (v.creds === null) {
      patch.username = "";
      patch.password = "";
    } else if (v.creds) {
      patch.username = v.creds.username.trim();
      patch.password = v.creds.password;
    }
    const saved = await api.repos.update(repo.name, { proxy: patch });
    draft.commit(formOf(saved.proxy));
    result = null;
    toast.success(`Saved the remote of ${repo.name}`);
    onChanged();
  }

  let testing = $state(false);
  let result = $state<TestResult | null>(null);

  async function test() {
    testing = true;
    try {
      await api.repos.testProxy(repo.name);
      result = { ok: true, text: "The remote answered." };
    } catch (e) {
      result = { ok: false, text: `${errorMessage(e)} Check the address and the credentials.` };
    } finally {
      testing = false;
      onChanged();
    }
  }
</script>

<FormSection title="Remote">
  {#snippet footer()}
    <ConnectionTest label="Test the remote" {testing} {result} onTest={test} disabled={draft.count > 0} />
    {#if draft.count > 0}<p class="text-meta text-muted-foreground">Save the changes first. The test uses the saved remote.</p>{/if}
  {/snippet}
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
        bind:value={draft.value.url}
      />
      {#if presets.length}
        <div class="flex flex-wrap gap-2" role="group" aria-label="Well-known remotes">
          {#each presets as p (p.url)}
            <Button type="button" variant="outline" size="sm" onclick={() => (draft.value.url = p.url)}>{p.label}</Button>
          {/each}
        </div>
      {/if}
    </FormRow>
    <FormRow label="Credentials" for="{id}-creds" help="The username and password the server signs in to the remote with." error={credsError || undefined}>
      <CredentialsInput
        id="{id}-creds"
        set={repo.proxy?.has_credentials ?? false}
        change={draft.value.creds}
        onchange={(c) => (draft.value.creds = c)}
        invalid={!!credsError}
        describedby="{id}-creds-help"
      />
    </FormRow>
  </FormGrid>
  <Separator class="my-6" />
  <FormGrid>
    <FormRow
      label="Check changed files after"
      for="{id}-meta"
      help="The server uses a file that changes, such as maven-metadata.xml or InRelease, this long before it asks the remote again."
      error={metaError || undefined}
    >
      <UnitInput id="{id}-meta" bind:num={draft.value.meta.num} bind:unit={draft.value.meta.unit} units={DURATION_UNITS} label="Check changed files after" invalid={!!metaError} describedby="{id}-meta-help" />
    </FormRow>
    <FormRow
      label="Remember missing files for"
      for="{id}-missing"
      help="A file that the remote does not have answers 404 this long, without a new request to the remote."
      error={missingError || undefined}
    >
      <UnitInput id="{id}-missing" bind:num={draft.value.missing.num} bind:unit={draft.value.missing.unit} units={DURATION_UNITS} label="Remember missing files for" invalid={!!missingError} describedby="{id}-missing-help" />
    </FormRow>
  </FormGrid>
</FormSection>

<SaveBar count={draft.count} onDiscard={draft.reset} onSave={save} />
