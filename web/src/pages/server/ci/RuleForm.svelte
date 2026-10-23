<!--
  The page of one CI pipeline (a trust policy in the API). A person answers in the words they
  know: a GitHub repository with its branches, tags and workflow file, or a Jenkins address and
  its jobs. The page builds the token conditions from the answers. "Edit the token conditions"
  shows the conditions themselves, for a check the answers do not cover; another CI system always
  uses them. The workflow or Jenkinsfile step for the chosen repositories shows on the same page.

  A new pipeline is a setup of numbered steps that ends with Create; it can start from a
  repository (`startRepo`) and return to `back` afterwards. An existing pipeline saves its edits
  from the SaveBar. The parent keys it by id (null: a new pipeline).
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { toast } from "svelte-sonner";
  import { api, errorMessage, type Trust, type TrustInput } from "$lib/api";
  import { Resource } from "$lib/load.svelte";
  import { Draft } from "$lib/unsaved.svelte";
  import { go, href, navigate } from "$lib/router.svelte";
  import { auth } from "$lib/auth.svelte";
  import { formats } from "$lib/formats/registry.svelte";
  import * as RadioGroup from "$lib/components/ui/radio-group";
  import * as Alert from "$lib/components/ui/alert";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Textarea } from "$lib/components/ui/textarea";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import { Separator } from "$lib/components/ui/separator";
  import { Spinner } from "$lib/components/ui/spinner";
  import PageHeader from "$lib/components/PageHeader.svelte";
  import FormPage from "$lib/components/FormPage.svelte";
  import FormGrid from "$lib/components/FormGrid.svelte";
  import FormRow from "$lib/components/FormRow.svelte";
  import FormField from "$lib/components/FormField.svelte";
  import SwitchRow from "$lib/components/SwitchRow.svelte";
  import ListInput from "$lib/components/ListInput.svelte";
  import CompactEmpty from "$lib/components/CompactEmpty.svelte";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import Loading from "$lib/components/Loading.svelte";
  import SaveBar from "$lib/components/SaveBar.svelte";
  import ConfirmDialog from "$lib/components/ConfirmDialog.svelte";
  import ShortFormDialog from "$lib/components/ShortFormDialog.svelte";
  import Area from "./Area.svelte";
  import OptionRow from "$lib/components/OptionRow.svelte";
  import WorkflowSetup from "./WorkflowSetup.svelte";
  import AdmissionTicket from "./AdmissionTicket.svelte";
  import FormatIcon from "$lib/components/FormatIcon.svelte";
  import {
    conditionField,
    conditionsOf,
    emptyGitHub,
    emptyJenkins,
    gitHubAccepts,
    gitHubRepo,
    jenkinsAccepts,
    jenkinsJob,
    jenkinsURL,
    parseGitHub,
    workflowFile,
    knownFields,
    readGitHub,
    readJenkins,
    systemOf,
    systemTitle,
    type Accepts,
    type Condition,
    type GitHubJobs,
    type JenkinsJobs,
    type System,
  } from "./presets";
  import WorkflowIcon from "@lucide/svelte/icons/workflow";
  import DatabaseIcon from "@lucide/svelte/icons/database";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import XIcon from "@lucide/svelte/icons/x";
  import TriangleAlertIcon from "@lucide/svelte/icons/triangle-alert";

  let { id, startRepo = null, back = null }: { id: number | null; startRepo?: string | null; back?: string | null } = $props();

  const fid = $props.id();
  const isNew = untrack(() => id === null);
  const listPath = "/settings/ci-pipelines";
  /** Where Create and Cancel return to: a Settings or repository page that opened this one, else the list. */
  const returnTo = untrack(() => (back && /^\/(settings|repo)\/[^/]/.test(back) && !back.includes("//") ? back : null));

  const rule = new Resource<Trust | null>(() => (id === null ? Promise.resolve(null) : api.publish.trusts.get(id)));
  const repos = new Resource(() => api.repos.list());
  // A pipeline without its own audience checks tokens against the public address of the server.
  // The settings need one of General, Sign-in options, Storage or Network; without them the page skips the check.
  const readsSettings = (["general", "signin", "storage", "network"] as const).some((p) => auth.can(p));
  const settings = new Resource(() => (readsSettings ? api.settings.get() : Promise.resolve(null)));
  const noBaseURL = $derived(
    !!settings.data && !(settings.data.groups.flatMap((g) => g.settings).find((s) => s.key === "base_url")?.value ?? ""),
  );
  // A CI pipeline publishes, so it names only hosted repositories, and only those where the person holds CI pipelines.
  const sortedRepos = $derived(
    (repos.data ?? []).filter((r) => r.kind === "hosted" && auth.canRepo(r.name, "ci.pipelines")).sort((a, b) => a.name.localeCompare(b.name)),
  );

  type Value = {
    name: string;
    on: boolean;
    repos: string[];
    system: System;
    gh: GitHubJobs;
    jk: JenkinsJobs;
    /** The conditions themselves, when the person edits them by hand. */
    issuer: string;
    audience: string;
    jwks: string;
    conditions: Condition[];
  };

  function fromTrust(t: Trust): Value {
    const system = systemOf(t.issuer);
    return {
      name: t.name,
      on: !t.disabled,
      repos: [...t.repos].sort(),
      system,
      gh: readGitHub(t) ?? emptyGitHub(),
      jk: readJenkins(t) ?? (system === "jenkins" ? { url: t.issuer.slice(0, -5), jobs: [], jwks: t.jwks } : emptyJenkins()),
      issuer: t.issuer,
      audience: t.audience,
      jwks: t.jwks,
      conditions: conditionsOf(system, t.claims),
    };
  }

  /** A stored pipeline opens on its conditions when the answers cannot say what they check. */
  const needsConditions = (t: Trust) => {
    const s = systemOf(t.issuer);
    return s === "other" || (s === "github" ? !readGitHub(t) : !readJenkins(t));
  };

  let draft = $state<Draft<Value> | null>(null);
  /** The conditions show in place of the answers. */
  let byHand = $state(false);
  /** A new pipeline takes its name from the answers until the person types one. */
  let nameTouched = $state(false);

  // Start the form once: from the pipeline, or from GitHub Actions for a new one.
  $effect(() => {
    if (draft || rule.loading) return;
    const t = rule.data;
    untrack(() => {
      if (t) {
        draft = new Draft(fromTrust(t));
        byHand = needsConditions(t);
      } else if (t === null) {
        draft = new Draft<Value>({
          name: "",
          on: true,
          repos: [],
          system: "github",
          gh: emptyGitHub(),
          jk: emptyJenkins(),
          issuer: "",
          audience: "",
          jwks: "",
          conditions: [],
        });
      }
    });
  });

  // A new pipeline that starts from a repository has it chosen.
  let preselected = false;
  $effect(() => {
    if (!isNew || preselected || !draft || !repos.data) return;
    preselected = true;
    const r = startRepo;
    if (r && repos.data.some((x) => x.name === r)) untrack(() => draft && (draft.value.repos = [r]));
  });

  const autoName = $derived.by(() => {
    const v = draft?.value;
    if (!v) return "";
    if (v.system === "github") return gitHubRepo(v.gh.repo);
    if (v.system === "jenkins") return v.jk.jobs[0] ?? "";
    return "";
  });
  $effect(() => {
    const n = autoName;
    if (isNew && !nameTouched && draft) untrack(() => draft && (draft.value.name = n));
  });

  function claimsOf(conditions: Condition[]): Record<string, string[]> {
    const claims: Record<string, string[]> = {};
    for (const c of conditions) if (c.key.trim() && c.values.length) claims[c.key.trim()] = c.values;
    return claims;
  }

  function accepts(v: Value, hand: boolean): Accepts {
    if (hand || v.system === "other") return { issuer: v.issuer.trim(), audience: v.audience.trim(), jwks: v.jwks.trim(), claims: claimsOf(v.conditions) };
    return v.system === "github" ? gitHubAccepts(v.gh) : jenkinsAccepts(v.jk);
  }

  /** What is wrong with the form, in one sentence, or "". */
  function problem(v: Value, hand: boolean): string {
    if (!hand && v.system === "github" && !/^[\w.-]+\/[\w.-]+$/.test(gitHubRepo(v.gh.repo)))
      return "Write the GitHub repository as owner/name, such as acme/app.";
    if (!hand && v.system === "jenkins") {
      if (!/^https?:\/\/[^/]+/.test(v.jk.url.trim())) return "Write the Jenkins address, such as https://jenkins.example.com.";
      if (!v.jk.jobs.length) return "Add at least one Jenkins job.";
    }
    const a = accepts(v, hand);
    if (!a.issuer) return "Write the CI system address.";
    if (Object.keys(a.claims).length === 0) return "Give at least one condition a value. Without one, every job of the CI system could publish.";
    if (!v.name.trim()) return "Give the pipeline a name.";
    if (v.repos.length === 0) return "Choose at least one repository.";
    return "";
  }

  function body(v: Value, hand: boolean): TrustInput {
    const p = problem(v, hand);
    if (p) throw new Error(p);
    const a = accepts(v, hand);
    return { name: v.name.trim(), issuer: a.issuer, audience: a.audience, claims: a.claims, jwks: a.jwks, repos: v.repos, disabled: !v.on };
  }

  // The SaveBar counts what the server would store differently, so showing the conditions
  // without changing them is not a change.
  const sorted = (c: Record<string, string[]>) => JSON.stringify(Object.keys(c).sort().map((k) => [k, c[k]]));
  const changes = $derived.by(() => {
    const t = rule.data;
    if (!t || !draft) return 0;
    const v = draft.value;
    const a = accepts(v, byHand);
    return [
      v.name.trim() !== t.name,
      v.on === t.disabled,
      v.repos.join("\n") !== [...t.repos].sort().join("\n"),
      a.issuer !== t.issuer,
      a.audience !== t.audience,
      a.jwks !== t.jwks,
      sorted(a.claims) !== sorted(t.claims),
    ].filter(Boolean).length;
  });

  function discard() {
    draft?.reset();
    if (rule.data) byHand = needsConditions(rule.data);
  }

  // A new pipeline: Create at the end of the form.
  let busy = $state(false);
  let error = $state("");
  async function create(e: SubmitEvent) {
    e.preventDefault();
    if (!draft || busy) return;
    error = "";
    busy = true;
    try {
      const t = await api.publish.trusts.create(body(draft.value, byHand));
      toast.success(`Created the CI pipeline ${t.name}`);
      if (returnTo) go("#" + returnTo, { replace: true });
      else navigate(`${listPath}/${t.id}`, { replace: true });
    } catch (err) {
      error = errorMessage(err);
    } finally {
      busy = false;
    }
  }

  // An existing pipeline: the SaveBar.
  async function save() {
    if (!draft || id === null) return;
    const t = await api.publish.trusts.update(id, body(draft.value, byHand));
    rule.data = t;
    draft.commit(fromTrust(t));
    byHand = needsConditions(t);
    toast.success(`Saved the CI pipeline ${t.name}`);
  }

  function useSystem(s: string) {
    if (!draft) return;
    draft.value.system = s as System;
    byHand = s === "other";
  }

  // A pasted GitHub or Jenkins address becomes what it names, at once on a paste and else when
  // the field is left: a file's address fills the repository and the workflow file.
  const pasted = (e: Event) => (e as InputEvent).inputType === "insertFromPaste";
  function tidyRepo() {
    if (!draft) return;
    const gh = draft.value.gh;
    const p = parseGitHub(gh.repo);
    if (p.repo) gh.repo = p.repo;
    if (p.workflow && !gh.workflow.trim()) gh.workflow = p.workflow;
  }
  function tidyWorkflow() {
    if (!draft) return;
    const gh = draft.value.gh;
    if (!gh.workflow.trim()) return;
    const p = parseGitHub(gh.workflow);
    if (p.repo && !gh.repo.trim()) gh.repo = p.repo;
    gh.workflow = workflowFile(gh.workflow);
  }
  function jobOf(s: string): string {
    const jk = draft?.value.jk;
    if (jk && !jk.url.trim() && /^https?:\/\/.+\/job\//i.test(s)) jk.url = jenkinsURL(s);
    return jenkinsJob(s);
  }

  function setRepo(name: string, on: boolean) {
    if (!draft) return;
    const r = draft.value.repos.filter((x) => x !== name);
    draft.value.repos = on ? [...r, name].sort() : r;
  }

  // --- the conditions by hand ---
  function showConditions() {
    if (!draft) return;
    const v = draft.value;
    const a = accepts(v, false);
    v.issuer = a.issuer;
    v.audience = a.audience;
    v.jwks = a.jwks;
    v.conditions = conditionsOf(v.system, a.claims);
    byHand = true;
  }

  let simpleOpen = $state(false);
  function showAnswers() {
    if (!draft) return;
    const v = draft.value;
    const a = accepts(v, true);
    const read = v.system === "github" ? readGitHub(a) : readJenkins(a);
    if (!read) {
      simpleOpen = true;
      return;
    }
    if (v.system === "github") v.gh = read as GitHubJobs;
    else v.jk = read as JenkinsJobs;
    byHand = false;
  }

  let addOpen = $state(false);
  let newKey = $state("");
  const keyTaken = $derived(!!draft?.value.conditions.some((c) => c.key === newKey.trim()));
  function addCondition(key: string) {
    draft?.value.conditions.push({ key: key.trim(), values: [] });
  }
  const missing = $derived(draft ? knownFields[draft.value.system].filter((f) => !draft!.value.conditions.some((c) => c.key === f.key)) : []);

  let deleteOpen = $state(false);
  async function remove() {
    if (id === null) return;
    await api.publish.trusts.delete(id);
    toast.success(`Deleted the CI pipeline ${rule.data?.name ?? ""}`);
    navigate(listPath, { force: true });
  }

  const quietDanger = "hover:bg-destructive/10 hover:text-destructive";
  const jobsTitle: Record<System, string> = {
    github: "Which runs can publish",
    jenkins: "Which Jenkins jobs can publish",
    other: "Which jobs can publish",
  };
  const systems: { id: System; text: string }[] = [
    { id: "github", text: "Workflows of a GitHub repository publish with no stored secret." },
    { id: "jenkins", text: "Jobs of a Jenkins server with the OpenID Connect Provider plugin." },
    { id: "other", text: "Any CI system that gives its jobs an OpenID Connect identity token." },
  ];
</script>

{#if (rule.loading && rule.data === undefined) || (!draft && !rule.error)}
  <PageHeader title={isNew ? "New CI pipeline" : "CI pipeline"} />
  <Loading rows={6} />
{:else if rule.error && rule.data === undefined}
  <PageHeader title="CI pipeline" />
  <ErrorState error={rule.error} title="The CI pipeline did not load" onRetry={rule.reload} />
{:else if draft}
  {@const v = draft.value}
  {@const hand = byHand || v.system === "other"}
  {@const steps = v.system === "other" ? 3 : 4}
  <PageHeader title={rule.data ? rule.data.name : "New CI pipeline"}>
    {#snippet actions()}
      {#if !isNew}<Button variant="ghost" class={quietDanger} onclick={() => (deleteOpen = true)}>Delete</Button>{/if}
    {/snippet}
  </PageHeader>

  {#snippet nameField()}
    <FormRow label="Name" for="{fid}-name" help="Name it after what it publishes, such as acme/app releases.">
      <Input id="{fid}-name" aria-describedby="{fid}-name-help" bind:value={v.name} oninput={() => (nameTouched = true)} />
    </FormRow>
    {#if !isNew}
      <SwitchRow id="{fid}-on" label="Pipeline is on" help="Jobs of a pipeline that is off cannot publish." bind:checked={v.on} />
    {/if}
  {/snippet}

  {#snippet ticket()}
    <AdmissionTicket
      system={v.system}
      {hand}
      gh={v.gh}
      jk={v.jk}
      accepts={accepts(v, hand)}
      repos={v.repos}
      formatOf={(r) => repos.data?.find((x) => x.name === r)?.format}
    />
  {/snippet}

  {#snippet createRow()}
    <div class="mb-4 w-full xl:hidden">{@render ticket()}</div>
    <Button type="submit" disabled={busy}>
      {#if busy}<Spinner />{/if}
      Create CI pipeline
    </Button>
    <Button variant="ghost" href={href(returnTo ?? listPath)}>Cancel</Button>
    {#if error}<div class="w-full"><ErrorState inline {error} /></div>{/if}
  {/snippet}

  <!-- The admission ticket sits beside the steps on a wide window, sticky under the top bar; on a
       narrower one it sits above Create (a new pipeline) or after the form. -->
  <div class="grid grid-cols-[minmax(0,1fr)] items-start gap-x-6 xl:grid-cols-[minmax(0,1fr)_22rem]">
  <FormPage>
    {#if noBaseURL && !accepts(v, hand).audience}
      <Alert.Root class="mb-6">
        <TriangleAlertIcon />
        <Alert.Title>The server has no public address</Alert.Title>
        <Alert.Description>
          <p>
            CI jobs cannot publish until the server checks the audience of their tokens.
            {#if auth.can("settings.general")}
              Set the public address on
              <a href={href("/settings/general")} class="text-link underline-offset-4 hover:underline">General</a>, or edit the token conditions and give the pipeline an audience.
            {:else}
              Ask an administrator to set the public address, or edit the token conditions and give the pipeline an audience.
            {/if}
          </p>
        </Alert.Description>
      </Alert.Root>
    {/if}
    <form onsubmit={isNew ? create : (e) => e.preventDefault()} autocomplete="off">
      {#if isNew}
        <Area n={1} title="Where the job runs">
          <RadioGroup.Root class="grid gap-1 sm:grid-cols-3" value={v.system} onValueChange={useSystem}>
            {#each systems as s (s.id)}
              <OptionRow id="{fid}-sys-{s.id}" label={systemTitle[s.id]} description={s.text}>
                {#snippet control()}<RadioGroup.Item id="{fid}-sys-{s.id}" value={s.id} />{/snippet}
              </OptionRow>
            {/each}
          </RadioGroup.Root>
        </Area>
      {/if}

      <Area
        n={isNew ? 2 : undefined}
        title={jobsTitle[v.system]}
        description={hand ? "A job can publish when its token matches every condition. A * matches any text." : `Only the ${v.system === "github" ? "runs" : "jobs"} that match every answer can publish.`}
      >
        {#snippet actions()}
          {#if hand}
            <Button variant="outline" size="sm" onclick={() => { newKey = ""; addOpen = true; }}><PlusIcon />Add a condition</Button>
          {/if}
          {#if v.system !== "other"}
            <Button variant="ghost" size="sm" onclick={byHand ? showAnswers : showConditions}>
              {byHand ? "Use the simple form" : "Edit the token conditions"}
            </Button>
          {/if}
        {/snippet}

        {#if !hand && v.system === "github"}
          <FormGrid>
            <FormRow label="GitHub repository" for="{fid}-gh-repo" help="The repository whose workflow publishes.">
              <Input
                id="{fid}-gh-repo"
                spellcheck={false}
                placeholder="https://github.com/acme/app"
                aria-describedby="{fid}-gh-repo-help"
                bind:value={v.gh.repo}
                oninput={(e) => pasted(e) && queueMicrotask(tidyRepo)}
                onchange={tidyRepo}
              />
            </FormRow>
            <FormRow label="Workflow file" for="{fid}-gh-wf" help="Leave it empty to let every workflow of the repository publish.">
              <Input
                id="{fid}-gh-wf"
                spellcheck={false}
                placeholder="https://github.com/acme/app/blob/main/.github/workflows/release.yml"
                aria-describedby="{fid}-gh-wf-help"
                bind:value={v.gh.workflow}
                oninput={(e) => pasted(e) && queueMicrotask(tidyWorkflow)}
                onchange={tidyWorkflow}
              />
              {#snippet more()}The file in .github/workflows that publishes, such as release.yml. The other workflows of the repository then cannot publish, also when they run on the same branch.{/snippet}
            </FormRow>
            <FormRow label="Branches" for="{fid}-gh-br" help="A * matches any text, such as release/* for every release branch.">
              <ListInput id="{fid}-gh-br" values={v.gh.branches} onchange={(x) => (v.gh.branches = x)} placeholder="main" describedby="{fid}-gh-br-help" label="branch" />
            </FormRow>
            <FormRow label="Tags" for="{fid}-gh-tag" help="A tag such as v* lets every version tag publish.">
              <ListInput id="{fid}-gh-tag" values={v.gh.tags} onchange={(x) => (v.gh.tags = x)} placeholder="v*" describedby="{fid}-gh-tag-help" label="tag" />
            </FormRow>
            {#if !v.gh.branches.length && !v.gh.tags.length}
              <p class="text-meta text-warning sm:col-span-2">Runs on every branch and every tag can publish.</p>
            {/if}
            {@render nameField()}
          </FormGrid>
        {:else if !hand && v.system === "jenkins"}
          <FormGrid>
            <FormRow label="Jenkins address" for="{fid}-jk-url" help="Write the address that you open Jenkins at.">
              <Input
                id="{fid}-jk-url"
                type="url"
                spellcheck={false}
                placeholder="https://jenkins.example.com"
                aria-describedby="{fid}-jk-url-help"
                bind:value={v.jk.url}
                onchange={() => v.jk.url.trim() && (v.jk.url = jenkinsURL(v.jk.url))}
              />
              {#snippet more()}Jenkins gives each job a token with the OpenID Connect Provider plugin. This server reads the keys that check the token from the Jenkins address followed by /oidc.{/snippet}
            </FormRow>
            <FormRow label="Jobs" for="{fid}-jk-jobs" help="Write each full job name with its folders, such as team/app.">
              <ListInput
                id="{fid}-jk-jobs"
                values={v.jk.jobs}
                onchange={(x) => (v.jk.jobs = x)}
                normalize={jobOf}
                placeholder="https://jenkins.example.com/job/team/job/app/"
                describedby="{fid}-jk-jobs-help"
                label="job"
              />
              {#snippet more()}Paste the address of the job, or write its name with its folders. A branch of a multibranch pipeline is a job of its own: write app/main. A * matches any text, so app/* lets every branch of app publish.{/snippet}
            </FormRow>
            <FormRow label="Jenkins keys" for="{fid}-jk-keys" help="Leave this empty when this server can reach Jenkins." full>
              <Textarea id="{fid}-jk-keys" rows={3} spellcheck={false} placeholder={'{"keys": [...]}'} aria-describedby="{fid}-jk-keys-help" bind:value={v.jk.jwks} />
              {#snippet more()}Open the Jenkins address followed by /oidc/jwks and paste the whole page here. Paste it again when Jenkins changes its keys.{/snippet}
            </FormRow>
            {@render nameField()}
          </FormGrid>
        {:else}
          <div class="flex flex-col gap-6">
            <FormGrid>
              <FormRow label="CI system address" for="{fid}-issuer" help="The server reads the signing keys of the CI system from this address." aka="Issuer">
                <Input id="{fid}-issuer" type="url" spellcheck={false} placeholder="https://ci.example.com" aria-describedby="{fid}-issuer-help" bind:value={v.issuer} />
              </FormRow>
              <FormRow label="Audience" for="{fid}-aud" help="Leave it empty to use the address of this server." aka="aud">
                <Input id="{fid}-aud" spellcheck={false} aria-describedby="{fid}-aud-help" bind:value={v.audience} />
              </FormRow>
            </FormGrid>
            <Separator />
            {#if v.conditions.length === 0}
              <CompactEmpty icon={WorkflowIcon} text="Add the token fields that only your jobs carry, such as the repository." />
            {:else}
              <FormGrid>
                {#each v.conditions as c, i (c.key + i)}
                  {@const f = conditionField(v.system, c.key)}
                  {@const cid = `${fid}-cond-${i}`}
                  <FormRow label={f.label} for={cid} help={f.help} aka={f.label === c.key ? undefined : c.key} akaLabel="Token field">
                    {#snippet badge()}
                      <Button variant="ghost" size="icon-xs" class={quietDanger} aria-label="Remove the condition {f.label}" onclick={() => v.conditions.splice(i, 1)}>
                        <XIcon />
                      </Button>
                    {/snippet}
                    <ListInput
                      id={cid}
                      values={c.values}
                      onchange={(vals) => (c.values = vals)}
                      placeholder={f.example ? `e.g. ${f.example}` : "Type a value, then Enter"}
                      describedby="{cid}-help"
                      label={f.label}
                    />
                  </FormRow>
                {/each}
              </FormGrid>
            {/if}
            {#if missing.length}
              <div class="flex flex-wrap gap-2">
                {#each missing as f (f.key)}
                  <Button variant="outline" size="sm" onclick={() => addCondition(f.key)}><PlusIcon />{f.label}</Button>
                {/each}
              </div>
            {/if}
            <Separator />
            <FormGrid>
              <FormRow label="Public keys" for="{fid}-jwks" help="Leave this empty when this server can reach the CI system." aka="JWKS" full>
                <Textarea id="{fid}-jwks" rows={3} spellcheck={false} placeholder={'{"keys": [...]}'} aria-describedby="{fid}-jwks-help" bind:value={v.jwks} />
                {#snippet more()}The server checks each token with the keys that the CI system publishes at its address. When this server cannot reach the CI system, paste its keys here. Paste them again when the CI system changes them.{/snippet}
              </FormRow>
              {@render nameField()}
            </FormGrid>
          </div>
        {/if}
      </Area>

      <Area n={isNew ? 3 : undefined} title="Repositories it can publish to" footer={isNew && steps === 3 ? createRow : undefined}>
        {#if repos.loading && !repos.data}
          <Loading rows={3} />
        {:else if repos.error && !repos.data}
          <ErrorState error={repos.error} title="The repositories did not load" onRetry={repos.reload} />
        {:else if sortedRepos.length === 0}
          {#if auth.can("repositories.all")}
            <CompactEmpty icon={DatabaseIcon} text="Create a repository first, then come back to choose it.">
              <Button variant="outline" href={href("/settings/repositories/new")}><PlusIcon />New repository</Button>
            </CompactEmpty>
          {:else}
            <CompactEmpty icon={DatabaseIcon} text="You hold CI pipelines on no repository that takes publishes." />
          {/if}
        {:else}
          <div class="grid gap-1 sm:grid-cols-2">
            {#each sortedRepos as r (r.name)}
              <OptionRow id="{fid}-repo-{r.name}" label={r.name} description={formats.title(r.format)}>
                {#snippet icon()}<FormatIcon format={r.format} />{/snippet}
                {#snippet control()}
                  <Checkbox id="{fid}-repo-{r.name}" checked={v.repos.includes(r.name)} onCheckedChange={(on) => setRepo(r.name, on === true)} />
                {/snippet}
              </OptionRow>
            {/each}
          </div>
        {/if}
      </Area>

      {#if v.system !== "other"}
        <Area
          n={isNew ? 4 : undefined}
          title={v.system === "github" ? "Add the workflow to your repository" : "Add the Jenkinsfile to your repository"}
          footer={isNew ? createRow : undefined}
        >
          <WorkflowSetup system={v.system} repos={v.repos} github={v.gh} formatOf={(r) => repos.data?.find((x) => x.name === r)?.format} />
        </Area>
      {/if}
    </form>
    {#if !isNew}<div class="mt-(--section-gap) xl:hidden">{@render ticket()}</div>{/if}
  </FormPage>
  <aside class="sticky top-20 hidden xl:block" aria-label="Preview">{@render ticket()}</aside>
  </div>

  {#if !isNew}<SaveBar count={changes} onDiscard={discard} onSave={save} />{/if}

  <ShortFormDialog
    bind:open={addOpen}
    title="Add a condition"
    description="Name a field of the job's token. Then type its allowed values in the new row."
    submitLabel="Add condition"
    canSubmit={!!newKey.trim() && !keyTaken}
    onSubmit={() => addCondition(newKey)}
  >
    <FormField label="Token field" for="{fid}-newkey" hint="For example repository_owner, environment or sub." error={keyTaken ? "The pipeline already checks this field." : undefined}>
      <Input id="{fid}-newkey" spellcheck={false} bind:value={newKey} />
    </FormField>
  </ShortFormDialog>

  <ConfirmDialog
    bind:open={simpleOpen}
    title="Use the simple form?"
    confirmLabel="Use the simple form"
    changes={[
      `The simple form cannot hold these conditions`,
      `The ${systemTitle[v.system]} answers replace them`,
    ]}
    onConfirm={() => {
      byHand = false;
    }}
  />

  <ConfirmDialog
    bind:open={deleteOpen}
    title="Delete the CI pipeline {rule.data?.name ?? ''}?"
    confirmLabel="Delete CI pipeline"
    destructive
    changes={["CI jobs can no longer publish with this pipeline", "Ends the publish tokens that jobs got with this pipeline"]}
    onConfirm={remove}
  />
{/if}
