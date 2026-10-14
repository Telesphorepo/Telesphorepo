<!--
  Debian upload fields: the distribution and component the packages go into. The open folder
  (dists/<distribution>/, pool/<component>/) or the repo config gives the first values.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import type { Repo } from "$lib/api";
  import type { UploadPlan } from "./types";

  let {
    repo,
    folder,
    files,
    plan = $bindable(),
    disabled = false,
  }: {
    repo: Repo;
    folder: string;
    files: File[];
    plan?: UploadPlan;
    disabled?: boolean;
  } = $props();

  // Distribution, component and other names (repos.NameRE on the server).
  const nameRE = /^[a-z0-9][a-z0-9._-]{0,63}$/;

  const start = untrack(() => {
    const seg = folder.split("/");
    const configured = typeof repo.config?.distribution === "string" ? (repo.config.distribution as string) : "";
    return {
      dist: seg[0] === "dists" && seg[1] ? seg[1] : configured,
      comp: seg[0] === "pool" && seg[1] ? seg[1] : "main",
    };
  });
  let distribution = $state(start.dist);
  let component = $state(start.comp);

  $effect(() => {
    const rows = files.map((file) => ({
      file,
      error: file.name.endsWith(".deb") ? undefined : "Not a .deb file",
    }));
    const d = distribution.trim();
    const c = component.trim();
    let problem = "";
    if (!files.length) problem = "Add one or more .deb files.";
    else if (rows.some((r) => r.error)) problem = "Take out the files that are not .deb packages.";
    else if (!nameRE.test(d)) problem = "Enter a distribution, e.g. trixie.";
    else if (!nameRE.test(c)) problem = "Enter a component, e.g. main.";
    plan = { rows, fields: { distribution: d, component: c }, problem };
  });
</script>

<div class="flex flex-col gap-4">
  <div class="flex flex-wrap gap-4">
    <div class="flex flex-col gap-2">
      <Label for="deb-distribution">Distribution</Label>
      <Input id="deb-distribution" class="w-44 font-mono" placeholder="trixie" autocomplete="off" spellcheck={false} {disabled} bind:value={distribution} />
    </div>
    <div class="flex flex-col gap-2">
      <Label for="deb-component">Component</Label>
      <Input id="deb-component" class="w-44 font-mono" placeholder="main" autocomplete="off" spellcheck={false} {disabled} bind:value={component} />
    </div>
  </div>
  <p class="text-sm text-muted-foreground">
    The server reads each package's name, version and architecture, stores it under pool/, and rebuilds the
    apt indexes of the distribution.
  </p>
</div>
