package publish

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/publish/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// The setup of a project: a person makes a single-use setup code for one repo on its Publish tab
// (POST /api/publish/setup-codes), and "telesphorepo setup" sends the code with a new device key
// or with the proof of the device it has (POST /api/devices/setup). The code takes the place of
// the browser approval of the device flow. "telesphorepo logout" revokes the device with its own
// signature (DELETE /api/devices/self).

const (
	// setupPrefix starts the message that a device signs to set up a project with its key.
	setupPrefix = "telesphorepo setup v1\n"
	// revokePrefix starts the message that a device signs to revoke itself.
	revokePrefix = "telesphorepo revoke v1\n"
)

// SetupMessage is what a device signs with Ed25519 to set up a project: a nonce of Challenge
// and the device id.
func SetupMessage(nonce string, deviceID int64) []byte {
	return []byte(setupPrefix + nonce + "\n" + strconv.FormatInt(deviceID, 10))
}

// RevokeMessage is what a device signs with Ed25519 to revoke itself: a nonce of Challenge and
// the device id.
func RevokeMessage(nonce string, deviceID int64) []byte {
	return []byte(revokePrefix + nonce + "\n" + strconv.FormatInt(deviceID, 10))
}

// ProblemSetupCodeUsed is the answer to a setup code that is wrong, used or expired. The remedy
// opens the Publish tab of the repo of the code (the server root when the code is unknown).
var ProblemSetupCodeUsed = httpx.DeclareProblem(httpx.Problem{Name: "setup-code-used", Status: http.StatusUnauthorized,
	Title: "This setup line is already used", Remedy: &httpx.Remedy{Action: httpx.RemedyOpenURL}})

// SetupCodeError is a setup code that is wrong, used or expired. Repo is the repo of an expired
// code ("" when the code is unknown or used).
type SetupCodeError struct {
	Repo    string
	Expired bool
}

func (e *SetupCodeError) Error() string {
	switch {
	case e.Expired:
		return "This setup line expired. A setup line works for 10 minutes. Copy a new one from the Publish tab of " + e.Repo + "."
	case e.Repo != "":
		return "A setup line works once. Copy a new one from the Publish tab of " + e.Repo + "."
	}
	return "A setup line works once, for 10 minutes. Copy a new one from the Publish tab of the repository."
}

// SetupCode is the answer of POST /api/publish/setup-codes. Link carries the code in its
// fragment, so a browser that opens it never sends the code to the server.
type SetupCode struct {
	Code      string    `json:"code"`
	Link      string    `json:"link"`
	ExpiresAt time.Time `json:"expires_at"`
}

// SetupInput is a request of POST /api/devices/setup: the code with a new device key (Name and
// PublicKey), or with the proof of an enrolled device (DeviceID, and Signature over
// SetupMessage of a Challenge Nonce).
type SetupInput struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
	DeviceID  int64  `json:"device_id"`
	Nonce     string `json:"nonce"`
	Signature string `json:"signature"`
}

// SetupResult is the answer of POST /api/devices/setup: the device, its user, and the repo of the
// code with its format and kind.
type SetupResult struct {
	DeviceID int64  `json:"device_id"`
	User     string `json:"user"`
	Repo     string `json:"repo"`
	Format   string `json:"format"`
	Kind     string `json:"kind"`
}

// CreateSetupCode makes a setup code of p for repo and records setup_code.create. The route
// policy checked the fresh second factor and that p may mint for repo.
func (s *Service) CreateSetupCode(ctx context.Context, p *auth.Principal, repo *repos.Repo) (string, time.Time, error) {
	code, exp, err := s.auth.IssueSetupCode(ctx, p.UserID, repo.ID)
	if err != nil {
		return "", time.Time{}, err
	}
	_ = s.audit.Record(ctx, audit.Event{Action: ActionSetupCodeCreate, RepoID: repo.ID, Repo: repo.Name,
		Detail: map[string]any{"expires_at": exp.Unix()}})
	return code, exp, nil
}

// SetupDevice takes the setup code of in and enrolls the new device key of in for the person who
// made the code, or checks the proof of the enrolled device of that person. It records
// device.setup as that person. A failure after the code is taken puts the code back, unless the
// code expired. Errors: *SetupCodeError, *RefusedError, ErrInvalid, ErrDenied, ErrConflict.
func (s *Service) SetupDevice(ctx context.Context, r *http.Request, in SetupInput) (*SetupResult, error) {
	newKey := in.PublicKey != ""
	if newKey == (in.DeviceID != 0) {
		return nil, errorf(ErrInvalid, "send a new device (name and public_key) or the proof of an enrolled device (device_id, nonce and signature)")
	}
	var name string
	var key []byte
	var d dbq.GetDeviceRow
	var err error
	if newKey {
		if name, key, err = deviceInput(in.Name, in.PublicKey); err != nil {
			return nil, err
		}
	} else if d, err = s.takeDeviceProof(ctx, in.DeviceID, in.Nonce, in.Signature, SetupMessage(in.Nonce, in.DeviceID)); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(in.Code, auth.SetupCodePrefix) {
		return nil, &SetupCodeError{}
	}
	sc, err := s.auth.TakeSetupCode(ctx, in.Code)
	if errors.Is(err, auth.ErrNotFound) {
		return nil, &SetupCodeError{}
	}
	if err != nil {
		return nil, err
	}
	repo, err := s.repos.ByID(ctx, sc.RepoID)
	if errors.Is(err, repos.ErrNotFound) {
		return nil, &SetupCodeError{}
	}
	if err != nil {
		return nil, err
	}
	if sc.Expired(time.Now()) {
		return nil, &SetupCodeError{Repo: repo.Name, Expired: true}
	}
	res, err := s.setupWith(ctx, r, sc, repo, name, key, d, newKey)
	if err != nil {
		if rerr := s.auth.RestoreSetupCode(ctx, in.Code, sc); rerr != nil {
			s.log.WarnContext(ctx, "put a setup code back", "repo", repo.Name, "error", rerr.Error())
		}
		return nil, err
	}
	return res, nil
}

// setupWith enrolls or checks the device for the setup code sc of repo.
func (s *Service) setupWith(ctx context.Context, r *http.Request, sc *auth.SetupCode, repo *repos.Repo, name string, key []byte, d dbq.GetDeviceRow, newKey bool) (*SetupResult, error) {
	u, err := s.auth.UserPrincipal(ctx, sc.UserID)
	if errors.Is(err, auth.ErrBadCredentials) {
		return nil, errorf(ErrDenied, "the account of this setup line is disabled")
	}
	if err != nil {
		return nil, err
	}
	if !newKey && d.UserID != u.UserID {
		return nil, errorf(ErrConflict, "the device of this computer belongs to "+d.Username+", and the setup line to "+u.Username+
			"; run telesphorepo logout for this server first")
	}
	ctx = audit.WithActor(ctx, audit.Actor{Kind: audit.ActorUser, UserID: u.UserID, Name: u.Username, IP: audit.ActorOf(ctx).IP})
	if dec := s.access.Decide(ctx, r, u, repo, access.Mint, perm.RepoPublish); !dec.Allowed {
		return nil, &RefusedError{dec}
	}
	res := &SetupResult{User: u.Username, Repo: repo.Name, Format: repo.Format, Kind: repo.Kind}
	now := time.Now()
	if !newKey {
		res.DeviceID = d.ID
		if err := s.wq.TouchDevice(ctx, dbq.TouchDeviceParams{LastUsedAt: nullID(now.Unix()), ID: d.ID}); err != nil {
			return nil, err
		}
		_ = s.audit.Record(ctx, audit.Event{Action: ActionDeviceSetup, RepoID: repo.ID, Repo: repo.Name, Target: d.Name,
			Detail: map[string]any{"device_id": d.ID, "new": false}})
		return res, nil
	}
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		id, err := q.CreateDevice(ctx, dbq.CreateDeviceParams{UserID: u.UserID, Name: name, PublicKey: key,
			CreatedAt: now.Unix(), ExpiresAt: now.Add(s.deviceTTL()).Unix()})
		if isUnique(err) {
			return errorf(ErrConflict, "this device key is already enrolled")
		}
		if err != nil {
			return err
		}
		res.DeviceID = id
		return s.audit.RecordTx(ctx, tx, audit.Event{Action: ActionDeviceSetup, RepoID: repo.ID, Repo: repo.Name, Target: name,
			Detail: map[string]any{"device_id": id, "new": true}})
	})
	if err != nil {
		return nil, err
	}
	s.deviceChanged(ctx, res.DeviceID)
	return res, nil
}

// takeDeviceProof takes the one-request nonce of Challenge for device deviceID and checks that
// the device is usable and that its signature over msg verifies.
func (s *Service) takeDeviceProof(ctx context.Context, deviceID int64, nonce, signature string, msg []byte) (dbq.GetDeviceRow, error) {
	now := time.Now().Unix()
	if deviceID <= 0 || nonce == "" || signature == "" {
		return dbq.GetDeviceRow{}, errorf(ErrInvalid, "device_id, nonce and signature are required")
	}
	id, err := s.wq.TakeNonce(ctx, dbq.TakeNonceParams{IDHash: hashBytes([]byte(nonce)), Now: now})
	if errors.Is(err, sql.ErrNoRows) || (err == nil && id != deviceID) {
		return dbq.GetDeviceRow{}, errorf(ErrDenied, "invalid or expired nonce")
	}
	if err != nil {
		return dbq.GetDeviceRow{}, err
	}
	d, err := s.usableDevice(ctx, deviceID, now)
	if err != nil {
		return d, err
	}
	sig, err := decodeB64URL(signature)
	if err != nil || len(d.PublicKey) != ed25519.PublicKeySize || !ed25519.Verify(ed25519.PublicKey(d.PublicKey), msg, sig) {
		return d, errorf(ErrDenied, "bad device signature")
	}
	return d, nil
}

// RevokeSelf revokes the device of a signature over RevokeMessage of a Challenge nonce, ends its
// open sessions and records device.revoke as the device's user.
func (s *Service) RevokeSelf(ctx context.Context, deviceID int64, nonce, signature string) error {
	d, err := s.takeDeviceProof(ctx, deviceID, nonce, signature, RevokeMessage(nonce, deviceID))
	if err != nil {
		return err
	}
	ctx = audit.WithActor(ctx, audit.Actor{Kind: audit.ActorUser, UserID: d.UserID, Name: d.Username, IP: audit.ActorOf(ctx).IP})
	now := nullID(time.Now().Unix())
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		n, err := q.RevokeDevice(ctx, dbq.RevokeDeviceParams{RevokedAt: now, ID: d.ID})
		if err != nil {
			return err
		}
		if n == 0 {
			return errorf(ErrDenied, "device revoked; enroll again")
		}
		if _, err = q.CloseDevicePublishSessions(ctx, dbq.CloseDevicePublishSessionsParams{ClosedAt: now, DeviceID: nullID(d.ID)}); err != nil {
			return err
		}
		return s.audit.RecordTx(ctx, tx, audit.Event{Action: ActionDeviceRevoke, Target: strconv.FormatInt(d.ID, 10),
			Detail: map[string]any{"device_id": d.ID, "by": "device"}})
	})
	if err == nil {
		s.deviceChanged(ctx, d.ID)
	}
	return err
}

// --- routes ---

// maxRepoBody bounds the body that bodyRepo reads.
const maxRepoBody = 64 << 10

// bodyRepo is the repo name of a JSON body {"repo": ...} (route.RepoRule Name). It reads the body
// and puts it back for the handler.
func bodyRepo(r *http.Request) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxRepoBody+1))
	if err != nil {
		return "", errors.New("the request body cannot be read")
	}
	if len(b) > maxRepoBody {
		return "", errors.New("the request body is too large")
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	var in struct {
		Repo string `json:"repo"`
	}
	if json.Unmarshal(b, &in) != nil || in.Repo == "" {
		return "", errors.New(`the body must be a JSON object with "repo"`)
	}
	return in.Repo, nil
}

// createSetupCode makes a setup code for the repo of the body (UI session, fresh second factor,
// repo.publish): {repo} -> 201 {code, link, expires_at}.
func (m *mod) createSetupCode(w http.ResponseWriter, r *http.Request, c *route.Call) {
	var in struct {
		Repo string `json:"repo"`
	}
	if !m.h.API.Decode(w, r, &in) {
		return
	}
	if c.Proof != nil && c.Proof.Kind == "grant" && c.Proof.Data != SetupGrant(c.Repo.Name) {
		httpx.Error(w, r, http.StatusUnauthorized, "the single sign-on confirmation is for another repository; sign in again")
		return
	}
	code, exp, err := m.svc.CreateSetupCode(r.Context(), c.P, c.Repo)
	if err != nil {
		publishFail(w, r, err)
		return
	}
	link := strings.TrimSuffix(m.h.API.BaseURL(r), "/") + "/#/setup/" + code
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusCreated, SetupCode{Code: code, Link: link, ExpiresAt: exp})
}

// SetupGrant is what the grant of a fresh SSO sign-in confirms when it makes a setup code for
// repo (auth.Proof.Data).
func SetupGrant(repo string) string { return "setup:" + repo }

// setupDevice sets up a project with a setup code (no auth): {code, name, public_key} or {code,
// device_id, nonce, signature} -> 200 {device_id, user, repo, format, kind}. A wrong, used or
// expired code answers 401, the problem setup-code-used.
func (m *mod) setupDevice(w http.ResponseWriter, r *http.Request, c *route.Call) {
	var in SetupInput
	if !m.h.API.Decode(w, r, &in) || m.blocked(w, r) {
		return
	}
	res, err := m.svc.SetupDevice(r.Context(), r, in)
	var codeErr *SetupCodeError
	var refused *RefusedError
	switch {
	case errors.As(err, &codeErr):
		m.h.Auth.Failed(r, "")
		d := ProblemSetupCodeUsed.Doc(codeErr.Error())
		base := strings.TrimSuffix(m.h.API.BaseURL(r), "/")
		d.Remedy.URL = base + "/"
		if codeErr.Repo != "" {
			d.Remedy.URL = base + "/#/repo/" + url.PathEscape(codeErr.Repo)
		}
		httpx.WriteProblem(w, r, d)
	case errors.As(err, &refused):
		route.Refuse(w, r, refused.D)
	case err != nil:
		if errors.Is(err, ErrDenied) {
			m.h.Auth.Failed(r, "")
		}
		publishFail(w, r, err)
	default:
		httpx.WriteJSON(w, http.StatusOK, res)
	}
}

// revokeSelf revokes the device that signs the request (no auth): {device_id, nonce, signature}
// with the signature over RevokeMessage -> 204.
func (m *mod) revokeSelf(w http.ResponseWriter, r *http.Request, c *route.Call) {
	var in struct {
		DeviceID  int64  `json:"device_id"`
		Nonce     string `json:"nonce"`
		Signature string `json:"signature"`
	}
	if !m.h.API.Decode(w, r, &in) || m.blocked(w, r) {
		return
	}
	if err := m.svc.RevokeSelf(r.Context(), in.DeviceID, in.Nonce, in.Signature); err != nil {
		if errors.Is(err, ErrDenied) {
			m.h.Auth.Failed(r, "")
		}
		publishFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// exchangeSession mints a session from a device proof of a token exchange nonce (no auth):
// {proof: "trx.<device id>.<nonce>.<signature>", repo} -> 201 {id, token, repo, format, kind,
// expires_at}. One nonce mints one session per repo; a later call with the same proof and repo
// answers that session again while it is open.
func (m *mod) exchangeSession(w http.ResponseWriter, r *http.Request, c *route.Call) {
	var in struct {
		Proof string `json:"proof"`
		Repo  string `json:"repo"`
	}
	if !m.h.API.Decode(w, r, &in) || m.blocked(w, r) {
		return
	}
	proof := ParseDeviceProof(in.Proof)
	if proof == nil {
		m.h.Auth.Failed(r, "")
		publishFail(w, r, errorf(ErrDenied, `proof must be "trx.<device id>.<nonce>.<signature>"`))
		return
	}
	mt, err := m.svc.Exchange(r.Context(), r, proof, in.Repo)
	var refused *RefusedError
	switch {
	case errors.As(err, &refused):
		route.Refuse(w, r, refused.D)
	case err != nil:
		if errors.Is(err, ErrDenied) {
			m.h.Auth.Failed(r, "")
		}
		publishFail(w, r, err)
	default:
		httpx.WriteJSON(w, http.StatusCreated, mt)
	}
}

// blocked answers 429 and returns true when the client address made too many failed attempts.
func (m *mod) blocked(w http.ResponseWriter, r *http.Request) bool {
	d := m.h.Auth.Blocked(r)
	if d <= 0 {
		return false
	}
	w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(d.Seconds())))))
	httpx.Error(w, r, http.StatusTooManyRequests, auth.ErrRateLimited.Error())
	return true
}

// baseURL is the base_url setting, else the URL the client reached the server at, without a
// trailing slash.
func (m *mod) baseURL(r *http.Request) string {
	if base := strings.TrimRight(settings.BaseURL.Get(m.h.Settings.Current()), "/"); base != "" {
		return base
	}
	return httpx.ClientScheme(r) + "://" + r.Host
}
