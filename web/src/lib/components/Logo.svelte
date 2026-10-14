<!-- The logo mark: a ticket with a T punched out. The yellow stays the same in both themes. -->
<script lang="ts">
  let { size = 28, class: className = "" }: { size?: number; class?: string } = $props();
</script>

<svg
  width={size}
  height={size}
  viewBox="0 0 32 32"
  class={className}
  aria-hidden="true"
  focusable="false"
>
  <path
    fill="#E9B546"
    fill-rule="evenodd"
    d="M5 7h22a2 2 0 0 1 2 2v4a3 3 0 0 0 0 6v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4a3 3 0 0 0 0-6V9a2 2 0 0 1 2-2Zm5 5v3h4.5v8h3v-8H22v-3Z"
  />
</svg>
