<!--
  Which key signs a Debian repository: a key that exists, or a new key for this repository. The
  name of a new key follows the repository name until the user types one. The parent creates the
  new key (POST /api/signing-keys) before it saves the repository.

    <SigningKeyChoice keys={keys.data ?? []} repoName={name} bind:choice />
-->
<script lang="ts" module>
  export type KeyChoice = { mode: "existing" | "new"; name: string };
</script>

<script lang="ts">
  import type { SigningKey } from "$lib/api";
  import { groupFingerprint } from "$lib/format";
  import * as RadioGroup from "$lib/components/ui/radio-group";
  import * as Select from "$lib/components/ui/select";
  import { Input } from "$lib/components/ui/input";
  import OptionRow from "./OptionRow.svelte";
  import FormGrid from "./FormGrid.svelte";
  import FormRow from "./FormRow.svelte";

  let {
    keys,
    repoName,
    exclude,
    choice = $bindable({ mode: "existing", name: "" }),
    canCreate = true,
  }: {
    keys: SigningKey[];
    repoName: string;
    /** A key not to offer (the one the repository uses now). */
    exclude?: string;
    choice?: KeyChoice;
    /** False when the person does not hold Signing keys, which a new key needs. */
    canCreate?: boolean;
  } = $props();

  const id = $props.id();
  const offered = $derived(keys.filter((k) => k.name !== exclude && !k.error));
  const taken = $derived(new Set(keys.map((k) => k.name)));

  let mode = $state<string>("existing");
  let picked = $state("");
  let typed = $state<string | null>(null);

  // Start with "default" when it is offered, else the first key; with no key, a new one.
  $effect(() => {
    if (picked && offered.some((k) => k.name === picked)) return;
    picked = offered.find((k) => k.name === "default")?.name ?? offered[0]?.name ?? "";
    if (!picked && canCreate) mode = "new";
  });

  const newName = $derived(typed ?? repoName);
  const nameTaken = $derived(mode === "new" && taken.has(newName.trim()));

  $effect(() => {
    choice = { mode: mode === "new" ? "new" : "existing", name: mode === "existing" ? picked : nameTaken ? "" : newName };
  });

  const pickedKey = $derived(offered.find((k) => k.name === picked));
</script>

<RadioGroup.Root class="gap-1" aria-label="Signing key" bind:value={mode}>
  {#if offered.length > 0}
    <OptionRow id="{id}-existing" label="Use a key that exists" description="Repositories that share a key need only one key on each machine.">
      {#snippet control()}<RadioGroup.Item id="{id}-existing" value="existing" />{/snippet}
      {#if mode === "existing"}
        <div class="mt-2">
          <FormGrid>
            <FormRow label="Key" for="{id}-key" help={pickedKey ? `Its fingerprint is ${groupFingerprint(pickedKey.fingerprint)}.` : undefined}>
              <Select.Root type="single" bind:value={picked}>
                <Select.Trigger id="{id}-key" class="w-full" aria-describedby={pickedKey ? `${id}-key-help` : undefined}>
                  <span class="truncate">{picked || "Choose a key"}</span>
                </Select.Trigger>
                <Select.Content>
                  {#each offered as k (k.name)}
                    <Select.Item value={k.name} label={k.name}>{k.name}</Select.Item>
                  {/each}
                </Select.Content>
              </Select.Root>
            </FormRow>
          </FormGrid>
        </div>
      {/if}
    </OptionRow>
  {/if}
  <OptionRow
    id="{id}-new"
    label="Create a new key for this repository"
    description={canCreate ? "The server creates an Ed25519 key. Only this repository uses it." : "A new key needs the Signing keys permission."}
  >
    {#snippet control()}<RadioGroup.Item id="{id}-new" value="new" disabled={!canCreate} />{/snippet}
    {#if mode === "new"}
      <div class="mt-2">
        <FormGrid>
          <FormRow
            label="Key name"
            for="{id}-name"
            help="Repositories refer to the key by this name."
            error={nameTaken ? `A key named ${newName.trim()} exists. Choose another name, or use that key.` : undefined}
          >
            <Input
              id="{id}-name"
              autocomplete="off"
              spellcheck={false}
              aria-describedby="{id}-name-help"
              aria-invalid={nameTaken ? true : undefined}
              value={newName}
              oninput={(e) => (typed = e.currentTarget.value)}
            />
          </FormRow>
        </FormGrid>
      </div>
    {/if}
  </OptionRow>
</RadioGroup.Root>
