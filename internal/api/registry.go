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
	x.AuditActions(coreActions()...)
	x.Settings(settings.General())
}

// The words of the sign-in methods (auth.Service.RecordSignIn).
var signInMethods = map[string]string{
	"password": "with a password",
	"totp":     "with an authenticator app",
	"passkey":  "with a passkey",
	"recovery": "with a recovery code",
}

// The words of what a repo.update changed: its own parts and the fields of the modules.
var repoChanges = map[string]string{
	"description":    "description",
	"anonymous_read": "public or private",
	"config":         "format settings",
	"proxy":          "remote",
	"members":        "members",
	"signing_key":    "signing key",
	"rehearsal_for":  "rehearsal",
	"moves_into":     "move project by project",
}

// coreActions are the actions that the kernel records, with their sentences and fields.
func coreActions() []audit.ActionDef {
	return []audit.ActionDef{
		{ID: audit.SignInSuccess, Label: "Signed in", Subsystem: "people",
			Sentence: "{actor} signed in {method}",
			Fields:   []audit.Field{audit.Enum("method", "Sign-in method", signInMethods)}},
		{ID: audit.SignInFailed, Label: "Sign-in failed", Subsystem: "people",
			Sentence: "Sign-in as {target} failed",
			Fields: []audit.Field{audit.Enum("reason", "Reason", map[string]string{
				"password":      "wrong username or password",
				"second factor": "wrong code at the second step",
				"password sign-in is off for people who manage": "password sign-in is off for people who manage the server",
				"admin password sign-in is disabled":            "password sign-in is off for administrators",
			})}},
		{ID: audit.TokenCreate, Label: "Read token created", Subsystem: "people",
			Sentence: "{actor} created the read token {target}",
			Fields:   []audit.Field{audit.Hidden("token_id", "Token")}},
		{ID: audit.TokenDelete, Label: "Read token deleted", Subsystem: "people",
			Sentence: "{actor} deleted a read token",
			Fields:   []audit.Field{audit.Hidden("token_id", "Token")}},
		{ID: audit.UserCreate, Label: "Person created", Subsystem: "people",
			Sentence: "{actor} created the account {target}",
			Fields: []audit.Field{
				audit.Perms("permissions", "Server permissions", "server"),
				audit.Bool("is_admin", "Administrator", "Administrator", ""),
			}},
		{ID: audit.UserUpdate, Label: "Person changed", Subsystem: "people",
			Sentence: "{actor} changed the account {target}",
			Fields: []audit.Field{
				audit.Bool("disabled", "Disabled", "Disabled", "Enabled"),
				audit.Enum("password", "Password", map[string]string{"changed": "password changed"}),
				audit.Enum("second_factors", "Two-step verification", map[string]string{"reset": "two-step verification reset"}),
				audit.Bool("is_admin", "Administrator", "administrator", "no longer an administrator"),
			}},
		{ID: audit.UserPermissions, Label: "Server permissions changed", Subsystem: "people",
			Sentence: "{actor} changed the server permissions of {target} from {old} to {new}",
			Fields: []audit.Field{
				audit.Perms("old", "Before", "server"),
				audit.Perms("new", "After", "server"),
			}},
		{ID: audit.FileUpload, Label: "File uploaded", Subsystem: "repo",
			Sentence: "{actor} published {target} to {repo}",
			Fields: []audit.Field{
				audit.Size("size", "Size"),
				audit.Hidden("sha256", "SHA-256"),
				audit.Enum("source", "Sent from", map[string]string{
					"put": "A build tool or the command line", "ui": "The browser", "import": "Nexus"}).Quietly(),
				audit.Bool("manual", "From the browser", "from the browser", "").Quietly(),
			}},
		{ID: audit.FileDelete, Label: "File deleted", Subsystem: "repo",
			Sentence: "{actor} deleted {target} from {repo}",
			Fields:   []audit.Field{audit.Count("count", "Files", "file", "files")}},
		{ID: audit.GrantSet, Label: "Repository permissions changed", Subsystem: "repo",
			Sentence: "{actor} changed the permissions of {target} on {repo} from {old} to {new}",
			Fields: []audit.Field{
				audit.Perms("old", "Before", "repo"),
				audit.Perms("new", "After", "repo"),
			}},
		{ID: audit.RepoCreate, Label: "Repository created", Subsystem: "repositories",
			Sentence: "{actor} created the repository {repo}",
			Fields: []audit.Field{
				audit.Enum("format", "Format", nil).From(audit.WordsFormats),
				audit.Enum("kind", "Kind", map[string]string{"hosted": "Hosted", "proxy": "Proxy", "group": "Group"}),
				audit.Bool("anonymous_read", "Public", "Public", "Private"),
				audit.List("members", "Members"),
				audit.Text("remote_url", "Remote"),
			}},
		{ID: audit.RepoUpdate, Label: "Repository changed", Subsystem: "repositories",
			Sentence: "{actor} changed the settings of {repo}",
			Fields: []audit.Field{
				audit.List("fields", "Changed").Words(repoChanges),
				audit.Bool("anonymous_read", "Public", "Public", "Private"),
				audit.List("proxy", "Remote settings changed").Words(map[string]string{
					"remote_url": "address", "credentials": "sign-in", "metadata_ttl": "metadata cache time",
					"not_found_ttl": "not-found cache time"}).Quietly(),
				audit.List("members", "Members").Quietly(),
				audit.Text("remote_url", "Remote").Quietly(),
				audit.Text("renamed_from", "Old name").Quietly(),
			}},
		{ID: audit.RepoDelete, Label: "Repository deleted", Subsystem: "repositories",
			Sentence: "Repository {repo} deleted",
			Fields:   []audit.Field{audit.Count("count", "Files", "file", "files")}},
		{ID: audit.RepoDeleteResume, Label: "Repository delete resumed", Subsystem: "repositories",
			Sentence: "{actor} resumed the delete of {repo}"},
		{ID: audit.RepoDeleteClear, Label: "Repository delete cleared", Subsystem: "repositories",
			Sentence: "{actor} ended the failed delete of {repo}",
			Fields:   []audit.Field{audit.Count("files", "Files left", "file left", "files left")}},
		{ID: audit.RepoRename, Label: "Repository renamed", Subsystem: "repositories",
			Sentence: "{actor} renamed {renamed_from} to {repo}",
			Fields: []audit.Field{
				audit.Text("renamed_from", "Old name"),
				audit.Text("redirect_to", "Its names now lead to").Quietly(),
			}},
		{ID: audit.RedirectAdd, Label: "Repository redirect added", Subsystem: "repositories",
			Sentence: "{actor} made the name {redirect} lead to {repo}",
			Fields:   []audit.Field{audit.Text("redirect", "Name")}},
		{ID: audit.RedirectRemove, Label: "Repository redirect removed", Subsystem: "repositories",
			Sentence: "{actor} removed the name {redirect}, which led to {repo}",
			Fields:   []audit.Field{audit.Text("redirect", "Name")}},
		{ID: audit.SettingsUpdate, Label: "Settings changed", Subsystem: "settings",
			Sentence: "{actor} changed the settings",
			Fields: []audit.Field{
				audit.List("keys", "Settings").From(audit.WordsSettings),
				audit.Enum("via", "When", map[string]string{
					"first sign-in": "at the first sign-in", "download link": "with the first download link"}),
			}},
	}
}

// listPermissionRegistry returns the permission registry (no credential needed): {subsystems: [...]}
// in module start order, each with its permissions in registration order.
func (a *API) listPermissionRegistry(w http.ResponseWriter, r *http.Request, _ *route.Call) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"subsystems": a.h.Perms.Subsystems()})
}
