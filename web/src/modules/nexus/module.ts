// The UI of the nexus module: the Nexus migration.

import type { ModuleUI } from "../types";
import ArrowRightLeftIcon from "@lucide/svelte/icons/arrow-right-left";

export const nexus: ModuleUI = {
  id: "nexus",
  pages: [
    {
      path: "/settings/nexus", layout: "settings", access: "nexus.manage", title: "Nexus migration",
      page: () => import("../../pages/server/NexusMigration.svelte"),
      nav: { group: "server", label: "Nexus migration", icon: ArrowRightLeftIcon, order: 40 },
    },
    // Before /settings/nexus/:id, which would take "merge" as an id.
    {
      path: "/settings/nexus/merge", layout: "settings", access: ["nexus.manage", "repositories.all"], title: "Merge",
      page: () => import("../../pages/server/NexusMergePlan.svelte"),
    },
    {
      path: "/settings/nexus/merges/:id", layout: "settings", access: ["nexus.manage", "repositories.all"], title: "Merge",
      page: () => import("../../pages/server/NexusMerge.svelte"),
    },
    {
      path: "/settings/nexus/:id", layout: "settings", access: "nexus.manage", title: "Nexus server",
      page: () => import("../../pages/server/NexusSource.svelte"),
    },
  ],
};
