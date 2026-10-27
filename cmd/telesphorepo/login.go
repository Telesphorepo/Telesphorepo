package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// deviceName is the default name of a new device: the host name of the computer.
func deviceName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "unknown computer"
	}
	return host
}

// usableDevice returns the device of the server of c when the server accepts it, else nil. A
// device that the server refuses gets a line on stderr.
func usableDevice(ctx context.Context, c *client) (*deviceFile, string, error) {
	d, dir, err := deviceOf(c.id)
	if err != nil || d == nil {
		return nil, dir, err
	}
	_, _, err = c.challenge(ctx, d, false, "")
	switch {
	case err == nil:
		return d, dir, nil
	case refused(err):
		deviceRefused(c, d, err)
		return nil, dir, nil
	}
	return nil, dir, err
}

// deviceRefused prints that the server of c refused device d with err.
func deviceRefused(c *client, d *deviceFile, err error) {
	fmt.Fprintf(os.Stderr, "%s no longer accepts device %d (%s).\n", c.id.base(), d.DeviceID, detailOf(err))
}

// loginDevice runs the device flow for the server of c and saves the device file and hosts.json.
func loginDevice(ctx context.Context, c *client, name string) (*deviceFile, error) {
	dir, err := serverDir(c.id)
	if err != nil {
		return nil, err
	}
	d, err := c.enroll(ctx, name)
	if err != nil {
		return nil, err
	}
	f, err := d.save(dir)
	if err != nil {
		return nil, err
	}
	saveRegistryHosts(ctx, c, dir)
	fmt.Fprintf(os.Stderr, "This computer is device %d of %s. Its key is in %s\n", d.DeviceID, c.id.base(), f)
	return d, nil
}

// saveRegistryHosts writes the Docker hosts of the server to hosts.json in dir. A server without
// the Docker format has none; the file then holds an empty list.
func saveRegistryHosts(ctx context.Context, c *client, dir string) {
	hosts, err := c.registryHosts(ctx)
	if err != nil {
		hosts = nil
	}
	if err := saveHosts(dir, hosts); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

// login enrolls this computer as a device of the server, or keeps the device that the server
// accepts.
func login(ctx context.Context, args []string) error {
	name, rest, err := nameFlag("login", args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usage("login")
	}
	id, err := parseIdentity(rest[0])
	if err != nil {
		return err
	}
	c := newClient(id)
	if err := attach(c, false).check(ctx, "login"); err != nil {
		return err
	}
	d, _, err := usableDevice(ctx, c)
	if err != nil {
		return err
	}
	if d != nil {
		fmt.Fprintf(os.Stderr, "This computer is already device %d of %s.\n", d.DeviceID, id.base())
		return nil
	}
	if name == "" {
		name = deviceName()
	}
	_, err = loginDevice(ctx, c, name)
	return err
}

// nameFlag reads the optional --name NAME of args and returns the other arguments.
func nameFlag(cmd string, args []string) (string, []string, error) {
	var name string
	var rest []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--name" && i+1 < len(args):
			name = args[i+1]
			i++
		case strings.HasPrefix(a, "--name="):
			name = strings.TrimPrefix(a, "--name=")
		case strings.HasPrefix(a, "-") && a != "-":
			return "", nil, usage(cmd)
		default:
			rest = append(rest, a)
		}
	}
	return name, rest, nil
}

// pickServer returns the server that a command without SERVER acts on: the server of the project
// of the current folder when this computer has its device, else the one enrolled server.
func pickServer(cmd string) (identity, error) {
	if p, err := findProject("."); err == nil && p != nil {
		if d, _, err := deviceOf(p.id); err == nil && d != nil {
			return p.id, nil
		}
	}
	ds, _, err := enrolledDevices()
	if err != nil {
		return identity{}, err
	}
	switch len(ds) {
	case 0:
		return identity{}, errors.New("this computer has no device of any server")
	case 1:
		return ds[0].id, nil
	}
	names := make([]string, len(ds))
	for i, d := range ds {
		names[i] = "  " + d.Server
	}
	return identity{}, fmt.Errorf("this computer has devices of several servers. Name one: telesphorepo %s SERVER\n%s", cmd, strings.Join(names, "\n"))
}

// logout revokes the device of the server on the server, removes the hook entries of that server
// and deletes ~/.telesphorepo/<server>/.
func logout(ctx context.Context, args []string) error {
	if len(args) > 1 || (len(args) == 1 && strings.HasPrefix(args[0], "-")) {
		return usage("logout")
	}
	var id identity
	var err error
	if len(args) == 1 {
		id, err = parseIdentity(args[0])
	} else {
		id, err = pickServer("logout")
	}
	if err != nil {
		return err
	}
	dir, err := serverDir(id)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("this computer has no device of %s", id.base())
	}
	d, err := loadDevice(dir)
	if err != nil {
		// The key cannot sign the revoke; the device stays in Settings > Devices of the server
		// until it expires or someone revokes it there.
		fmt.Fprintf(os.Stderr, "%v\nThe CLI cannot revoke the device with this file, so it only deletes the file.\n", err)
	}
	if d != nil {
		c := newClient(id)
		if err := revokeSelf(ctx, c, d); err != nil {
			if !refused(err) {
				return fmt.Errorf("cannot revoke device %d on %s, so this computer keeps its key: %w", d.DeviceID, id.base(), err)
			}
			fmt.Fprintf(os.Stderr, "%s refused the revoke of device %d (%s); the CLI deletes the key anyway.\n", id.base(), d.DeviceID, detailOf(err))
		} else {
			fmt.Fprintf(os.Stderr, "Revoked device %d on %s.\n", d.DeviceID, id.base())
		}
	}
	if cfg, removed, err := removeCredHelpers(dockerHosts(id, dir)); err != nil {
		return err
	} else if len(removed) > 0 {
		fmt.Fprintf(os.Stderr, "Removed the credential helper of %s from %s.\n", strings.Join(removed, " and "), cfg)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("cannot delete %s: %w", dir, err)
	}
	fmt.Fprintf(os.Stderr, "Deleted %s.\n", dir)
	return nil
}

// revokeSelf revokes device d with its own signature (DELETE /api/devices/self).
func revokeSelf(ctx context.Context, c *client, d *deviceFile) error {
	nonce, _, err := c.challenge(ctx, d, false, "")
	if err != nil {
		return err
	}
	_, err = c.call(ctx, request{method: http.MethodDelete, path: "/api/devices/self", json: map[string]any{
		"device_id": d.DeviceID, "nonce": nonce,
		"signature": d.sign(revokePrefix + nonce + "\n" + strconv.FormatInt(d.DeviceID, 10)),
	}})
	return err
}

// status prints each enrolled server with its device, whether the server accepts it and its
// hooks, then the project of the current folder. It prints no secret.
func status(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return usage("status")
	}
	ds, dirs, err := enrolledDevices()
	if err != nil {
		return err
	}
	if len(ds) == 0 {
		fmt.Println("This computer has no device of any server.")
	}
	cfgPath, _ := dockerConfigPath()
	helpers := currentCredHelpers()
	for i, d := range ds {
		fmt.Println(d.Server)
		state := "the server accepts it"
		if _, _, err := newClient(d.id).challenge(ctx, d, false, ""); refused(err) {
			state = "the server refuses it: " + detailOf(err)
		} else if err != nil {
			state = "the server cannot be reached: " + err.Error()
		}
		fmt.Printf("  device %d, created %s, %s\n", d.DeviceID, d.CreatedAt, state)
		var hooked []string
		for _, h := range dockerHosts(d.id, dirs[i]) {
			if helpers[h] == helperName {
				hooked = append(hooked, h)
			}
		}
		if len(hooked) > 0 {
			fmt.Printf("  Docker credential helper for %s in %s\n", strings.Join(hooked, ", "), cfgPath)
		} else {
			fmt.Println("  no Docker credential helper")
		}
	}

	fmt.Println()
	p, err := findProject(".")
	if err != nil {
		return err
	}
	if p == nil {
		fmt.Printf("No %s in this folder or above.\n", projectFileName)
		return nil
	}
	fmt.Printf("Project file %s: repo %s on %s\n", p.file, p.Repo, p.id.base())
	found := false
	for _, r := range rows {
		if len(r.markers) == 0 || (p.Kind != "" && p.Kind != r.kind) {
			continue
		}
		if m := r.marker(p.dir); m != "" {
			found = true
			fmt.Printf("  %s (%s): telesphorepo publish runs %s\n", r.title, m, r.preview(p.dir))
		}
	}
	switch {
	case p.Dist != "" || p.Component != "":
		fmt.Printf("  Debian: telesphorepo publish FILE.deb uploads to %s/%s\n", p.Dist, p.Component)
	case !found:
		fmt.Printf("  no project marker in %s; telesphorepo publish FILE uploads files to a raw or Arch repo\n", p.dir)
	}
	return nil
}
