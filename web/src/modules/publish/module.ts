// The UI of the publish module: device approval, your devices and the CI pipelines.

import type { ModuleUI } from "../types";
import LaptopIcon from "@lucide/svelte/icons/laptop";
import WorkflowIcon from "@lucide/svelte/icons/workflow";

export const publish: ModuleUI = {
  id: "publish",
  pages: [
    { path: "/setup/:code", layout: "app", access: "any", title: "Set up a project", page: () => import("../../pages/Setup.svelte") },
    { path: "/device", layout: "app", access: "user", title: "Approve a device", page: () => import("../../pages/DeviceApprove.svelte") },
    {
      path: "/settings/devices", layout: "settings", access: "user", title: "Devices",
      page: () => import("../../pages/account/Devices.svelte"),
      nav: { group: "account", label: "Devices", icon: LaptopIcon, order: 30 },
    },
    {
      path: "/settings/ci-pipelines", layout: "settings", access: "ci.pipelines", title: "CI pipelines",
      page: () => import("../../pages/server/CIPublishing.svelte"),
      nav: { group: "people", label: "CI pipelines", icon: WorkflowIcon, order: 20 },
    },
    {
      path: "/settings/ci-pipelines/new", layout: "settings", access: "ci.pipelines", title: "New CI pipeline",
      page: () => import("../../pages/server/CIPublishingRule.svelte"),
    },
    {
      path: "/settings/ci-pipelines/:id", layout: "settings", access: "ci.pipelines", title: "CI pipeline",
      page: () => import("../../pages/server/CIPublishingRule.svelte"),
    },
  ],
};
