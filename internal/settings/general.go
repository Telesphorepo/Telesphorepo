package settings

import (
	"crypto/tls"
	"errors"
	"log/slog"
	"net/url"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
)

// HTTPURL refuses a value that is not empty and not an http:// or https:// URL.
func HTTPURL(v any) error {
	s := v.(string)
	if s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("must be an http:// or https:// URL")
	}
	return nil
}

// Trim is a Normalize that trims spaces.
func Trim(v any) any { return strings.TrimSpace(v.(string)) }

// Lower is a Normalize that trims spaces and lowers the case.
func Lower(v any) any { return strings.ToLower(strings.TrimSpace(v.(string))) }

// The settings of the group "general" that the kernel reads. The core module registers the
// group (General).
var (
	BaseURL = String(&Def{Key: "base_url", Label: "Public base URL", Dev: "http://localhost:8080",
		Help:      "Clients reach this server at this URL, for example https://repo.example.com. The UI uses it in client snippets.",
		Normalize: func(v any) any { return strings.TrimRight(strings.TrimSpace(v.(string)), "/") },
		Validate:  HTTPURL})
	LogLevel = String(&Def{Key: "log.level", Default: "info", Label: "Log level",
		Help:    "The server log keeps messages of this level and more important ones.",
		Options: []string{"debug", "info", "warn", "error"}, Normalize: Lower})
	MaxUploadSize = Size(&Def{Key: "max_upload_size", Default: "2GiB", Label: "Max upload size",
		Help: "The server refuses a bigger artifact or package upload, for example above 2GiB. It also caps one image layer."})
	MaxJSONSize = Size(&Def{Key: "max_json_size", Default: "1MiB", Label: "Max JSON body size",
		Help: "The server refuses a bigger JSON API request body, for example above 1MiB."})
	CookieSecure = Bool(&Def{Key: "cookie.secure", Default: "true", Dev: "false", Label: "Secure session cookie",
		Help: "The browser sends the session cookie only over HTTPS. Turn this off only for a plain-HTTP setup."})
	SessionTTL = Duration(&Def{Key: "session_ttl", Default: "168h", Label: "Session lifetime",
		Help: "After this time, a person signed in to the UI must sign in again."})
	IndexDebounce = Duration(&Def{Key: "index.debounce", Default: "2s", Dev: "300ms", Label: "Index rebuild delay",
		Help: "After a change, the server waits this long for more changes before it rebuilds the indexes of a repository (for example the Debian Packages files)."})
	IndexMaxDelay = Duration(&Def{Key: "index.max_delay", Default: "30s", Label: "Max index rebuild delay",
		Help: "While changes continue, the server rebuilds the indexes of a repository at least this often."})
	BlobsGCGrace = Duration(&Def{Key: "blobs.gc_grace", Default: "1h", Label: "Unused file grace period",
		Help: "The server deletes stored content that no file uses after this time. Content that a new upload needs within this time is not stored again."})
	UploadSessionTTL = Duration(&Def{Key: "upload.session_ttl", Default: "24h", Label: "Resumable upload lifetime",
		Help: "The server deletes a resumable upload that received no data for this time."})
	TLSCertFile = String(&Def{Key: "tls.cert_file", Label: "TLS certificate file", Normalize: Trim,
		Help: "The path of a PEM file with the certificate chain of the server. With a key file too, the server serves HTTPS with HTTP/2 itself. A renewed file applies at the next connection. Turning TLS on or off applies at the next start."})
	TLSKeyFile = String(&Def{Key: "tls.key_file", Label: "TLS key file", Normalize: Trim,
		Help: "The path of the PEM file with the private key of the TLS certificate. Turning TLS on or off applies at the next start."})
)

// validateGeneral checks the TLS files: both or neither, readable, and a pair that parses.
func validateGeneral(d *Draft, _ Mode) error {
	cert, key := TLSCertFile.Get(d.Next), TLSKeyFile.Get(d.Next)
	switch {
	case cert == "" && key == "":
		return nil
	case cert == "":
		d.Fail(TLSCertFile.Name(), "required when a TLS key file is set")
	case key == "":
		d.Fail(TLSKeyFile.Name(), "required when a TLS certificate file is set")
	default:
		if _, err := tls.LoadX509KeyPair(cert, key); err != nil {
			d.Fail(TLSCertFile.Name(), "the certificate and the key do not load: "+err.Error())
		}
	}
	return nil
}

// General returns the group "general": the settings of the kernel, edited with the permission
// settings.general on Settings > General.
func General() *Group {
	return &Group{ID: "general", Label: "General", Perm: perm.SettingsGeneral, Defs: []*Def{
		BaseURL.Def(), LogLevel.Def(), MaxUploadSize.Def(), MaxJSONSize.Def(), CookieSecure.Def(), SessionTTL.Def(),
		IndexDebounce.Def(), IndexMaxDelay.Def(), BlobsGCGrace.Def(), UploadSessionTTL.Def(), TLSCertFile.Def(), TLSKeyFile.Def(),
	}, Validate: validateGeneral}
}

// Level returns the log level of log.level.
func Level(s *Snapshot) slog.Level {
	switch LogLevel.Get(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

// TmpDir is the upload spool directory (on the data volume, not tmpfs).
func (s *Snapshot) TmpDir() string { return config.JoinDataDir(s.dataDir, "tmp") }
