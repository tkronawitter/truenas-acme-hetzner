package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
// Deleting an RRSet returns a running action, and the RRSet only disappears
// once a client polls that action, as with the real asynchronous API.
type fakeHetzner struct {
	t   *testing.T
	srv *httptest.Server

	mu             sync.Mutex
	rrsets         map[string][]string // "<name>/<type>" -> record values
	pendingDeletes map[int64]string    // running delete action ID -> rrset key
	nextActionID   int64
	calls          []string                      // "METHOD /path" in order
	failAt         map[string]int                // call -> occurrence (1-based) answered with an API error
	lastCreate     schema.ZoneRRSetCreateRequest // body of the last create call
}

func newFakeHetzner(t *testing.T) *fakeHetzner {
	t.Helper()
	f := &fakeHetzner{
		t:              t,
		rrsets:         map[string][]string{},
		pendingDeletes: map[int64]string{},
		failAt:         map[string]int{},
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
			return hcloud.NewClient(
				hcloud.WithToken(token),
				hcloud.WithEndpoint(f.srv.URL),
				hcloud.WithRetryOpts(hcloud.RetryOpts{MaxRetries: 0}),
				hcloud.WithPollOpts(hcloud.PollOpts{BackoffFunc: hcloud.ConstantBackoff(time.Millisecond)}),
			)
		},
	}, stdout, stderr
}

// failOn makes the n-th occurrence of call (e.g. "DELETE /zones/42/rrsets/x/TXT") fail.
func (f *fakeHetzner) failOn(call string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failAt[call] = n
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

		call := r.Method + " " + r.URL.Path
		f.mu.Lock()
		f.calls = append(f.calls, call)
		n := 0
		for _, c := range f.calls {
			if c == call {
				n++
			}
		}
		fail := f.failAt[call] == n
		f.mu.Unlock()

		if fail {
			writeError(w, http.StatusUnprocessableEntity, "invalid_input", "injected failure")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, schema.ErrorResponse{Error: schema.Error{Code: code, Message: msg}})
}

// action returns a new action; the caller must hold f.mu.
func (f *fakeHetzner) action(command, status string) schema.Action {
	f.nextActionID++
	return schema.Action{ID: f.nextActionID, Command: command, Status: status}
}

// rrsetSchema renders a stored RRSet; the caller must hold f.mu.
func (f *fakeHetzner) rrsetSchema(name, typ string) schema.ZoneRRSet {
	rs := schema.ZoneRRSet{
		ID:   name + "/" + typ,
		Name: name,
		Type: typ,
		TTL:  hcloud.Ptr(txtTTL),
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
		Action: f.action("create_rrset", "running"),
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
	a := f.action("delete_rrset", "running")
	f.pendingDeletes[a.ID] = key
	writeJSON(w, http.StatusCreated, schema.ActionGetResponse{Action: a})
}

// listActions answers the action waiter's poll: every requested action is
// reported finished, and a finished delete takes effect now.
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
		if key, ok := f.pendingDeletes[id]; ok {
			delete(f.rrsets, key)
			delete(f.pendingDeletes, id)
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
	writeJSON(w, http.StatusCreated, schema.ActionGetResponse{Action: f.action(r.PathValue("action"), "running")})
}
