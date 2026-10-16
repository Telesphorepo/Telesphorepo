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
    {
      path: "/settings/nexus/:id", layout: "settings", access: "nexus.manage", title: "Nexus server",
      page: () => import("../../pages/server/NexusSource.svelte"),
    },
  ],
};
