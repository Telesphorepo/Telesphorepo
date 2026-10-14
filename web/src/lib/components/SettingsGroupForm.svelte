<!--
  Settings from GET /api/settings, rendered generically by setting type and saved with
  PATCH /api/settings after a confirm dialog that lists every changed setting. A field the
  environment forces (`locked`) or the API does not take (`read_only`) is shown disabled. A secret
  never shows its value: it shows "Set" or "Not set" and a Replace button. An empty text field
  resets the setting to its default.

    <SettingsGroupForm settings={group.settings} label={group.label} onSaved={(r) => ...} />

  `sections` splits the fields under h3 headings (a setting in no section goes to the first one).
  `effects` adds lines to the confirm dialog for a pending PATCH body. `extra` renders next to the
  Save button and gets a function that returns the PATCH body of the unsaved edits (Storage and
  SSO use it for their connection tests).
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import { toast } from "svelte-sonner";
  import { api, ApiError, errorMessage, type Setting, type Settings, type SettingsMoveStarted } from "$lib/api";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import { Textarea } from "$lib/components/ui/textarea";
  import { Switch } from "$lib/components/ui/switch";
  import { Badge } from "$lib/components/ui/badge";
  import * as Select from "$lib/components/ui/select";
  import ErrorState from "./ErrorState.svelte";
  import ConfirmDialog from "./ConfirmDialog.svelte";

  type Section = { title: string; description?: string; keys: string[] };

  let {
    settings,
    label,
    visible,
    sections,
    effects,
    onSaved,
    extra,
  }: {
    settings: Setting[];
    /** Names the settings in the dialog and the toast ("Saved storage settings"). */
    label: string;
    /** Hides a setting for the current edits (storage hides the S3 fields for the file store). */
    visible?: (s: Setting, draft: Record<string, string | boolean>) => boolean;
    sections?: Section[];
    /** Extra effects of a save, one line each, for the confirm dialog. */
    effects?: (patch: Record<string, unknown>) => string[];
    /** Called with the server's answer: the new settings, or {move} when a storage move started. */
    onSaved: (result: Settings | SettingsMoveStarted) => void;
    extra?: Snippet<[() => Record<string, unknown>]>;
  } = $props();

  // Edits by key: text for every type except bool.
  let draft = $state<Record<string, string | boolean>>({});
  let replacing = $state<Record<string, boolean>>({});
  let fieldErrors = $state<Record<string, string>>({});
  let confirming = $state(false);

  const idPrefix = $props.id();
  const readOnly = (s: Setting) => s.locked || !!s.read_only;
  const shown = $derived(settings.filter((s) => (visible ? visible(s, draft) : true)));
  const dirty = $derived(Object.keys(draft).length > 0);

  // The fields under each heading; one unnamed group without sections.
  const groups = $derived.by(() => {
    if (!sections?.length) return [{ title: "", description: undefined, fields: shown }];
    const known = new Set(sections.flatMap((s) => s.keys));
    return sections
      .map((sec, i) => ({
        title: sec.title,
        description: sec.description,
        fields: shown.filter((s) => sec.keys.includes(s.key) || (i === 0 && !known.has(s.key))),
      }))
      .filter((g) => g.fields.length > 0);
  });

  const asText = (s: Setting): string => {
    if (Array.isArray(s.value)) return s.value.join("\n");
    return s.value === null || s.value === undefined ? "" : String(s.value);
  };
  const current = (s: Setting): string | boolean => {
    if (s.key in draft) return draft[s.key];
    return s.type === "bool" ? s.value === true : asText(s);
  };
  const isSet = (s: Setting) => (s.value as { set?: boolean } | null)?.set === true;
  const defaultText = (s: Setting) => {
    const d = s.default;
    if (d === null || d === undefined || d === "") return "";
    return Array.isArray(d) ? d.join(", ") : String(d);
  };

  function edit(s: Setting, v: string | boolean) {
    const original = s.type === "bool" ? s.value === true : s.secret ? "" : asText(s);
    if (v === original) {
      const { [s.key]: _, ...rest } = draft;
      draft = rest;
    } else draft = { ...draft, [s.key]: v };
  }

  function cancelReplace(s: Setting) {
    const { [s.key]: _, ...rest } = draft;
    draft = rest;
    replacing = { ...replacing, [s.key]: false };
  }

  function discard() {
    draft = {};
    replacing = {};
    fieldErrors = {};
  }

  const patchBody = (): Record<string, unknown> => {
    const body: Record<string, unknown> = {};
    for (const s of settings) {
      if (!(s.key in draft) || readOnly(s)) continue;
      const v = draft[s.key];
      if (s.type === "bool") body[s.key] = v;
      else if (s.secret) {
        if (v !== "") body[s.key] = v;
      } else {
        const t = String(v).trim();
        if (s.type === "list") body[s.key] = t.split(/[,\n]/).map((x) => x.trim()).filter(Boolean);
        else if (t === "") body[s.key] = null;
        else body[s.key] = s.type === "int" ? Number(t) : t;
      }
    }
    return body;
  };

  const changes = $derived.by(() => {
    if (!confirming) return [];
    const body = patchBody();
    const out: string[] = [];
    for (const s of settings) {
      if (!(s.key in body)) continue;
      const v = body[s.key];
      if (s.secret) out.push(`Replaces ${s.label}`);
      else if (v === null) out.push(`Resets ${s.label} to its default${defaultText(s) ? ` (${defaultText(s)})` : ""}`);
      else if (s.type === "bool") out.push(`${v ? "Turns on" : "Turns off"} ${s.label}`);
      else if (s.type === "list") out.push(`Sets ${s.label} to ${(v as string[]).join(", ") || "an empty list"}`);
      else out.push(`Sets ${s.label} to ${String(v)}`);
    }
    return [...out, ...(effects ? effects(body) : [])];
  });

  function submit(e: SubmitEvent) {
    e.preventDefault();
    if (Object.keys(patchBody()).length === 0) {
      discard();
      return;
    }
    confirming = true;
  }

  async function save() {
    fieldErrors = {};
    try {
      const result = await api.settings.patch(patchBody());
      draft = {};
      replacing = {};
      toast.success(`Saved ${label.toLowerCase()}`);
      onSaved(result);
    } catch (err) {
      const fields = err instanceof ApiError ? err.fields : undefined;
      if (fields) {
        fieldErrors = fields;
        const named = settings.filter((s) => s.key in fields).map((s) => `${s.label}: ${fields[s.key]}`);
        if (named.length) throw new Error(named.join(". "));
      }
      throw new Error(errorMessage(err));
    }
  }
</script>

{#snippet field(s: Setting)}
  {@const id = `${idPrefix}-${s.key}`}
  {@const ro = readOnly(s)}
  {@const err = fieldErrors[s.key]}
  {@const help = s.help ? `${id}-help` : undefined}
  {#snippet notes()}
    {#if s.help}<p id={help} class="max-w-prose text-sm text-muted-foreground">{s.help}</p>{/if}
    {#if err}<ErrorState inline error={err} />{/if}
  {/snippet}
  {#snippet lock()}
    {#if s.locked}
      <Badge variant="secondary" class="font-mono" title="The environment variable {s.env} sets this value. Remove it to change the setting here.">
        Set by {s.env}
      </Badge>
    {/if}
  {/snippet}

  {#if s.type === "bool"}
    <div class="flex items-start gap-3">
      <Switch {id} class="mt-0.5" checked={current(s) === true} disabled={ro} onCheckedChange={(v) => edit(s, v)} aria-describedby={help} />
      <div class="flex min-w-0 flex-col gap-1">
        <div class="flex flex-wrap items-center gap-2">
          <Label for={id}>{s.label}</Label>
          {@render lock()}
        </div>
        {@render notes()}
      </div>
    </div>
  {:else}
    <div class="flex flex-col gap-2">
      <div class="flex flex-wrap items-center gap-2">
        <Label for={id}>{s.label}</Label>
        {@render lock()}
      </div>

      {#if s.secret}
        {#if replacing[s.key] && !ro}
          <div class="flex flex-wrap items-center gap-2">
            <Input
              {id}
              type="password"
              class="max-w-sm"
              autocomplete="new-password"
              placeholder="New value"
              value={String(current(s))}
              oninput={(e) => edit(s, e.currentTarget.value)}
              aria-invalid={err ? true : undefined}
              aria-describedby={help}
            />
            <Button type="button" variant="ghost" size="sm" onclick={() => cancelReplace(s)}>Keep the current value</Button>
          </div>
        {:else}
          <div class="flex flex-wrap items-center gap-3">
            <span class="text-sm {isSet(s) ? '' : 'text-muted-foreground'}">{isSet(s) ? "Set. The value is not shown." : "Not set"}</span>
            {#if !ro}
              <Button type="button" variant="outline" size="sm" onclick={() => (replacing = { ...replacing, [s.key]: true })}>
                {isSet(s) ? "Replace" : "Set a value"}
              </Button>
            {/if}
          </div>
        {/if}
      {:else if s.options?.length}
        <Select.Root type="single" disabled={ro} value={String(current(s))} onValueChange={(v) => edit(s, v)}>
          <Select.Trigger {id} class="w-44 font-mono" aria-invalid={err ? true : undefined} aria-describedby={help}>
            <span class="truncate">{String(current(s)) || "Choose"}</span>
          </Select.Trigger>
          <Select.Content>
            {#each s.options as o (o)}
              <Select.Item value={o} label={o} class="font-mono">{o}</Select.Item>
            {/each}
          </Select.Content>
        </Select.Root>
      {:else if s.type === "list"}
        <Textarea
          {id}
          class="max-w-form font-mono text-[0.8125rem]"
          rows={Math.min(8, Math.max(2, String(current(s)).split("\n").length + 1))}
          disabled={ro}
          spellcheck={false}
          placeholder="One per line"
          value={String(current(s))}
          oninput={(e) => edit(s, e.currentTarget.value)}
          aria-invalid={err ? true : undefined}
          aria-describedby={help}
        />
      {:else}
        <Input
          {id}
          type={s.type === "int" ? "number" : "text"}
          class={s.type === "int" || s.type === "duration" ? "w-44 font-mono" : "max-w-form font-mono"}
          disabled={ro}
          spellcheck={false}
          autocomplete="off"
          placeholder={defaultText(s) ? `Default: ${defaultText(s)}` : undefined}
          value={String(current(s))}
          oninput={(e) => edit(s, e.currentTarget.value)}
          aria-invalid={err ? true : undefined}
          aria-describedby={help}
        />
      {/if}
      {@render notes()}
    </div>
  {/if}
{/snippet}

<form class="flex flex-col gap-8" onsubmit={submit} autocomplete="off">
  {#each groups as g (g.title)}
    <div class="flex flex-col gap-4">
      {#if g.title}
        <div>
          <h3 class="text-sm font-semibold">{g.title}</h3>
          {#if g.description}<p class="mt-1 max-w-prose text-sm text-muted-foreground">{g.description}</p>{/if}
        </div>
      {/if}
      {#each g.fields as s (s.key)}
        {@render field(s)}
      {/each}
    </div>
  {/each}

  <div class="flex flex-wrap items-center gap-2">
    <Button type="submit" disabled={!dirty}>Save changes</Button>
    {#if dirty}
      <Button type="button" variant="ghost" onclick={discard}>Discard edits</Button>
    {/if}
    {#if extra}{@render extra(patchBody)}{/if}
  </div>
</form>

<ConfirmDialog
  bind:open={confirming}
  title="Save {label.toLowerCase()}"
  confirmLabel="Save changes"
  {changes}
  onConfirm={save}
/>
