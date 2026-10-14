<!--
  The side sheet that creates or edits a CI trust policy (Settings > Trust policies). The parent
  wraps it in {#key} so the form starts again for each trust it opens.

    {#key seq}<TrustSheet bind:open trust={editing} {repos} onSaved={list.reload} />{/key}
-->
<script lang="ts">
  import { toast } from "svelte-sonner";
  import { api, errorMessage, type Repo, type Trust, type TrustInput } from "$lib/api";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import { Textarea } from "$lib/components/ui/textarea";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import { Switch } from "$lib/components/ui/switch";
  import { Spinner } from "$lib/components/ui/spinner";
  import * as Sheet from "$lib/components/ui/sheet";
  import ErrorState from "./ErrorState.svelte";
  import FormatBadge from "./FormatBadge.svelte";
  import PlusIcon from "@lucide/svelte/icons/plus";
  import XIcon from "@lucide/svelte/icons/x";

  type Row = { key: string; values: string };

  let {
    open = $bindable(false),
    trust,
    repos,
    onSaved,
  }: {
    open?: boolean;
    /** null creates a new policy. */
    trust: Trust | null;
    repos: Repo[];
    onSaved: () => void;
  } = $props();

  const presets = {
    github: {
      label: "GitHub Actions",
      issuer: "https://token.actions.githubusercontent.com",
      claims: ["repository", "ref", "workflow_ref"],
      hint: "For example repository acme/app, ref refs/tags/v*, workflow_ref acme/app/.github/workflows/release.yml@*.",
    },
    jenkins: {
      label: "Jenkins",
      issuer: "https://JENKINS_URL/oidc",
      claims: ["sub"],
      hint: "The issuer is the Jenkins URL followed by /oidc (OpenID Connect Provider plugin). Paste the JWKS if this server cannot reach Jenkins.",
    },
  };

  const id = $props.id();
  const editing = trust !== null;

  /* svelte-ignore state_referenced_locally */
  let name = $state(trust?.name ?? "");
  /* svelte-ignore state_referenced_locally */
  let issuer = $state(trust?.issuer ?? "");
  /* svelte-ignore state_referenced_locally */
  let audience = $state(trust?.audience ?? "");
  /* svelte-ignore state_referenced_locally */
  let jwks = $state(trust?.jwks ?? "");
  /* svelte-ignore state_referenced_locally */
  let disabled = $state(trust?.disabled ?? false);
  /* svelte-ignore state_referenced_locally */
  let selected = $state<string[]>(trust?.repos ?? []);
  /* svelte-ignore state_referenced_locally */
  let rows = $state<Row[]>(
    trust ? Object.entries(trust.claims).map(([key, v]) => ({ key, values: v.join("\n") })) : [{ key: "", values: "" }],
  );
  let showJwks = $state(!!trust?.jwks);
  let busy = $state(false);
  let error = $state("");

  const hint = $derived(Object.values(presets).find((p) => p.issuer === issuer)?.hint);

  function applyPreset(p: (typeof presets)[keyof typeof presets]) {
    issuer = p.issuer;
    rows = p.claims.map((key) => ({ key, values: "" }));
  }

  const split = (s: string) => s.split(/[\n,]/).map((x) => x.trim()).filter(Boolean);

  function toggleRepo(repo: string, on: boolean) {
    selected = on ? [...selected, repo] : selected.filter((r) => r !== repo);
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    error = "";
    const claims: Record<string, string[]> = {};
    for (const r of rows) {
      const v = split(r.values);
      if (r.key.trim() && v.length) claims[r.key.trim()] = v;
    }
    if (Object.keys(claims).length === 0) {
      error = "Add at least one claim with a value. Without a claim, every token of this issuer would match.";
      return;
    }
    if (selected.length === 0) {
      error = "Select at least one repository.";
      return;
    }
    const body: TrustInput = {
      name: name.trim(),
      issuer: issuer.trim(),
      audience: audience.trim(),
      claims,
      jwks: jwks.trim(),
      repos: selected,
      disabled,
    };
    busy = true;
    try {
      if (trust) await api.publish.trusts.update(trust.id, body);
      else await api.publish.trusts.create(body);
      toast.success(trust ? `Saved ${body.name}` : `Created ${body.name}`);
      open = false;
      onSaved();
    } catch (err) {
      error = errorMessage(err);
    } finally {
      busy = false;
    }
  }
</script>

<Sheet.Root bind:open>
  <Sheet.Content side="right" class="overflow-y-auto data-[side=right]:w-full data-[side=right]:sm:max-w-form">
    <Sheet.Header>
      <Sheet.Title>{editing ? `Edit ${trust?.name}` : "New trust policy"}</Sheet.Title>
      <Sheet.Description>
        A CI job that sends a signed identity token matching this policy gets a short publish session for the selected repositories.
      </Sheet.Description>
    </Sheet.Header>

    <form class="flex flex-col gap-6 px-6 pb-6" onsubmit={submit} autocomplete="off">
      {#if !editing}
        <div class="flex flex-wrap items-center gap-2">
          <span class="text-sm text-muted-foreground">Start from</span>
          {#each Object.entries(presets) as [k, p] (k)}
            <Button type="button" variant="outline" size="sm" onclick={() => applyPreset(p)}>{p.label}</Button>
          {/each}
        </div>
      {/if}

      <div class="flex flex-col gap-4">
        <div class="flex flex-col gap-2">
          <Label for="{id}-name">Name</Label>
          <Input id="{id}-name" class="max-w-sm" required bind:value={name} />
        </div>
        <div class="flex flex-col gap-2">
          <Label for="{id}-issuer">Issuer</Label>
          <Input
            id="{id}-issuer"
            class="font-mono"
            required
            placeholder="https://token.actions.githubusercontent.com"
            bind:value={issuer}
          />
          {#if hint}<p class="text-sm text-muted-foreground">{hint}</p>{/if}
        </div>
        <div class="flex flex-col gap-2">
          <Label for="{id}-aud">Audience</Label>
          <Input id="{id}-aud" class="font-mono" placeholder="Empty: the base URL of this server" bind:value={audience} />
        </div>
      </div>

      <fieldset class="flex flex-col gap-4">
        <legend class="text-sm font-semibold">Claims</legend>
        <p class="-mt-2 text-sm text-muted-foreground">
          Every claim listed must match. Give one value per line. A value is exact, or a pattern with <code class="font-mono">*</code>.
        </p>
        {#each rows as r, i (i)}
          <div class="flex items-start gap-2">
            <Input
              class="w-40 font-mono"
              aria-label="Claim name"
              placeholder="repository"
              bind:value={r.key}
            />
            <Textarea
              class="min-h-9 flex-1 font-mono text-[0.8125rem]"
              aria-label="Allowed values for {r.key || 'the claim'}"
              rows={2}
              placeholder="Allowed values"
              bind:value={r.values}
            />
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label="Remove claim {r.key}"
              onclick={() => (rows = rows.filter((_, j) => j !== i))}
            >
              <XIcon />
            </Button>
          </div>
        {/each}
        <div>
          <Button type="button" variant="outline" size="sm" onclick={() => (rows = [...rows, { key: "", values: "" }])}>
            <PlusIcon /> Add claim
          </Button>
        </div>
      </fieldset>

      <fieldset class="flex flex-col gap-3">
        <legend class="text-sm font-semibold">Repositories</legend>
        <p class="-mt-1 text-sm text-muted-foreground">A session from this policy can write to the selected repositories only.</p>
        {#if repos.length === 0}
          <p class="text-sm text-muted-foreground">No repositories exist yet. Create one first.</p>
        {:else}
          <div class="flex max-h-56 flex-col gap-1 overflow-y-auto rounded-xl border p-2">
            {#each repos as r (r.name)}
              <div class="flex items-center gap-3 rounded-lg px-2 py-1.5 hover:bg-muted/60">
                <Checkbox
                  id="{id}-repo-{r.name}"
                  checked={selected.includes(r.name)}
                  onCheckedChange={(v) => toggleRepo(r.name, v === true)}
                />
                <Label for="{id}-repo-{r.name}" class="flex flex-1 items-center justify-between gap-2 font-normal">
                  <span class="font-mono text-[0.8125rem]">{r.name}</span>
                  <FormatBadge format={r.format} />
                </Label>
              </div>
            {/each}
          </div>
        {/if}
      </fieldset>

      <div class="flex flex-col gap-2">
        {#if showJwks}
          <Label for="{id}-jwks">Pasted JWKS (optional)</Label>
          <Textarea id="{id}-jwks" class="font-mono text-[0.8125rem]" rows={6} spellcheck={false} bind:value={jwks} />
          <p class="text-sm text-muted-foreground">When set, the server uses these keys instead of fetching them from the issuer.</p>
        {:else}
          <div>
            <Button type="button" variant="ghost" size="sm" onclick={() => (showJwks = true)}>Paste a JWKS</Button>
          </div>
        {/if}
      </div>

      <div class="flex items-center gap-3">
        <Switch id="{id}-disabled" bind:checked={disabled} />
        <Label for="{id}-disabled">Disabled</Label>
      </div>

      {#if error}<ErrorState inline {error} />{/if}

      <div class="flex flex-wrap gap-2">
        <Button type="submit" disabled={busy}>
          {#if busy}<Spinner />{/if}
          {editing ? "Save changes" : "Create trust policy"}
        </Button>
        <Button type="button" variant="outline" disabled={busy} onclick={() => (open = false)}>Cancel</Button>
      </div>
    </form>
  </Sheet.Content>
</Sheet.Root>
