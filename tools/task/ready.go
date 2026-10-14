package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// readyToken marks the machine-readable line "telesphorepo dev" prints once it listens,
// e.g. `TELESPHOREPO_READY url=http://localhost:8080 user=admin password=... data="./.telesphorepo-dev"`
// (a JSON object after the token is accepted too). tools/task swallows the line and prints
// one combined banner instead.
const readyToken = "TELESPHOREPO_READY"

type readyInfo struct {
	URL, User, Password, Data string
}

func parseReady(line string) (readyInfo, bool) {
	i := strings.Index(line, readyToken)
	if i < 0 {
		return readyInfo{}, false
	}
	rest := strings.TrimSpace(strings.TrimLeft(line[i+len(readyToken):], ":= \t"))
	kv := map[string]string{}
	if strings.HasPrefix(rest, "{") {
		var m map[string]any
		if json.Unmarshal([]byte(rest), &m) == nil {
			for k, v := range m {
				kv[strings.ToLower(k)] = fmt.Sprint(v)
			}
		}
	} else {
		for _, f := range splitFields(rest) {
			if k, v, ok := strings.Cut(f, "="); ok {
				if uq, err := strconv.Unquote(v); err == nil {
					v = uq
				}
				kv[strings.ToLower(k)] = v
			}
		}
	}
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v := kv[k]; v != "" {
				return v
			}
		}
		return ""
	}
	return readyInfo{
		URL:      pick("url", "addr", "listen", "base_url"),
		User:     pick("user", "username", "admin"),
		Password: pick("password", "pass", "admin_password"),
		Data:     pick("data", "data_dir", "datadir", "dir"),
	}, true
}

// splitFields splits on whitespace but keeps "quoted values" together.
func splitFields(s string) []string {
	var fields []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && inQuote && i+1 < len(s):
			cur.WriteByte(c)
			i++
			cur.WriteByte(s[i])
		case c == '"':
			inQuote = !inQuote
			cur.WriteByte(c)
		case (c == ' ' || c == '\t') && !inQuote:
			if cur.Len() > 0 {
				fields = append(fields, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		fields = append(fields, cur.String())
	}
	return fields
}

// banner prints the "open this URL, log in with ..." block once per session, after the API
// is up (READY line, or /healthz as a fallback) and, for dev, after Vite answers too.
type banner struct {
	openURL  string // what the human should open
	waitURL  string // extra URL that must answer first ("" = none)
	apiURL   string
	mu       sync.Mutex
	info     readyInfo
	printed  bool
	readyCh  chan struct{}
	readyOne sync.Once
}

func newBanner(openURL, waitURL string) *banner {
	return &banner{openURL: openURL, waitURL: waitURL, apiURL: "http://localhost:8080", readyCh: make(chan struct{})}
}

// filter is the api prefixWriter filter: it captures READY lines.
func (b *banner) filter(line string) bool {
	ri, ok := parseReady(line)
	if !ok {
		return false
	}
	b.mu.Lock()
	if ri.Password != "" && b.printed && ri.Password != b.info.Password {
		out.line(bold("  admin password: ") + ri.Password)
	}
	if ri.URL != "" {
		b.apiURL = ri.URL
	}
	if ri.User != "" {
		b.info.User = ri.User
	}
	if ri.Password != "" {
		b.info.Password = ri.Password
	}
	if ri.Data != "" {
		b.info.Data = ri.Data
	}
	b.mu.Unlock()
	b.readyOne.Do(func() { close(b.readyCh) })
	return true
}

// watch waits for readiness and prints the banner; it returns early when stop closes.
func (b *banner) watch(stop <-chan struct{}) {
	client := &http.Client{Timeout: time.Second}
	up := func(u string) bool {
		resp, err := client.Get(u)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return true
	}
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	healthySince := time.Time{}
wait:
	for {
		select {
		case <-stop:
			return
		case <-b.readyCh:
			break wait
		case <-tick.C:
			// Fallback for a server that does not print the READY line.
			if up("http://localhost:8080/healthz") {
				if healthySince.IsZero() {
					healthySince = time.Now()
				} else if time.Since(healthySince) > 3*time.Second {
					break wait
				}
			}
		}
	}
	if b.waitURL != "" {
		deadline := time.Now().Add(30 * time.Second)
		for !up(b.waitURL) && time.Now().Before(deadline) {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.printed {
		return
	}
	b.printed = true
	user, pass, data := b.info.User, b.info.Password, b.info.Data
	if user == "" {
		user = "admin"
	}
	if pass == "" {
		pass = dim("(unknown; delete " + orDefault(data, ".telesphorepo-dev") + " to start fresh)")
	}
	if data == "" {
		data = ".telesphorepo-dev"
	}
	rule := dim(strings.Repeat("─", 60))
	out.line("")
	out.line(rule)
	out.line("  " + bold("Telesphorepo is running"))
	out.line("")
	out.line("  open      " + green(bold(b.openURL)))
	if b.openURL != b.apiURL {
		out.line("  api       " + b.apiURL)
	}
	out.line("  username  " + bold(user))
	out.line("  password  " + bold(pass))
	out.line("  2fa       " + dim("not enforced (dev); add one under Settings > Account to try it"))
	out.line("  data      " + data + dim("  (delete it to start fresh)"))
	out.line("  clients   " + dim("Maven, Gradle, apt and curl setup: README.md"))
	out.line("")
	out.line("  " + dim("Ctrl+C to stop"))
	out.line(rule)
	out.line("")
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
