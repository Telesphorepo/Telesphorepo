<!--
  Settings from GET /api/settings, rendered by setting type in the one form grid and saved with
  PATCH /api/settings from the sticky SaveBar ("3 unsaved changes · Discard · Save"). A field that the
  environment forces (`locked`) is shown disabled with the variable's name. A secret is a
  SecretInput (Set / Not set, Replace, Clear); a list is a ListInput; a bool is a SwitchRow. An
  empty text field resets the setting to its default.

    <SettingsGroupForm settings={group.settings} label="Storage settings" onSaved={(r) => ...} />

  `sections` splits the fields into FormSections, one card each (a setting in no section goes to
  the first one), so the form is the whole body of a page: put it in a FormPage and nothing
  between sections. Without `sections` the form is one card.
  With `steps`, the sections are numbered Steps of a setup. `fields` gives a setting a plain label,
  one line of help, `aka` (its technical name, after `akaLabel`) or a placeholder;
  without it the registry's label and help show. `errors` marks fields from outside the form (a
  connection test). `effects` lists the consequences of a pending PATCH body beyond the settings
  themselves (a storage move); when it returns lines, Save first asks in a confirm dialog that
  lists every change. `before` renders at the top of the form and `extra` in the footer of the last section (or of section `extraAt`);
  both get a function that returns the PATCH body of the unsaved edits (Storage and Single sign-on
  use it for their connection tests).
-->
<script lang="ts" module>
  import type { ControlWidth } from "./FormRow.svelte";

  export type FieldLook = {
    label?: string;
    help?: string;
    aka?: string;
    placeholder?: string;
    /** @deprecated Every text control has the full control width; leave it out. */
    width?: ControlWidth;
  };
</script>

<script lang="ts">
  import type { Snippet } from "svelte";
  import { toast } from "svelte-sonner";
  import { api, ApiError, errorMessage, type Setting, type Settings, type SettingsMoveStarted } from "$lib/api";
  import { Input } from "$lib/components/ui/input";
  import * as Select from "$lib/components/ui/select";
  import SaveBar from "./SaveBar.svelte";
  import UnitInput from "./UnitInput.svelte";
  import ConfirmDialog from "./ConfirmDialog.svelte";
  import FormSection from "./FormSection.svelte";
  import FormGrid from "./FormGrid.svelte";
  import FormRow from "./FormRow.svelte";
  import SwitchRow from "./SwitchRow.svelte";
  import ListInput from "./ListInput.svelte";
  import SecretInput from "./SecretInput.svelte";
  import Step from "./Step.svelte";
  import StatusBadge from "./StatusBadge.svelte";
  import { DURATION_UNITS, SIZE_UNITS, splitDuration, splitSize, type Parts, type Unit } from "$lib/units";

  type Section = { title: string; description?: string; keys: string[]; /** A finished step shows a check. */ done?: boolean };
  type Patch = () => Record<string, unknown>;

  let {
    settings,
    label,
    visible,
    sections,
    steps = false,
    fields = {},
    akaLabel,
    errors = {},
    effects,
    onSaved,
    before,
    extra,
    extraAt,
  }: {
    settings: Setting[];
    /** Names the settings in the dialog and the toast ("Saved storage settings"). */
    label: string;
    /** Hides a setting for the current edits (Storage hides the S3 fields for a folder). */
    visible?: (s: Setting, draft: Record<string, string | boolean | null>) => boolean;
    sections?: Section[];
    /** Render the sections as numbered Steps. */
    steps?: boolean;
    fields?: Record<string, FieldLook>;
    akaLabel?: string;
    /** Field errors from outside the form, e.g. a connection test, by key. */
    errors?: Record<string, string>;
    /** Extra effects of a save, one line each, for the confirm dialog. */
    effects?: (patch: Record<string, unknown>) => string[];
    /** Called with the server's answer: the new settings, or {move} when a storage move started. */
    onSaved: (result: Settings | SettingsMoveStarted) => void;
    before?: Snippet<[Patch]>;
    extra?: Snippet<[Patch]>;
    /** The section whose footer holds xtra; the last one by default. */
    extraAt?: number;
  } = $props();

  const extraIn = (i: number) => !!extra && i === Math.min(extraAt ?? groups.length - 1, groups.length - 1);

  // Edits by key: text for every type except bool; null clears a secret.
  let draft = $state<Record<string, string | boolean | null>>({});
  let fieldErrors = $state<Record<string, string>>({});
  let confirming = $state(false);

  const idPrefix = $props.id();
  const shown = $derived(settings.filter((s) => (visible ? visible(s, draft) : true)));
  const count = $derived(Object.keys(draft).length);

  // The fields of each section; one untitled section without sections.
  const groups = $derived.by(() => {
    if (!sections?.length) return [{ title: "", description: undefined as string | undefined, done: false, fields: shown }];
    const known = new Set(sections.flatMap((s) => s.keys));
    return sections
      .map((sec, i) => ({
        title: sec.title,
        description: sec.description,
        done: sec.done ?? false,
        fields: shown
          .filter((s) => sec.keys.includes(s.key) || (i === 0 && !known.has(s.key)))
          .sort((a, b) => sec.keys.indexOf(a.key) - sec.keys.indexOf(b.key)),
      }))
      .filter((g) => g.fields.length > 0);
  });

  const labelOf = (s: Setting) => fields[s.key]?.label ?? s.label;
  const asText = (s: Setting): string => {
    if (Array.isArray(s.value)) return s.value.join("\n");
    return s.value === null || s.value === undefined ? "" : String(s.value);
  };
  const current = (s: Setting): string | boolean => {
    if (s.key in draft && draft[s.key] !== null) return draft[s.key] as string | boolean;
    return s.type === "bool" ? s.value === true : asText(s);
  };
  const listOf = (s: Setting) => String(current(s)).split("\n").filter(Boolean);
  // Plain words for the option values of select settings; a value without an entry shows as is.
  const optionWords: Record<string, Record<string, string>> = {
    storage: { fs: "A folder on this server", s3: "An S3 bucket", r2: "A Cloudflare R2 bucket" },
    "log.level": { debug: "Debug", info: "Info", warn: "Warnings", error: "Errors" },
  };
  const optionText = (s: Setting, o: string) => optionWords[s.key]?.[o] ?? o;
  const isSet = (s: Setting) => (s.value as { set?: boolean } | null)?.set === true;
  // Sizes and durations edit as a number and a unit; the draft holds the text to send ("512MiB").
  const isAmount = (s: Setting) => s.type === "size" || s.type === "duration";
  const unitsOf = (s: Setting): Unit[] => (s.type === "size" ? SIZE_UNITS : DURATION_UNITS);
  const initialParts = (s: Setting): Parts =>
    s.type === "size" ? splitSize(Number(s.value)) : splitDuration(String(s.value ?? ""));
  let parts = $state<Record<string, Parts>>({});
  const partsOf = (s: Setting): Parts => parts[s.key] ?? initialParts(s);


  function drop(key: string) {
    const { [key]: _, ...rest } = draft;
    draft = rest;
  }

  function editParts(s: Setting, next: Parts) {
    const first = initialParts(s);
    if (next.num === first.num && next.unit === first.unit) {
      const { [s.key]: _, ...rest } = parts;
      parts = rest;
      drop(s.key);
      return;
    }
    parts = { ...parts, [s.key]: next };
    const num = next.num.trim();
    draft = { ...draft, [s.key]: num === "" ? "" : `${num}${next.unit}` };
  }

  const defaultText = (s: Setting) => {
    if (s.default_display) return s.default_display;
    const d = s.default;
    if (d === null || d === undefined || d === "") return "";
    return Array.isArray(d) ? d.join(", ") : String(d);
  };

  function edit(s: Setting, v: string | boolean) {
    const original = s.type === "bool" ? s.value === true : asText(s);
    if (v === original) drop(s.key);
    else draft = { ...draft, [s.key]: v };
  }

  function editSecret(s: Setting, v: string | null | undefined) {
    if (v === undefined) drop(s.key);
    else draft = { ...draft, [s.key]: v };
  }

  function discard() {
    draft = {};
    parts = {};
    fieldErrors = {};
  }

  const patchBody = (): Record<string, unknown> => {
    const body: Record<string, unknown> = {};
    for (const s of settings) {
      if (!(s.key in draft) || s.locked) continue;
      const v = draft[s.key];
      if (s.type === "bool") body[s.key] = v;
      else if (s.secret) {
        if (v === null) body[s.key] = null;
        else if (v !== "") body[s.key] = v;
      } else {
        const t = String(v).trim();
        if (s.type === "list") body[s.key] = t.split(/[,\n]/).map((x) => x.trim()).filter(Boolean);
        else if (t === "") body[s.key] = null;
        else body[s.key] = t;
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
      const name = labelOf(s);
      if (s.secret) out.push(v === null ? `Clears ${name}` : `Replaces ${name}`);
      else if (v === null) out.push(`Resets ${name} to its default${defaultText(s) ? ` (${defaultText(s)})` : ""}`);
      else if (s.type === "bool") out.push(`${v ? "Turns on" : "Turns off"} ${name}`);
      else if (s.type === "list") out.push(`Sets ${name} to ${(v as string[]).join(", ") || "an empty list"}`);
      else out.push(`Sets ${name} to ${optionText(s, String(v))}`);
    }
    return [...out, ...(effects ? effects(body) : [])];
  });

  // Save from the SaveBar: at once, or after the confirm dialog when the save has further effects.
  async function saveFromBar() {
    const body = patchBody();
    if (Object.keys(body).length === 0) {
      discard();
      return;
    }
    if (effects && effects(body).length > 0) {
      confirming = true;
      return;
    }
    await save();
  }

  // Enter in a field saves, as Save in the SaveBar does; an error shows as a toast.
  function submit(e: SubmitEvent) {
    e.preventDefault();
    saveFromBar().catch((err) => toast.error(errorMessage(err)));
  }

  async function save() {
    fieldErrors = {};
    try {
      const result = await api.settings.patch(patchBody());
      draft = {};
      parts = {};
      toast.success(`Saved ${label.toLowerCase()}`);
      onSaved(result);
    } catch (err) {
      const fields = err instanceof ApiError ? err.fields : undefined;
      if (fields) {
        fieldErrors = fields;
        const named = settings.filter((s) => s.key in fields).map((s) => `${labelOf(s)}: ${fields[s.key]}`);
        if (named.length) throw new Error(named.join(". "));
      }
      throw new Error(errorMessage(err));
    }
  }
</script>

{#snippet field(s: Setting)}
  {@const id = `${idPrefix}-${s.key}`}
  {@const look = fields[s.key] ?? {}}
  {@const ro = s.locked}
  {@const err = fieldErrors[s.key] ?? errors[s.key]}
  {@const help = look.help ?? s.help}
  {@const described = help ? `${id}-help` : undefined}
  {#snippet lock()}
    {#if s.locked}
      <StatusBadge title="The environment variable {s.env} sets this value. Remove it to change the setting here.">Set by {s.env}</StatusBadge>
    {/if}
  {/snippet}

  {#if s.type === "bool"}
    <SwitchRow {id} label={labelOf(s)} {help} checked={current(s) === true} disabled={ro} onCheckedChange={(v) => edit(s, v)} badge={lock} />
  {:else}
    <FormRow label={labelOf(s)} for={id} {help} aka={look.aka} {akaLabel} error={err} badge={lock}>
      {#if s.secret}
        <SecretInput
          {id}
          set={isSet(s)}
          change={s.key in draft ? (draft[s.key] as string | null) : undefined}
          onchange={(v) => editSecret(s, v)}
          disabled={ro}
          invalid={!!err}
          describedby={described}
        />
      {:else if s.options?.length}
        <Select.Root type="single" disabled={ro} value={String(current(s))} onValueChange={(v) => edit(s, v)}>
          <Select.Trigger {id} class="w-full" aria-invalid={err ? true : undefined} aria-describedby={described}>
            <span class="truncate">{String(current(s)) ? optionText(s, String(current(s))) : "Choose"}</span>
          </Select.Trigger>
          <Select.Content>
            {#each s.options as o (o)}
              <Select.Item value={o} label={optionText(s, o)}>{optionText(s, o)}</Select.Item>
            {/each}
          </Select.Content>
        </Select.Root>
      {:else if s.type === "list"}
        <ListInput
          {id}
          values={listOf(s)}
          onchange={(v) => edit(s, v.join("\n"))}
          placeholder={look.placeholder ?? "Type a value, then Enter"}
          disabled={ro}
          invalid={!!err}
          describedby={described}
          label={labelOf(s)}
        />
      {:else if isAmount(s)}
        {@const p = partsOf(s)}
        <UnitInput
          {id}
          num={p.num}
          unit={p.unit}
          units={unitsOf(s)}
          label={labelOf(s)}
          disabled={ro}
          invalid={!!err}
          describedby={described}
          placeholder={defaultText(s) ? `Default: ${defaultText(s)}` : undefined}
          onchange={(v) => editParts(s, v)}
        />
      {:else}
        <Input
          {id}
          type="text"
          disabled={ro}
          spellcheck={false}
          autocomplete="off"
          placeholder={look.placeholder ?? (defaultText(s) ? `Default: ${defaultText(s)}` : undefined)}
          value={String(current(s))}
          oninput={(e) => edit(s, e.currentTarget.value)}
          aria-invalid={err ? true : undefined}
          aria-describedby={described}
        />
      {/if}
    </FormRow>
  {/if}
{/snippet}

{#snippet extraRow()}
  {#if extra}{@render extra(patchBody)}{/if}
{/snippet}

{#snippet grid(list: Setting[])}
  <FormGrid>
    {#each list as s (s.key)}
      {@render field(s)}
    {/each}
  </FormGrid>
{/snippet}

<form onsubmit={submit} autocomplete="off">
  {#if before}{@render before(patchBody)}{/if}
  {#if steps}
    {#each groups as g, i (g.title)}
      <Step n={i + 1} title={g.title} description={g.description} done={g.done} footer={extraIn(i) ? extraRow : undefined}>
        {@render grid(g.fields)}
      </Step>
    {/each}
  {:else}
    {#each groups as g, i (g.title)}
      <FormSection title={g.title || undefined} description={g.description} footer={extraIn(i) ? extraRow : undefined}>
        {@render grid(g.fields)}
      </FormSection>
    {/each}
  {/if}
</form>

<SaveBar {count} onDiscard={discard} onSave={saveFromBar} />

<ConfirmDialog bind:open={confirming} title="Save {label.toLowerCase()}?" confirmLabel="Save changes" {changes} onConfirm={save} />
