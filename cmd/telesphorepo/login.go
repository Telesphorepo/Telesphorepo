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

// usableDevice returns the device of the active account of the server of c when the server
// accepts it, else nil. A device that the server refuses gets a line on stderr.
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
	warn("%s no longer accepts device %d (%s).", c.id.base(), d.DeviceID, detailOf(err))
}

// loginDevice runs the device flow for the server of c and saves the device as the account of
// the user who approved it, which becomes the active account. When that user has an account on
// this computer already, the new device replaces its device, which the CLI revokes on the server.
// An account whose user the CLI does not know and that the server no longer accepts goes too.
func loginDevice(ctx context.Context, c *client, name string) (*deviceFile, error) {
	dir, err := serverDir(c.id)
	if err != nil {
		return nil, err
	}
	before, err := accountsIn(dir)
	if err != nil {
		return nil, err
	}
	d, err := c.enroll(ctx, name)
	if err != nil {
		return nil, err
	}
	for _, o := range before {
		switch {
		case o.accountName() == d.accountName():
			// The same user: the new device replaces the old one, which the server forgets too.
			if err := revokeSelf(ctx, c, o); err == nil {
				note("Revoked device %d, the device %s had on this computer before.", o.DeviceID, d.label())
			}
		case o.User == "":
			if _, _, err := c.challenge(ctx, o, false, ""); refused(err) {
				if err := removeAccount(dir, o); err == nil {
					note("Removed device %d, which %s no longer accepts.", o.DeviceID, c.id.base())
				}
			}
		}
	}
	f, err := d.save(dir)
	if err != nil {
		return nil, err
	}
	saveRegistryHosts(ctx, c, dir)
	if d.User != "" {
		done("This computer is device %d of %s, as %s.", d.DeviceID, c.id.base(), errp.bold(d.User))
	} else {
		done("This computer is device %d of %s.", d.DeviceID, c.id.base())
	}
	note("Its key is in %s", f)
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

// login enrolls this computer as a device of the server. A computer with accounts on the server
// keeps the active one; on a terminal it asks first: keep it, switch to another account, or log
// in as another account.
func login(ctx context.Context, args []string) error {
	name, rest, err := nameFlag("login", args)
	if err != nil {
		return err
	}
	var id identity
	switch len(rest) {
	case 0:
		// Without SERVER: the server of this folder's project, or the one server of this computer.
		if dirs, _ := enrolledDirs(); len(dirs) == 0 {
			return usage("login")
		}
		if id, err = pickServer("login"); err != nil {
			return err
		}
	case 1:
		if id, err = parseIdentity(rest[0]); err != nil {
			return err
		}
	default:
		return usage("login")
	}
	if name == "" {
		name = deviceName()
	}
	c := newClient(id)
	if err := attach(c, false).check(ctx, "login"); err != nil {
		return err
	}
	d, dir, err := usableDevice(ctx, c)
	if err != nil {
		return err
	}
	ds, err := accountsIn(dir)
	if err != nil {
		return err
	}
	if len(ds) == 0 || !interactive() {
		if d != nil {
			as := ""
			if d.User != "" {
				as = ", as " + errp.bold(d.User)
			}
			done("This computer is already device %d of %s%s.", d.DeviceID, id.base(), as)
			return nil
		}
		_, err = loginDevice(ctx, c, name)
		return err
	}

	active := activeOf(dir, ds)
	title("Accounts of this computer on %s", id.base())
	for _, a := range ds {
		mark, who := "  ", a.label()
		if a == active {
			mark, who = errp.green("*")+" ", errp.bold(who)+errp.dim(" (active)")
		}
		fmt.Fprintf(os.Stderr, "  %s%s  %s\n", mark, who, errp.dim(fmt.Sprintf("device %d", a.DeviceID)))
	}
	keep := "Keep " + active.label()
	if d == nil {
		keep = "Log in again as " + active.label()
	}
	options := []string{keep}
	var others []*deviceFile
	for _, a := range ds {
		if a != active {
			options = append(options, "Switch to "+a.label())
			others = append(others, a)
		}
	}
	options = append(options, "Log in as another account")
	n := choose("What now?", options, 0)
	switch {
	case n == 0 && d != nil:
		done("%s stays the active account of %s.", active.label(), id.base())
		return nil
	case n == 0:
		note("Approve the code in the browser as %s.", active.label())
	case n == len(options)-1:
		note("Sign in to the browser as the account to add. The approval page names the account that approves, and offers to sign in as someone else.")
	default:
		a := others[n-1]
		if err := setActive(dir, a); err != nil {
			return err
		}
		done("%s is now the active account of %s.", errp.bold(a.label()), id.base())
		if _, _, err := c.challenge(ctx, a, false, ""); refused(err) {
			deviceRefused(c, a, err)
			note("Run telesphorepo login %s again to log in as %s.", id.base(), a.label())
		}
		return nil
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

// logout revokes the device of the active account of the server on the server and deletes its
// files. Another account of the server becomes the active one: on a terminal the person picks it,
// else the first by name. The last account takes the credential helper entries of the server and
// ~/.telesphorepo/<server>/ with it.
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
	ds, err := accountsIn(dir)
	if err != nil {
		return err
	}
	d := activeOf(dir, ds)
	if d == nil {
		// The key cannot sign the revoke; the device stays in Settings > Devices of the server
		// until it expires or someone revokes it there.
		warn("The CLI cannot revoke the device with this file, so it only deletes the file.")
	} else {
		c := newClient(id)
		if err := revokeSelf(ctx, c, d); err != nil {
			if !refused(err) {
				return fmt.Errorf("cannot revoke device %d on %s, so this computer keeps its key: %w", d.DeviceID, id.base(), err)
			}
			warn("%s refused the revoke of device %d (%s); the CLI deletes the key anyway.", id.base(), d.DeviceID, detailOf(err))
		} else {
			done("Revoked device %d on %s.", d.DeviceID, id.base())
		}
	}
	var rest []*deviceFile
	for _, o := range ds {
		if o != d {
			rest = append(rest, o)
		}
	}
	if len(rest) > 0 {
		if err := removeAccount(dir, d); err != nil {
			return fmt.Errorf("cannot delete the files of %s: %w", d.label(), err)
		}
		done("Logged out %s.", d.label())
		next := rest[0]
		if len(rest) > 1 && interactive() {
			labels := make([]string, len(rest))
			for i, o := range rest {
				labels[i] = o.label()
			}
			next = rest[choose("Which account is active now?", labels, 0)]
		}
		if err := setActive(dir, next); err != nil {
			return err
		}
		done("%s is now the active account of %s.", errp.bold(next.label()), id.base())
		return nil
	}
	if cfg, removed, err := removeCredHelpers(dockerHosts(id, dir)); err != nil {
		return err
	} else if len(removed) > 0 {
		done("Removed the credential helper of %s from %s.", strings.Join(removed, " and "), cfg)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("cannot delete %s: %w", dir, err)
	}
	done("Deleted %s.", dir)
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

// status prints each server of this computer with its accounts (the active one marked), their
// devices, whether the server accepts each, and the hooks; then the project of the current
// folder. It prints no secret.
func status(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return usage("status")
	}
	dirs, err := enrolledDirs()
	if err != nil {
		return err
	}
	cfgPath, _ := dockerConfigPath()
	helpers := currentCredHelpers()
	shown := 0
	for _, dir := range dirs {
		ds, err := accountsIn(dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		active := activeOf(dir, ds)
		if active == nil {
			continue
		}
		shown++
		fmt.Println(out.bold(active.Server))
		var pairs []string
		for _, d := range ds {
			state := out.green("the server accepts it")
			if _, _, err := newClient(d.id).challenge(ctx, d, false, ""); refused(err) {
				state = out.red("the server refuses it: " + detailOf(err))
			} else if err != nil {
				state = out.yellow("the server cannot be reached: " + err.Error())
			}
			who := "  " + d.label()
			if d == active {
				who = "* " + d.label() + " (active)"
			}
			pairs = append(pairs, who, fmt.Sprintf("device %d, created %s, %s", d.DeviceID, d.CreatedAt, state))
		}
		fields(os.Stdout, out, "  ", pairs...)
		var hooked []string
		for _, h := range dockerHosts(active.id, dir) {
			if helpers[h] == helperName {
				hooked = append(hooked, h)
			}
		}
		if len(hooked) > 0 {
			fmt.Printf("  Docker credential helper for %s in %s\n", strings.Join(hooked, ", "), out.code(cfgPath))
		} else {
			fmt.Println(out.dim("  no Docker credential helper"))
		}
	}
	if shown == 0 {
		fmt.Println("This computer has no device of any server.")
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
		fmt.Printf("  no project marker in %s; telesphorepo publish FILE uploads files to a raw, Arch or RPM repo\n", p.dir)
	}
	return nil
}
