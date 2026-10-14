<!-- The fallback repo-config editor: the config as a JSON object. -->
<script lang="ts">
  import { Textarea } from "$lib/components/ui/textarea";
  import { Label } from "$lib/components/ui/label";
  import ErrorState from "$lib/components/ErrorState.svelte";
  import { untrack } from "svelte";
  import type { EditorProps } from "./index";

  let { value, onChange }: EditorProps = $props();

  const id = $props.id();
  // The editor owns its text after the first render; wrap it in {#key} to start over.
  let text = $state(untrack(() => JSON.stringify(value, null, 2)));
  let error = $state("");

  function edit() {
    try {
      const v = JSON.parse(text);
      if (v === null || typeof v !== "object" || Array.isArray(v)) throw new Error("The config must be a JSON object.");
      error = "";
      onChange(v);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
      onChange(null);
    }
  }
</script>

<div class="flex flex-col gap-2">
  <Label for="{id}-json">Config (JSON)</Label>
  <Textarea
    id="{id}-json"
    class="min-h-28 font-mono text-[0.8125rem]"
    rows={Math.min(14, Math.max(4, text.split("\n").length))}
    spellcheck={false}
    bind:value={text}
    oninput={edit}
    aria-invalid={error ? true : undefined}
  />
  {#if error}<ErrorState inline {error} />{/if}
</div>
