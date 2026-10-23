<!--
  One numbered step of a setup (Single sign-on: Connect your provider, Choose who becomes an
  administrator, Sign-in options): a Section whose title starts with the step's number. A
  finished step shows a check in place of the number. Put the step's FormGrid inside; a step that
  connects to another system has its Test action (ConnectionTest) in `actions` or `footer`.

    <Step n={1} title="Connect your provider" description="Register Telesphorepo as an app there." done={connected}>
      <FormGrid>...</FormGrid>
    </Step>
-->
<script lang="ts">
  import type { Snippet } from "svelte";
  import CheckIcon from "@lucide/svelte/icons/check";
  import * as Card from "$lib/components/ui/card";

  let {
    n,
    title,
    description,
    done = false,
    actions,
    footer,
    children,
  }: {
    n: number;
    title: string;
    description?: string;
    done?: boolean;
    /** @deprecated Steps are cards; there is no line to the next step. */
    last?: boolean;
    actions?: Snippet;
    footer?: Snippet;
    children: Snippet;
  } = $props();
</script>

<Card.Root class="mt-(--section-gap) text-body first:mt-0 max-sm:[--card-spacing:--spacing(5)]" role="group" aria-label="Step {n}: {title}">
  <Card.Header class="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
    <div class="flex min-w-0 grow basis-56 items-start gap-3">
      <span class="flex h-6 min-w-4 shrink-0 items-center text-section text-muted-foreground tabular-nums">
        {#if done}<CheckIcon aria-hidden="true" class="size-4 text-success" /><span class="sr-only">Done</span>{:else}{n}{/if}
      </span>
      <div class="min-w-0">
        <h2 class="text-section">{title}</h2>
        {#if description}<p class="mt-1 text-meta text-muted-foreground">{description}</p>{/if}
      </div>
    </div>
    {#if actions}<div data-slot="header-actions" class="flex shrink-0 flex-wrap items-center gap-2">{@render actions()}</div>{/if}
  </Card.Header>
  <Card.Content class="min-w-0">{@render children()}</Card.Content>
  {#if footer}<Card.Footer class="flex-wrap gap-2">{@render footer()}</Card.Footer>{/if}
</Card.Root>
