<!-- Debian repo config: the architectures the indexes list. -->
<script lang="ts">
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import { untrack } from "svelte";
  import type { EditorProps } from "./index";

  let { value, onChange }: EditorProps = $props();

  const id = $props.id();
  let text = $state(untrack(() => (Array.isArray(value.architectures) ? (value.architectures as string[]).join(", ") : "")));

  function edit() {
    const architectures = text.split(",").map((s) => s.trim()).filter(Boolean);
    onChange({ ...value, architectures });
  }
</script>

<div class="flex flex-col gap-2">
  <Label for="{id}-arch">Architectures</Label>
  <Input id="{id}-arch" class="max-w-sm font-mono" placeholder="amd64, arm64" bind:value={text} oninput={edit} />
  <p class="text-sm text-muted-foreground">Separate them with commas.</p>
</div>
