package api

import (
	"net/http"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// register declares the permissions, the audit actions and the settings group of the core.
func register(x *module.Extender) {
	x.Permissions(perm.Subsystem{ID: "repositories", Scope: perm.ScopeServer, Label: "Repositories",
		Help: "Every repository of the server, with its files, settings and access."},
		perm.Def{ID: perm.RepositoriesAll, Label: "All repositories", AllRepos: true, SecondFactor: true,
			Help: "Create and delete repositories, and hold every repository permission on every repository."})
	x.Permissions(perm.Subsystem{ID: "people", Scope: perm.ScopeServer, Label: "People",
		Help: "The accounts of the server and their server permissions."},
		perm.Def{ID: perm.PeopleManage, Label: "Manage people", SecondFactor: true,
			Help: "Create, disable and enable accounts, reset passwords, and give server permissions."})
	x.Permissions(perm.Subsystem{ID: "settings", Scope: perm.ScopeServer, Label: "Settings",
		Help: "The general settings of the server, such as its public address and the upload limits."},
		perm.Def{ID: perm.SettingsGeneral, Label: "General settings", SecondFactor: true,
			Help: "Change the settings on Settings > General."})
	x.Permissions(perm.Subsystem{ID: "repo", Scope: perm.ScopeRepo, Label: "Repository",
		Help: "The files, settings and access of one repository."},
		perm.Def{ID: perm.RepoDownload, Label: "Download", Help: "Read and download the files of the repository."},
		perm.Def{ID: perm.RepoPublish, Label: "Publish", Needs: []perm.ID{perm.RepoDownload},
			Help: "Publish files through a publish session (from an enrolled device, the browser or CI)."},
		perm.Def{ID: perm.RepoDelete, Label: "Delete files", SecondFactor: true,
			Help: "Delete files and folders of the repository."},
		perm.Def{ID: perm.RepoSettings, Label: "Edit settings", SecondFactor: true,
			Help: "Change the description, the format settings, the members, the remote and the network rules."},
		perm.Def{ID: perm.RepoAccess, Label: "Manage access", SecondFactor: true,
			Help: "Give and remove the permissions of the repository, up to the ones you hold."})
	x.AuditActions(
		audit.ActionDef{ID: audit.SignInSuccess, Label: "Signed in", Subsystem: "people"},
		audit.ActionDef{ID: audit.SignInFailed, Label: "Sign-in failed", Subsystem: "people"},
		audit.ActionDef{ID: audit.TokenCreate, Label: "Read token created", Subsystem: "people"},
		audit.ActionDef{ID: audit.TokenDelete, Label: "Read token deleted", Subsystem: "people"},
		audit.ActionDef{ID: audit.UserCreate, Label: "Person created", Subsystem: "people"},
		audit.ActionDef{ID: audit.UserUpdate, Label: "Person changed", Subsystem: "people"},
		audit.ActionDef{ID: audit.UserPermissions, Label: "Server permissions changed", Subsystem: "people"},
		audit.ActionDef{ID: audit.FileUpload, Label: "File uploaded", Subsystem: "repo"},
		audit.ActionDef{ID: audit.FileDelete, Label: "File deleted", Subsystem: "repo"},
		audit.ActionDef{ID: audit.GrantSet, Label: "Repository permissions changed", Subsystem: "repo"},
		audit.ActionDef{ID: audit.RepoCreate, Label: "Repository created", Subsystem: "repositories"},
		audit.ActionDef{ID: audit.RepoUpdate, Label: "Repository changed", Subsystem: "repositories"},
		audit.ActionDef{ID: audit.RepoDelete, Label: "Repository deleted", Subsystem: "repositories"},
		audit.ActionDef{ID: audit.RepoDeleteResume, Label: "Repository delete resumed", Subsystem: "repositories"},
		audit.ActionDef{ID: audit.RepoDeleteClear, Label: "Repository delete cleared", Subsystem: "repositories"},
		audit.ActionDef{ID: audit.RepoRename, Label: "Repository renamed", Subsystem: "repositories"},
		audit.ActionDef{ID: audit.RedirectRemove, Label: "Repository redirect removed", Subsystem: "repositories"},
		audit.ActionDef{ID: audit.SettingsUpdate, Label: "Settings changed", Subsystem: "settings"},
	)
	x.Settings(settings.General())
}

// listPermissionRegistry returns the permission registry (no credential needed): {subsystems: [...]}
// in module start order, each with its permissions in registration order.
func (a *API) listPermissionRegistry(w http.ResponseWriter, r *http.Request, _ *route.Call) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"subsystems": a.h.Perms.Subsystems()})
}
