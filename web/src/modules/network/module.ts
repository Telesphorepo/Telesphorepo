// The UI of the network module: network lists and the trusted proxies.

import type { ModuleUI } from "../types";
import NetworkIcon from "@lucide/svelte/icons/network";
import WaypointsIcon from "@lucide/svelte/icons/waypoints";

export const network: ModuleUI = {
  id: "network",
  pages: [
    {
      path: "/settings/networks", layout: "settings", access: "network.manage", title: "Networks",
      page: () => import("../../pages/server/IPLists.svelte"),
      nav: { group: "people", label: "Networks", icon: NetworkIcon, order: 30 },
    },
    {
      path: "/settings/networks/new", layout: "settings", access: "network.manage", title: "New network list",
      page: () => import("../../pages/server/IPListPage.svelte"),
    },
    {
      path: "/settings/networks/list/:name", layout: "settings", access: "network.manage", title: "Network list",
      page: () => import("../../pages/server/IPListPage.svelte"),
    },
    {
      path: "/settings/proxy", layout: "settings", access: "network.manage", title: "Network and proxy",
      page: () => import("../../pages/server/ReverseProxy.svelte"),
      nav: { group: "server", label: "Network and proxy", icon: WaypointsIcon, order: 50 },
    },
  ],
};
