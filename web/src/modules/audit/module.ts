// The UI of the audit module: the activity.

import type { ModuleUI } from "../types";
import HistoryIcon from "@lucide/svelte/icons/history";

export const audit: ModuleUI = {
  id: "audit",
  pages: [
    {
      path: "/settings/activity", layout: "settings", access: "activity.read", title: "Activity",
      page: () => import("../../pages/server/Activity.svelte"),
      nav: { group: "server", label: "Activity", icon: HistoryIcon, order: 70 },
    },
  ],
};
