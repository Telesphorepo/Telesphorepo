<!--
  Paste addresses and ranges in any format. The server reads the text (POST /api/ip-ranges/parse)
  after a short pause in typing, or at once on Check. The preview counts the ranges, the invalid
  lines and the removed duplicates, lists the invalid lines with their numbers, and shows the
  ranges. Add gives the ranges to the parent; Replace (only with onReplace) gives them in place of
  the current ones. Both empty the box. The parent removes ranges it already holds.

    <RangePaste id="ranges" onAdd={(r) => (ranges = [...ranges, ...r.filter((x) => !ranges.includes(x))])} onReplace={(r) => (ranges = r)} />
-->
<script lang="ts">
  import { api, errorMessage, type ParsedRanges } from "$lib/api";
  import { formatCount } from "$lib/format";
  import { Button } from "$lib/components/ui/button";
  import { Textarea } from "$lib/components/ui/textarea";
  import { Spinner } from "$lib/components/ui/spinner";
  import FormField from "./FormField.svelte";
  import ErrorState from "./ErrorState.svelte";

  let {
    id,
    label = "Paste addresses",
    disabled = false,
    onAdd = () => {},
    onReplace,
    buttons = true,
    found = $bindable([]),
  }: {
    /** The id of the text box, unique on the page. */
    id: string;
    label?: string;
    disabled?: boolean;
    /** Gets the parsed ranges (addresses as /32 or /128, ranges masked, no duplicates). */
    onAdd?: (ranges: string[]) => void;
    /** false hides Check, Add and Replace: a dialog reads `found` and adds from its own footer. */
    buttons?: boolean;
    /** The ranges of the checked text; empty while the text changes or holds none. */
    found?: string[];
    /** With it, a Replace button gives the ranges in place of the current ones. */
    onReplace?: (ranges: string[]) => void;
  } = $props();

  const pause = 400; // ms after the last key before the text is checked
  const shownInvalid = 20;
  const shownRanges = 200;

  let text = $state("");
  let result = $state<ParsedRanges | null>(null);
  let error = $state("");
  let checkedText = $state(""); // the text that result or error belongs to
  let checking = $state(false);
  let ctrl: AbortController | null = null;

  async function check() {
    const t = text;
    if (!t.trim() || (t === checkedText && !checking)) return;
    ctrl?.abort();
    const c = new AbortController();
    ctrl = c;
    checking = true;
    try {
      const r = await api.ipRanges.parse(t, c.signal);
      if (ctrl !== c) return;
      result = r;
      error = "";
      checkedText = t;
    } catch (e) {
      if (ctrl !== c) return;
      result = null;
      error = errorMessage(e);
      checkedText = t;
    } finally {
      if (ctrl === c) {
        ctrl = null;
        checking = false;
      }
    }
  }

  // Check the text after a pause in typing.
  $effect(() => {
    if (!text.trim()) return;
    const h = setTimeout(check, pause);
    return () => clearTimeout(h);
  });

  const empty = $derived(!text.trim());
  const current = $derived(!empty && checkedText === text);
  const shown = $derived(empty ? null : result);
  const count = $derived(current && result ? result.ranges.length : 0);
  $effect(() => {
    found = current && result && !checking ? result.ranges : [];
  });
  const plural = (n: number, one: string, many: string) => `${formatCount(n)} ${n === 1 ? one : many}`;

  function clear() {
    ctrl?.abort();
    ctrl = null;
    checking = false;
    text = "";
    result = null;
    error = "";
    checkedText = "";
  }

  function add() {
    if (!result || !current) return;
    onAdd(result.ranges);
    clear();
  }

  function replace() {
    if (!result || !current || !onReplace) return;
    onReplace(result.ranges);
    clear();
  }
</script>

<div class="flex flex-col gap-3">
  <FormField {label} for={id} hint="Paste addresses or ranges in any format: one per line, comma or space separated, or copied from a table.">
    <Textarea
      {id}
      rows={Math.min(12, Math.max(4, text.split("\n").length + 1))}
      spellcheck={false}
      placeholder={"203.0.113.7\n198.51.100.0/24"}
      {disabled}
      aria-invalid={current && (error || (result && result.invalid.length)) ? true : undefined}
      bind:value={text}
    />
  </FormField>

  {#if !empty && error && current}
    <ErrorState inline {error} />
  {:else if shown}
    <div class="flex flex-col gap-3" aria-live="polite">
      <p class="text-sm tabular-nums" class:text-muted-foreground={!current}>
        {plural(shown.ranges.length, "range", "ranges")} found · {formatCount(shown.invalid.length)} invalid · {plural(shown.duplicates, "duplicate", "duplicates")} removed
      </p>
      {#if shown.invalid.length}
        <ul class="flex flex-col gap-1 text-sm text-destructive">
          {#each shown.invalid.slice(0, shownInvalid) as l (l.line)}
            <li class="min-w-0 break-words"><span class="tabular-nums">Line {l.line}</span>: {l.text} ({l.reason})</li>
          {/each}
          {#if shown.invalid.length > shownInvalid}
            <li>And {plural(shown.invalid.length - shownInvalid, "more line", "more lines")}.</li>
          {/if}
        </ul>
      {/if}
      {#if shown.ranges.length}
        <ul class="max-h-48 overflow-y-auto rounded-lg bg-panel px-4 py-3 text-sm tabular-nums" aria-label="Ranges found">
          {#each shown.ranges.slice(0, shownRanges) as r (r)}<li>{r}</li>{/each}
          {#if shown.ranges.length > shownRanges}
            <li class="text-muted-foreground">And {plural(shown.ranges.length - shownRanges, "more range", "more ranges")}.</li>
          {/if}
        </ul>
      {/if}
    </div>
  {/if}

  {#if buttons}
  <div class="flex flex-wrap gap-2">
    <Button variant="outline" size="sm" disabled={disabled || empty || current || checking} onclick={check}>
      {#if checking}<Spinner />{/if}
      Check
    </Button>
    <Button size="sm" disabled={disabled || count === 0 || checking} onclick={add}>
      {count ? `Add ${plural(count, "range", "ranges")}` : "Add"}
    </Button>
    {#if onReplace}
      <Button variant="outline" size="sm" disabled={disabled || count === 0 || checking} onclick={replace}>
        {count ? `Replace with ${plural(count, "range", "ranges")}` : "Replace"}
      </Button>
    {/if}
  </div>
  {/if}
</div>
