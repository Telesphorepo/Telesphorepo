package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/storage"
)

// seenReaders is how many seen/ files the job reads at once.
const seenReaders = 16

// seen is the job edge.seen: every minute it reads the seen/ files that the Worker writes (each
// holds the read tokens that one isolate served, see noteSeen in edge/src/access.ts), sets
// last_used_at of those tokens and deletes the files.
func (m *mod) seen(ctx context.Context) error {
	v := m.cur.Load()
	if v.mode != modeOn {
		return nil
	}
	for {
		objs, err := v.home.st.List(ctx, root+seenPrefix, "", 1000)
		if err != nil || len(objs) == 0 {
			return err
		}
		uses := make([]map[string]time.Time, len(objs))
		errs := make([]error, len(objs))
		sem := make(chan struct{}, seenReaders)
		var wg sync.WaitGroup
		for i, o := range objs {
			sem <- struct{}{}
			wg.Go(func() {
				defer func() { <-sem }()
				uses[i], errs[i] = readSeen(ctx, v.home.st, o)
			})
		}
		wg.Wait()
		last := map[string]time.Time{}
		keys := make([]string, 0, len(objs))
		for i, o := range objs {
			if errors.Is(errs[i], storage.ErrNotFound) {
				continue
			}
			if errs[i] != nil {
				return errs[i]
			}
			for id, at := range uses[i] {
				if at.After(last[id]) {
					last[id] = at
				}
			}
			keys = append(keys, o.Key)
		}
		for id, at := range last {
			if err := m.h.Auth.TouchTokens(ctx, []string{id}, at); err != nil {
				return err
			}
		}
		if len(keys) == 0 {
			return nil
		}
		if err := v.home.st.DeleteMany(ctx, keys); err != nil {
			return err
		}
		if len(objs) < 1000 {
			return nil
		}
	}
}

// readSeen reads one seen/ file, {version, tokens: [{id, at}]} with at in unix seconds, and
// returns the last use of each read token by public ID. A use without a time, or one in the
// future, counts at the time the file was written.
func readSeen(ctx context.Context, st storage.Store, o storage.ObjectInfo) (map[string]time.Time, error) {
	rc, _, err := st.Get(ctx, o.Key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(io.LimitReader(rc, 4<<20)); err != nil {
		return nil, err
	}
	now := time.Now()
	written := o.ModTime
	if written.IsZero() || written.After(now) {
		written = now
	}
	var f struct {
		Tokens []struct {
			ID string          `json:"id"`
			At json.RawMessage `json:"at"`
		} `json:"tokens"`
	}
	out := map[string]time.Time{}
	if json.Unmarshal(buf.Bytes(), &f) != nil {
		return out, nil // not a seen file of this version: dropped
	}
	for _, t := range f.Tokens {
		if !validPublicID(t.ID) {
			continue
		}
		at := unixOf(t.At)
		if at.IsZero() || at.After(now) {
			at = written
		}
		if at.After(out[t.ID]) {
			out[t.ID] = at
		}
	}
	return out, nil
}

// publicIDLen is the length of the public ID of a read token (hex digits, see internal/token).
const publicIDLen = 12

// validPublicID accepts the public ID of a read token.
func validPublicID(s string) bool {
	if len(s) != publicIDLen {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
