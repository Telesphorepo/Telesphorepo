<!--
  An empty list or a feature that is not set up, as one row in the left reading column: a plain icon,
  the name and its state ("Authenticator app · Not set up"), one sentence on why it is useful, and
  one action. Never a blank table and never a centered block: DataTable shows its `empty` snippet in
  place of the table, without column headings, so pass a CompactEmpty there. Inside a card (a
  Section) it is a plain row; outside a card it has a dashed frame (app.css). A page that stacks
  several rows in one card parts them with thin dividers, never a frame each.

    <CompactEmpty icon={KeyRoundIcon} title="Passkey" state="Not set up"
      text="Sign in with your fingerprint, your face or a security key.">
      <Button variant="outline" onclick={add}><PlusIcon />Add passkey</Button>
    </CompactEmpty>
-->
<script lang="ts">
  import type { Component, Snippet } from "svelte";
  import InboxIcon from "@lucide/svelte/icons/inbox";

  let {
    icon: Icon = InboxIcon,
    title,
    state,
    text,
    children,
  }: {
    icon?: Component<{ class?: string }>;
    /** What is empty: "Authenticator app", "Read tokens". */
    title?: string;
    /** Its state in a word or two: "Not set up", "None yet". */
    state?: string;
    /** One sentence: why it is useful, or what fills it. */
    text: string;
    /** The one action. */
    children?: Snippet;
  } = $props();
</script>

<div data-slot="compact-empty" class="flex flex-wrap items-center gap-x-4 gap-y-3 rounded-lg border border-dashed border-muted-foreground/35 px-4 py-3">
  <span aria-hidden="true" class="flex shrink-0 text-muted-foreground"><Icon class="size-5" /></span>
  <div class="flex min-w-0 flex-1 basis-56 flex-col">
    {#if title}
      <p class="text-label">
        {title}{#if state}<span aria-hidden="true" class="px-1.5 font-normal text-muted-foreground">·</span><span
            class="font-normal text-muted-foreground">{state}</span
          >{/if}
      </p>
      <p class="text-meta text-muted-foreground">{text}</p>
    {:else}
      <p class="text-body">{text}</p>
    {/if}
  </div>
  {#if children}<div class="flex shrink-0 flex-wrap gap-2">{@render children()}</div>{/if}
</div>
