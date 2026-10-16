// The UI of the storage module: the storage settings and the storage move.

import type { ModuleUI } from "../types";
import HardDriveIcon from "@lucide/svelte/icons/hard-drive";

export const storage: ModuleUI = {
  id: "storage",
  pages: [
    {
      path: "/settings/storage", layout: "settings", access: "storage.manage", title: "Storage",
      page: () => import("../../pages/server/Storage.svelte"),
      nav: { group: "server", label: "Storage", icon: HardDriveIcon, order: 30 },
    },
  ],
};
