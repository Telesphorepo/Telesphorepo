// Package clients is the registry of publisher clients: the build tool plugins that enroll a
// device key and mint publish sessions with it (the Gradle plugin, the Maven extension). A module
// registers its clients (module.Extender.PublisherClient); a device names the client it was
// enrolled from.
package clients

import (
	"fmt"
	"regexp"
	"strings"
)

// Client is one publisher client.
type Client struct {
	ID    string // ^[a-z][a-z0-9-]{0,31}$, e.g. "gradle"
	Title string // e.g. "Gradle plugin"
	Help  string // one line for the approval page
}

var idRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// Registry holds the clients in registration order. It is fixed after startup.
type Registry struct {
	all []Client
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{} }

// Add registers c. It fails on a bad or repeated ID or an empty title.
func (r *Registry) Add(c Client) error {
	if !idRE.MatchString(c.ID) || c.Title == "" {
		return fmt.Errorf("clients: bad publisher client %q", c.ID)
	}
	if _, ok := r.Get(c.ID); ok {
		return fmt.Errorf("clients: publisher client %s registered twice", c.ID)
	}
	r.all = append(r.all, c)
	return nil
}

// Get returns client id.
func (r *Registry) Get(id string) (Client, bool) {
	for _, c := range r.all {
		if c.ID == id {
			return c, true
		}
	}
	return Client{}, false
}

// Title returns the title of client id, or id when no module registers it.
func (r *Registry) Title(id string) string {
	if c, ok := r.Get(id); ok {
		return c.Title
	}
	return id
}

// IDs returns the IDs of every client, quoted and joined for an error message.
func (r *Registry) IDs() string {
	q := make([]string, len(r.all))
	for i, c := range r.all {
		q[i] = `"` + c.ID + `"`
	}
	return strings.Join(q, ", ")
}
