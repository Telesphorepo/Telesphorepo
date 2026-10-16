// The UI of the sso module: the sign-in options.

import type { ModuleUI } from "../types";
import LogInIcon from "@lucide/svelte/icons/log-in";

export const sso: ModuleUI = {
  id: "sso",
  pages: [
    {
      path: "/settings/sign-in", layout: "settings", access: "signin.manage", title: "Sign-in options",
      page: () => import("../../pages/server/SingleSignOn.svelte"),
      nav: { group: "server", label: "Sign-in options", icon: LogInIcon, order: 10 },
    },
  ],
};
