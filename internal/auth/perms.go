package auth

import (
	"strings"
)

// RepoPerm is a set of repository permissions (spec "Permissions"). A person holds them per
// repository; PermRepos holds all of them on every repository.
type RepoPerm uint8

const (
	RepoDownload  RepoPerm = 1 << iota // read the files
	RepoPublish                        // publish through a publish session; needs RepoDownload
	RepoDelete                         // delete files and folders
	RepoSettings                       // edit the repository settings and network rules
	RepoAccess                         // give and remove the permissions of this repository
	RepoPipelines                      // add and edit CI pipelines; needs RepoPublish

	// AllRepoPerms is every repository permission.
	AllRepoPerms = RepoDownload | RepoPublish | RepoDelete | RepoSettings | RepoAccess | RepoPipelines
)

// RepoPermNames are the stored names of the repository permissions, in display order.
var RepoPermNames = []string{"download", "publish", "delete", "settings", "access", "pipelines"}

// ServerPerm is a set of server permissions. An administrator holds AllServerPerms.
type ServerPerm uint16

const (
	PermRepos    ServerPerm = 1 << iota // create and delete repos; every repository permission everywhere
	PermPeople                          // create, edit and disable people, and set their permissions
	PermSignIn                          // Settings › Sign-in options
	PermStorage                         // Settings › Storage
	PermNetwork                         // Settings › Network and proxy, Networks
	PermKeys                            // Settings › Signing keys
	PermNexus                           // Settings › Nexus migration
	PermGeneral                         // Settings › General
	PermActivity                        // Settings › Activity (the audit log)

	// AllServerPerms is every server permission: an administrator.
	AllServerPerms = PermRepos | PermPeople | PermSignIn | PermStorage | PermNetwork | PermKeys | PermNexus | PermGeneral | PermActivity
)

// ServerPermNames are the stored names of the server permissions, in display order.
var ServerPermNames = []string{"repos", "people", "signin", "storage", "network", "keys", "nexus", "general", "activity"}

func names(set uint16, all []string) []string {
	out := []string{}
	for i, n := range all {
		if set&(1<<i) != 0 {
			out = append(out, n)
		}
	}
	return out
}

func parse(xs []string, all []string) (uint16, error) {
	var set uint16
	for _, x := range xs {
		i := indexOf(all, x)
		if i < 0 {
			return 0, ErrInvalid
		}
		set |= 1 << i
	}
	return set, nil
}

func indexOf(all []string, x string) int {
	for i, n := range all {
		if n == x {
			return i
		}
	}
	return -1
}

// splitPerms reads a comma-joined perms column ("" for none). Unknown names are dropped; the
// schema CHECK allows none.
func splitPerms(s string, all []string) uint16 {
	var set uint16
	for x := range strings.SplitSeq(s, ",") {
		if i := indexOf(all, x); i >= 0 {
			set |= 1 << i
		}
	}
	return set
}

// Has reports whether p holds every permission of q.
func (p RepoPerm) Has(q RepoPerm) bool { return p&q == q }

// Any reports whether p holds at least one permission of q.
func (p RepoPerm) Any(q RepoPerm) bool { return p&q != 0 }

// Names returns the names of p in display order.
func (p RepoPerm) Names() []string { return names(uint16(p), RepoPermNames) }

// Valid reports whether p keeps the dependency rules: publish needs download, and pipelines
// needs publish.
func (p RepoPerm) Valid() bool {
	return (!p.Has(RepoPublish) || p.Has(RepoDownload)) && (!p.Has(RepoPipelines) || p.Has(RepoPublish))
}

// Elevated reports whether p holds more than download and publish (the second factor applies).
func (p RepoPerm) Elevated() bool { return p&^(RepoDownload|RepoPublish) != 0 }

// ParseRepoPerms reads names into a set. An unknown name returns ErrInvalid.
func ParseRepoPerms(xs []string) (RepoPerm, error) {
	s, err := parse(xs, RepoPermNames)
	return RepoPerm(s), err
}

// Has reports whether p holds every permission of q.
func (p ServerPerm) Has(q ServerPerm) bool { return p&q == q }

// Names returns the names of p in display order.
func (p ServerPerm) Names() []string { return names(uint16(p), ServerPermNames) }

// ParseServerPerms reads names into a set. An unknown name returns ErrInvalid.
func ParseServerPerms(xs []string) (ServerPerm, error) {
	s, err := parse(xs, ServerPermNames)
	return ServerPerm(s), err
}

// ServerPermsOf reads a comma-joined perms column of a query.
func ServerPermsOf(col string) ServerPerm { return ServerPerm(splitPerms(col, ServerPermNames)) }
func repoPermOf(name string) RepoPerm     { return RepoPerm(splitPerms(name, RepoPermNames)) }
