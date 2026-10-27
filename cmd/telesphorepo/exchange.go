package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// exchangeFile is exchange.json next to the device file of an account: the token exchange nonces that the
// helper gave, one per process that ran it, e.g.
//
//	{"nonces": [{"pid": 4242, "start": "1787000000123", "nonce": "<nonce>",
//	             "expires_at": "2026-10-16T10:15:00Z"}]}
//
// A client such as Helm or ORAS runs "get" again for each token that it fetches. The helper gives
// the same process the same nonce while the nonce is valid, so all token requests of one push get
// the same publish session. Only the owner can read the file.
type exchangeFile struct {
	Nonces []exchangeNonce `json:"nonces"`
}

// exchangeNonce is the nonce of one process.
type exchangeNonce struct {
	parentProcess
	Nonce     string    `json:"nonce"`
	ExpiresAt time.Time `json:"expires_at"`
}

// parentProcess names the process that runs the helper: its id and its start time ("" when the
// system does not tell it). A process that gets the id of a process that ended has another start
// time.
type parentProcess struct {
	PID   int    `json:"pid"`
	Start string `json:"start"`
}

func currentParent() parentProcess {
	pid := os.Getppid()
	return parentProcess{PID: pid, Start: processStart(pid)}
}

// loadExchange reads exchange.json in dir. A file that is missing or not valid is empty.
func loadExchange(dir string) exchangeFile {
	var x exchangeFile
	if b, err := os.ReadFile(filepath.Join(dir, "exchange.json")); err == nil {
		_ = json.Unmarshal(b, &x)
	}
	return x
}

// nonceOf returns the nonce of process p that is valid at now, or "".
func (x exchangeFile) nonceOf(p parentProcess, now time.Time) string {
	for _, n := range x.Nonces {
		if n.parentProcess == p && n.Nonce != "" && now.Before(n.ExpiresAt) {
			return n.Nonce
		}
	}
	return ""
}

// saveExchange writes the nonce of process p to exchange.json in dir, and drops the nonce that
// p had before and each nonce that expired.
func saveExchange(dir string, p parentProcess, nonce string, expires time.Time) error {
	x := loadExchange(dir)
	now := time.Now()
	x.Nonces = slices.DeleteFunc(x.Nonces, func(n exchangeNonce) bool {
		return n.parentProcess == p || !now.Before(n.ExpiresAt)
	})
	x.Nonces = append(x.Nonces, exchangeNonce{parentProcess: p, Nonce: nonce, ExpiresAt: expires})
	b, err := json.MarshalIndent(x, "", "  ")
	if err != nil {
		return err
	}
	return writeOwnerOnly(filepath.Join(dir, "exchange.json"), append(b, '\n'))
}
