<!--
  Maven upload fields: the coordinates the pom gives (shown, not editable) and inputs for the ones
  it lacks. A lone artifact needs all of them; the server then generates the pom.
-->
<script lang="ts">
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import { Badge } from "$lib/components/ui/badge";
  import { Spinner } from "$lib/components/ui/spinner";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import { errorMessage } from "$lib/api";
  import { coordKeys, isPom, plan as mavenPlan, readPom, type Coords } from "./maven";
  import type { UploadPlan } from "./types";

  let {
    files,
    plan = $bindable(),
    disabled = false,
  }: {
    files: File[];
    plan?: UploadPlan;
    disabled?: boolean;
  } = $props();

  const empty = (): Coords => ({ groupId: "", artifactId: "", version: "", packaging: "" });
  const labels: Record<keyof Coords, { label: string; hint: string; width: string }> = {
    groupId: { label: "Group ID", hint: "com.acme", width: "max-w-sm" },
    artifactId: { label: "Artifact ID", hint: "app", width: "max-w-sm" },
    version: { label: "Version", hint: "1.0.0", width: "w-44" },
    packaging: { label: "Packaging", hint: "jar", width: "w-32" },
  };

  const pom = $derived(files.find((f) => isPom(f.name)));
  let fromPom = $state<Coords>(empty());
  let pomLoading = $state(false);
  let pomError = $state("");
  let typed = $state<Coords>(empty());

  let seq = 0;
  $effect(() => {
    const f = pom;
    const mine = ++seq;
    fromPom = empty();
    pomError = "";
    if (!f) {
      pomLoading = false;
      return;
    }
    pomLoading = true;
    readPom(f).then(
      (c) => {
        if (mine === seq) fromPom = c;
      },
      (e) => {
        if (mine === seq) pomError = errorMessage(e);
      },
    ).finally(() => {
      if (mine === seq) pomLoading = false;
    });
  });

  // Packaging only shapes a generated pom, so it is asked for only without a pom.
  const shown = $derived(coordKeys.filter((k) => k !== "packaging" || !pom));
  const coords = $derived.by(() => {
    const c = empty();
    for (const k of coordKeys) c[k] = fromPom[k] || typed[k].trim();
    return c;
  });
  const result = $derived(mavenPlan(files, coords));

  $effect(() => {
    const fields: Record<string, string> = {};
    for (const k of shown) if (!fromPom[k] && typed[k].trim()) fields[k] = typed[k].trim();
    let problem = result.problem;
    if (pomLoading) problem = "Reading the pom…";
    else if (pomError) problem = pomError;
    plan = { rows: result.rows, fields, problem };
  });
</script>

<div class="flex flex-col gap-4">
  {#if pomLoading}
    <p class="flex items-center gap-2 text-sm text-muted-foreground"><Spinner /> Reading the pom…</p>
  {:else if pomError}
    <ErrorState inline error={pomError} />
  {/if}

  {#if !pomLoading}
    {#if result.dir}
      <div class="flex flex-col gap-1 rounded-lg bg-panel px-4 py-3 text-sm">
        <span class="text-muted-foreground">Coordinates</span>
        <span class="break-all">{coords.groupId}:{coords.artifactId}:{coords.version}</span>
      </div>
    {/if}
    {#each shown as k (k)}
      <div class="flex flex-col gap-2">
        <Label for="maven-{k}">{labels[k].label}</Label>
        {#if fromPom[k]}
          <div class="flex items-center gap-2">
            <span id="maven-{k}" class="text-sm break-all">{fromPom[k]}</span>
            <Badge variant="secondary">from the pom</Badge>
          </div>
        {:else}
          <Input
            id="maven-{k}"
            class={labels[k].width}
            placeholder={labels[k].hint}
            autocomplete="off"
            spellcheck={false}
            {disabled}
            bind:value={typed[k]}
          />
        {/if}
      </div>
    {/each}
  {/if}

  <p class="text-sm text-muted-foreground">
    The server adds .md5, .sha1 and .sha256 files and adds the version to maven-metadata.xml.
    {#if !pom && result.pomPath}
      Without a pom, the server generates <span class="break-all">{result.pomPath}</span>.
    {/if}
  </p>
</div>
