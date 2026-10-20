// The UI of the edge module: the Edge delivery settings page.

import type { ModuleUI } from "../types";
import GlobeIcon from "@lucide/svelte/icons/globe";

export const edge: ModuleUI = {
  id: "edge",
  pages: [
    {
      path: "/settings/edge", layout: "settings", access: "edge.manage", title: "Edge delivery",
      page: () => import("../../pages/server/EdgeDelivery.svelte"),
      nav: { group: "server", label: "Edge delivery", icon: GlobeIcon, order: 35 },
    },
  ],
};
