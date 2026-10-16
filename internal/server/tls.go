package server

import (
	"crypto/tls"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// certLoader gives the TLS listener the certificate of the settings tls.cert_file and
// tls.key_file. It keeps the loaded pair until a path or the modification time of a file changes,
// so a renewed certificate applies at the next connection without a restart.
type certLoader struct {
	set settings.Provider

	mu                sync.Mutex
	certPath, keyPath string
	certMod, keyMod   time.Time
	cert              *tls.Certificate
}

// get is the tls.Config GetCertificate of the server.
func (l *certLoader) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	snap := l.set()
	certPath, keyPath := settings.TLSCertFile.Get(snap), settings.TLSKeyFile.Get(snap)
	if certPath == "" || keyPath == "" {
		return nil, errors.New("tls: no certificate is set")
	}
	certInfo, err := os.Stat(certPath)
	if err != nil {
		return nil, err
	}
	keyInfo, err := os.Stat(keyPath)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cert != nil && l.certPath == certPath && l.keyPath == keyPath &&
		l.certMod.Equal(certInfo.ModTime()) && l.keyMod.Equal(keyInfo.ModTime()) {
		return l.cert, nil
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		if l.cert != nil {
			return l.cert, nil // a file in the middle of a renewal: the last pair serves
		}
		return nil, err
	}
	l.certPath, l.keyPath, l.certMod, l.keyMod, l.cert = certPath, keyPath, certInfo.ModTime(), keyInfo.ModTime(), &pair
	return l.cert, nil
}
