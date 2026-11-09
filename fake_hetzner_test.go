package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/hetznercloud/hcloud-go/v2/hcloud/schema"
)

const (
	fakeToken  = "test-token"
	fakeZone   = "example.com"
	fakeZoneID = 42
)

// fakeHetzner is an in-memory stand-in for the Hetzner Cloud zone/RRSet API
// with a single zone, example.com. It serves the real hcloud-go client, so the
// tests cover the request paths and bodies the binary actually sends.
//
// Every change returns a running action that only finishes once a client
// polls it, as with the real asynchronous API. A deleted RRSet disappears at
// that point, and while an action on an RRSet runs, adding or removing records
// is rejected as locked. A command that does not wait for its action leaves it
// running, which unfinished reports and a following command runs into.
type fakeHetzner struct {
	t   *testing.T
	srv *httptest.Server

	mu           sync.Mutex
	rrsets       map[string][]string // "<name>/<type>" -> record values
	running      map[int64]string    // running action ID -> rrset key
	deletes      map[int64]bool      // running action IDs that delete their rrset
	nextActionID int64
	calls        []string                      // "METHOD /path" in order
	userAgent    string                        // User-Agent header of the last request
	failAt       map[string]failure            // call -> how one occurrence of it fails
	hangOn       string                        // call whose first occurrence never answers until the client gives up
	failActions  bool                          // polled actions finish with status error
	lastCreate   schema.ZoneRRSetCreateRequest // body of the last create call
}

func newFakeHetzner(t *testing.T) *fakeHetzner {
	t.Helper()
	f := &fakeHetzner{
		t:       t,
		rrsets:  map[string][]string{},
		running: map[int64]string{},
		deletes: map[int64]bool{},
		failAt:  map[string]failure{},
	}

	zone := "/zones/" + strconv.Itoa(fakeZoneID)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /zones/{zone}", f.getZone)
	mux.HandleFunc("GET "+zone+"/rrsets/{name}/{type}", f.getRRSet)
	mux.HandleFunc("POST "+zone+"/rrsets", f.createRRSet)
	mux.HandleFunc("DELETE "+zone+"/rrsets/{name}/{type}", f.deleteRRSet)
	mux.HandleFunc("POST "+zone+"/rrsets/{name}/{type}/actions/{action}", f.rrsetAction)
	mux.HandleFunc("GET /actions", f.listActions)

	f.srv = httptest.NewServer(f.middleware(mux))
	t.Cleanup(f.srv.Close)
	return f
}

// app returns an app wired to the fake, with a valid token file in a temp dir.
func (f *fakeHetzner) app() (app, *bytes.Buffer, *bytes.Buffer) {
	f.t.Helper()
	tokenPath := filepath.Join(f.t.TempDir(), ".tahtoken")
	if err := os.WriteFile(tokenPath, []byte(fakeToken+"\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}

	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	return app{
		stdout:    stdout,
		stderr:    stderr,
		tokenPath: tokenPath,
		newClient: func(token string) *hcloud.Client {
			// The production options first, so the fake sees the real User-Agent
			return hcloud.NewClient(append(clientOptions(token),
				hcloud.WithEndpoint(f.srv.URL),
				hcloud.WithRetryOpts(hcloud.RetryOpts{MaxRetries: 0}),
				hcloud.WithPollOpts(hcloud.PollOpts{BackoffFunc: hcloud.ConstantBackoff(time.Millisecond)}),
			)...)
		},
		timeout:     10 * time.Second,
		retryBudget: 10 * time.Second,
		attempts:    retryAttempts,
		retryDelay:  time.Millisecond,
	}, stdout, stderr
}

// failure describes how the n-th occurrence (1-based) of a call fails, or
// every occurrence for n == 0.
type failure struct {
	n      int
	status int    // HTTP status of the answer
	code   string // API error code; empty answers with a plain text body
	drop   drop   // instead of an answer, the connection is reset
}

type drop int

const (
	// dropBefore resets the connection before the request is handled. The
	// zero value answers normally.
	dropBefore drop = iota + 1
	// dropAfter handles the request, then resets the connection instead of
	// answering, as when a response is lost on the way back.
	dropAfter
)

// unfinished returns the IDs of actions no client has waited for.
func (f *fakeHetzner) unfinished() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []int64
	for id := range f.running {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// locked reports whether an action on key is still running; the caller must hold f.mu.
func (f *fakeHetzner) locked(key string) bool {
	for _, k := range f.running {
		if k == key {
			return true
		}
	}
	return false
}

// failOn makes the n-th occurrence of call (e.g. "DELETE /zones/42/rrsets/x/TXT")
// fail with a validation error, which is final.
func (f *fakeHetzner) failOn(call string, n int) {
	f.failWith(call, failure{n: n, status: http.StatusUnprocessableEntity, code: "invalid_input"})
}

func (f *fakeHetzner) failWith(call string, fl failure) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failAt[call] = fl
}

// count returns how often call was made.
func (f *fakeHetzner) count(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == call {
			n++
		}
	}
	return n
}

func (f *fakeHetzner) addRecords(name string, values ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rrsets[name+"/TXT"] = append(f.rrsets[name+"/TXT"], values...)
}

// records returns the values of a TXT RRSet and whether it exists.
func (f *fakeHetzner) records(name string) ([]string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.rrsets[name+"/TXT"]
	return slices.Clone(v), ok
}

func (f *fakeHetzner) called(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.calls, call)
}

func (f *fakeHetzner) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+fakeToken {
			f.t.Errorf("Authorization header = %q, want Bearer token from token file", got)
		}
		if want := "truenas-acme-hetzner/" + Version + " hcloud-go/"; !strings.HasPrefix(r.Header.Get("User-Agent"), want) {
			f.t.Errorf("User-Agent header = %q, want prefix %q", r.Header.Get("User-Agent"), want)
		}

		call := r.Method + " " + r.URL.Path
		f.mu.Lock()
		f.userAgent = r.Header.Get("User-Agent")
		f.calls = append(f.calls, call)
		n := 0
		for _, c := range f.calls {
			if c == call {
				n++
			}
		}
		fl, fail := f.failAt[call]
		fail = fail && (fl.n == n || fl.n == 0)
		hang := f.hangOn == call && n == 1
		f.mu.Unlock()

		switch {
		case hang:
			<-r.Context().Done()
		case fail && fl.drop == dropBefore:
			f.reset(w)
		case fail && fl.drop == dropAfter:
			next.ServeHTTP(httptest.NewRecorder(), r)
			f.reset(w)
		case fail && fl.code == "":
			http.Error(w, "injected failure", fl.status)
		case fail:
			writeError(w, fl.status, fl.code, "injected failure")
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// reset closes the connection of w with a TCP reset and without an answer.
func (f *fakeHetzner) reset(w http.ResponseWriter) {
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		f.t.Errorf("hijack: %v", err)
		return
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetLinger(0)
	}
	_ = conn.Close()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, schema.ErrorResponse{Error: schema.Error{Code: code, Message: msg}})
}

// action returns a new running action on the rrset key; the caller must hold f.mu.
func (f *fakeHetzner) action(command, key string) schema.Action {
	f.nextActionID++
	f.running[f.nextActionID] = key
	return schema.Action{ID: f.nextActionID, Command: command, Status: "running"}
}

// rrsetSchema renders a stored RRSet; the caller must hold f.mu.
func (f *fakeHetzner) rrsetSchema(name, typ string) schema.ZoneRRSet {
	rs := schema.ZoneRRSet{
		ID:   name + "/" + typ,
		Name: name,
		Type: typ,
		TTL:  new(txtTTL),
		Zone: fakeZoneID,
	}
	for _, v := range f.rrsets[name+"/"+typ] {
		rs.Records = append(rs.Records, schema.ZoneRRSetRecord{Value: v})
	}
	return rs
}

func (f *fakeHetzner) getZone(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("zone") != fakeZone {
		writeError(w, http.StatusNotFound, "not_found", "zone not found")
		return
	}
	writeJSON(w, http.StatusOK, schema.ZoneGetResponse{Zone: schema.Zone{ID: fakeZoneID, Name: fakeZone}})
}

func (f *fakeHetzner) getRRSet(w http.ResponseWriter, r *http.Request) {
	name, typ := r.PathValue("name"), r.PathValue("type")
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rrsets[name+"/"+typ]; !ok {
		writeError(w, http.StatusNotFound, "not_found", "rrset not found")
		return
	}
	writeJSON(w, http.StatusOK, schema.ZoneRRSetGetResponse{RRSet: f.rrsetSchema(name, typ)})
}

func (f *fakeHetzner) createRRSet(w http.ResponseWriter, r *http.Request) {
	var req schema.ZoneRRSetCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastCreate = req
	key := req.Name + "/" + req.Type
	if _, ok := f.rrsets[key]; ok {
		writeError(w, http.StatusConflict, "uniqueness_error", "rrset already exists")
		return
	}
	f.rrsets[key] = []string{}
	for _, rec := range req.Records {
		f.rrsets[key] = append(f.rrsets[key], rec.Value)
	}
	writeJSON(w, http.StatusCreated, schema.ZoneRRSetCreateResponse{
		RRSet:  f.rrsetSchema(req.Name, req.Type),
		Action: f.action("create_rrset", key),
	})
}

func (f *fakeHetzner) deleteRRSet(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("name") + "/" + r.PathValue("type")
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rrsets[key]; !ok {
		writeError(w, http.StatusNotFound, "not_found", "rrset not found")
		return
	}
	a := f.action("delete_rrset", key)
	f.deletes[a.ID] = true
	writeJSON(w, http.StatusCreated, schema.ActionGetResponse{Action: a})
}

// listActions answers the action waiter's poll: every requested action is
// reported finished (or failed with failActions), and a finished delete takes
// effect now.
func (f *fakeHetzner) listActions(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var resp schema.ActionListResponse
	for _, raw := range r.URL.Query()["id"] {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
			return
		}
		key, ok := f.running[id]
		if !ok {
			f.t.Errorf("poll for unknown or already finished action %d", id)
			continue
		}
		delete(f.running, id)

		if f.failActions {
			resp.Actions = append(resp.Actions, schema.Action{
				ID:     id,
				Status: "error",
				Error:  &schema.ActionError{Code: "action_failed", Message: "injected action failure"},
			})
			continue
		}
		if f.deletes[id] {
			delete(f.rrsets, key)
			delete(f.deletes, id)
		}
		resp.Actions = append(resp.Actions, schema.Action{ID: id, Status: "success"})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (f *fakeHetzner) rrsetAction(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("name") + "/" + r.PathValue("type")
	var req struct {
		Records []schema.ZoneRRSetRecord `json:"records"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rrsets[key]; !ok {
		writeError(w, http.StatusNotFound, "not_found", "rrset not found")
		return
	}
	if f.locked(key) {
		writeError(w, http.StatusLocked, "locked", "rrset is locked by a running action")
		return
	}

	switch r.PathValue("action") {
	case "add_records":
		for _, rec := range req.Records {
			f.rrsets[key] = append(f.rrsets[key], rec.Value)
		}
	case "remove_records":
		for _, rec := range req.Records {
			f.rrsets[key] = slices.DeleteFunc(f.rrsets[key], func(v string) bool { return v == rec.Value })
		}
	default:
		writeError(w, http.StatusNotFound, "not_found", "unknown action")
		return
	}
	writeJSON(w, http.StatusCreated, schema.ActionGetResponse{Action: f.action(r.PathValue("action"), key)})
}
