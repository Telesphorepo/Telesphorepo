<!--
  A list of short values (group names, email addresses) as chips in one input-like box. Enter, a
  comma or leaving the field adds the typed value; pasting text adds every value in it (split on
  new lines, tabs, commas and semicolons); Backspace in the empty field removes the last chip.
  Duplicates are dropped. `normalize` cleans each value (e.g. lower case for email addresses).

    <ListInput id="admin-groups" values={groups} onchange={(v) => (groups = v)} placeholder="Add a group" />
-->
<script lang="ts">
  import XIcon from "@lucide/svelte/icons/x";
  import { cn } from "$lib/utils";

  let {
    id,
    values,
    onchange,
    placeholder = "Add a value",
    disabled = false,
    normalize = (s: string) => s,
    invalid = false,
    describedby,
    label,
  }: {
    id: string;
    values: string[];
    onchange: (values: string[]) => void;
    placeholder?: string;
    disabled?: boolean;
    normalize?: (s: string) => string;
    invalid?: boolean;
    describedby?: string;
    /** Names a chip's remove button: "Remove {label} repo-admins". */
    label?: string;
  } = $props();

  let text = $state("");
  let input = $state<HTMLInputElement | null>(null);

  function add(raw: string) {
    const items = raw
      .split(/[\n\r\t,;]+/)
      .map((s) => normalize(s.trim()))
      .filter(Boolean);
    const next = [...values];
    for (const it of items) if (!next.includes(it)) next.push(it);
    if (next.length !== values.length) onchange(next);
  }

  function remove(i: number) {
    onchange(values.filter((_, j) => j !== i));
    input?.focus();
  }

  function keydown(e: KeyboardEvent) {
    if ((e.key === "Enter" || e.key === ",") && text.trim()) {
      e.preventDefault();
      add(text);
      text = "";
    } else if (e.key === "Enter") {
      // An empty field never submits the form by accident.
      e.preventDefault();
    } else if (e.key === "Backspace" && text === "" && values.length) {
      remove(values.length - 1);
    }
  }

  function paste(e: ClipboardEvent) {
    const t = e.clipboardData?.getData("text") ?? "";
    if (!/[\n\r\t,;]/.test(t.trim())) return;
    e.preventDefault();
    add(text + t);
    text = "";
  }

  function blur() {
    if (text.trim()) {
      add(text);
      text = "";
    }
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div
  class={cn(
    "flex min-h-9 w-full flex-wrap items-center gap-1.5 rounded-lg border border-input bg-input/30 px-1.5 py-1 transition-colors",
    "focus-within:border-ring focus-within:ring-[3px] focus-within:ring-ring/50",
    invalid && "border-destructive ring-[3px] ring-destructive/20",
    disabled && "pointer-events-none opacity-50",
  )}
  onclick={() => input?.focus()}
>
  {#each values as v, i (v)}
    <span class="inline-flex h-6 max-w-full items-center gap-1 rounded-lg bg-muted pr-0.5 pl-2 text-body">
      <span class="truncate">{v}</span>
      <button
        type="button"
        class="flex size-5 shrink-0 items-center justify-center rounded-lg text-muted-foreground outline-none hover:bg-hover hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50"
        aria-label="Remove {label ? `${label} ` : ''}{v}"
        onclick={(e) => {
          e.stopPropagation();
          remove(i);
        }}
        {disabled}
      >
        <XIcon class="size-3.5" />
      </button>
    </span>
  {/each}
  <input
    bind:this={input}
    {id}
    bind:value={text}
    {disabled}
    class="h-7 min-w-32 flex-1 bg-transparent px-1.5 text-base outline-none placeholder:text-muted-foreground md:text-sm"
    placeholder={values.length ? "" : placeholder}
    autocomplete="off"
    spellcheck={false}
    aria-describedby={describedby}
    aria-invalid={invalid || undefined}
    onkeydown={keydown}
    onpaste={paste}
    onblur={blur}
  />
</div>
