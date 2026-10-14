<!--
  Raw upload fields: the folder the files go into. The open folder is the first value; empty is
  the repo root.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import type { UploadPlan } from "./types";

  let {
    folder,
    files,
    plan = $bindable(),
    disabled = false,
  }: {
    folder: string;
    files: File[];
    plan?: UploadPlan;
    disabled?: boolean;
  } = $props();

  let target = $state(untrack(() => folder.replace(/\/+$/, "")));

  const badFolder = (f: string) =>
    f.includes("\\") || f.split("/").some((s) => s === "" || s === "." || s === "..");

  $effect(() => {
    const f = target.trim().replace(/^\/+|\/+$/g, "");
    const rows = files.map((file) => ({ file, target: f ? `${f}/${file.name}` : file.name }));
    let problem = "";
    if (!files.length) problem = "Add one or more files.";
    else if (f && badFolder(f)) problem = "Enter a folder such as releases/1.0, without empty, . or .. parts.";
    plan = { rows, fields: f ? { folder: f } : {}, problem };
  });
</script>

<div class="flex flex-col gap-2">
  <Label for="raw-folder">Folder</Label>
  <Input
    id="raw-folder"
    class="max-w-form font-mono"
    placeholder="Empty for the repository root"
    autocomplete="off"
    spellcheck={false}
    {disabled}
    bind:value={target}
  />
  <p class="text-sm text-muted-foreground">
    A file with the same name is replaced when the repository allows it.
  </p>
</div>
