package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"golang.org/x/oauth2"
)

// setTestManager puts an account on the manager list for the test
func setTestManager(t *testing.T, accountID, role string) {
	t.Helper()
	managersLock.Lock()
	managers[accountID] = &Manager{AccountID: accountID, DisplayName: accountID, Role: role}
	managersLock.Unlock()
	t.Cleanup(func() {
		managersLock.Lock()
		delete(managers, accountID)
		managersLock.Unlock()
	})
}

// sdmJira is a fake Jira holding one worklog, ABC-1 worklog 100 (09:00 UTC
// for 90 minutes, by "target" unless author is changed). Any "METHOD path"
// in fail is answered with that status instead; permissions in missingPerms
// are reported as not held. New worklogs get ID 200.
type sdmJira struct {
	author       string
	missingPerms map[string]bool
	fail         map[string]int
}

var testWorklogComment = map[string]interface{}{
	"type": "doc", "version": 1,
	"content": []interface{}{
		map[string]interface{}{"type": "paragraph", "content": []interface{}{map[string]interface{}{"type": "text", "text": "Original note"}}},
		map[string]interface{}{"type": "paragraph", "content": []interface{}{map[string]interface{}{"type": "text", "text": "Second paragraph"}}},
	},
}

func (f *sdmJira) handle(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
	path := r.URL.Path
	if status, ok := f.fail[r.Method+" "+path]; ok {
		writeTestJSON(w, status, map[string]interface{}{"errorMessages": []string{"injected failure"}})
		return
	}

	switch {
	case r.Method == http.MethodGet && path == "/mypermissions":
		perms := map[string]interface{}{}
		for _, p := range strings.Split(r.URL.Query().Get("permissions"), ",") {
			perms[p] = map[string]interface{}{"havePermission": !f.missingPerms[p]}
		}
		writeTestJSON(w, http.StatusOK, map[string]interface{}{"permissions": perms})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/worklog/100"):
		writeTestJSON(w, http.StatusOK, map[string]interface{}{
			"id":               "100",
			"author":           map[string]interface{}{"accountId": f.author},
			"started":          "2026-10-13T09:00:00.000+0000",
			"timeSpentSeconds": 5400,
			"comment":          testWorklogComment,
			"properties":       []interface{}{map[string]interface{}{"key": worklogSourcePropertyKey, "value": map[string]interface{}{"created_by": "jiratime"}}},
		})
	case r.Method == http.MethodPut && strings.Contains(path, "/properties/"):
		writeTestJSON(w, http.StatusOK, map[string]interface{}{})
	case r.Method == http.MethodPut && strings.Contains(path, "/worklog/"):
		writeTestJSON(w, http.StatusOK, map[string]interface{}{"id": path[strings.LastIndex(path, "/")+1:]})
	case r.Method == http.MethodDelete && strings.Contains(path, "/worklog/"):
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/worklog"):
		writeTestJSON(w, http.StatusCreated, map[string]interface{}{"id": "200"})
	default:
		writeTestJSON(w, http.StatusNotFound, map[string]interface{}{})
	}
}

// newSDMTest signs in "sdm" (on the manager list with the given role, if
// any) viewing "target"'s calendar, with saved tokens for the given accounts.
// It runs in a temporary directory so the audit log can be read back.
func newSDMTest(t *testing.T, role string, tokens map[string]string) (*sdmJira, *mockJira, func(method, target, body string) *httptest.ResponseRecorder) {
	t.Helper()
	if role != "" {
		setTestManager(t, "sdm", role)
	}
	session := &UserSession{AccountID: "sdm", DisplayName: "Sam SDM", CloudID: "cloud", ImpersonatingID: "target", ImpersonatingName: "Tara Target"}
	return newWriteTest(t, session, tokens)
}

// newOwnCalendarTest signs in "target", the author of the fake's worklog, on
// their own calendar
func newOwnCalendarTest(t *testing.T) (*sdmJira, *mockJira, func(method, target, body string) *httptest.ResponseRecorder) {
	t.Helper()
	session := &UserSession{AccountID: "target", DisplayName: "Tara Target", CloudID: "cloud"}
	return newWriteTest(t, session, map[string]string{"target": "target-access"})
}

// newWriteTest signs in the given session, with saved tokens for the given
// accounts, against an sdmJira
func newWriteTest(t *testing.T, session *UserSession, tokens map[string]string) (*sdmJira, *mockJira, func(method, target, body string) *httptest.ResponseRecorder) {
	t.Helper()
	t.Chdir(t.TempDir())
	viper.Set("SESSION_SECRET", "test-secret-that-is-at-least-32-characters")
	viper.Set("BASE_URL", "https://jiratime.example")

	store := testTokenStore(t, nil)
	for account, access := range tokens {
		store.set(account, &oauth2.Token{AccessToken: access, RefreshToken: "r", Expiry: time.Now().Add(time.Hour)})
	}
	original := accountTokens
	accountTokens = store
	t.Cleanup(func() { accountTokens = original })

	fake := &sdmJira{author: "target", missingPerms: map[string]bool{}, fail: map[string]int{}}
	mock, _ := newMockJira(t, fake.handle)

	sessionID := createSession(session)
	t.Cleanup(func() { deleteSession(sessionID) })
	t.Cleanup(func() { cache.InvalidateAll(session.AccountID) })

	do := func(method, target, body string) *httptest.ResponseRecorder {
		handler := handleEventByID
		if target == "/api/events" {
			handler = handleEvents
		}
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: sessionID + "." + signSession(sessionID)})
		req.AddCookie(&http.Cookie{Name: accountIDCookie, Value: session.AccountID + "." + signSession(session.AccountID)})
		rec := httptest.NewRecorder()
		requireAuth(handler)(rec, req)
		return rec
	}
	return fake, mock, do
}

// writesTo returns the non-GET requests Jira received, as "METHOD path"
func writesTo(mock *mockJira) []recordedRequest {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	var writes []recordedRequest
	for _, r := range mock.requests {
		if r.Method != http.MethodGet {
			writes = append(writes, r)
		}
	}
	return writes
}

// findRequest returns the first request with this method and path
func findRequest(t *testing.T, mock *mockJira, method, path string) recordedRequest {
	t.Helper()
	for _, r := range mock.requestsTo(path) {
		if r.Method == method {
			return r
		}
	}
	t.Fatalf("no %s %s request was made", method, path)
	return recordedRequest{}
}

// sameJSON reports whether two values encode to the same JSON
func sameJSON(a, b any) bool {
	aj, errA := json.Marshal(a)
	bj, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(aj) == string(bj)
}

func hasRequest(mock *mockJira, method, path string) bool {
	for _, r := range mock.requestsTo(path) {
		if r.Method == method {
			return true
		}
	}
	return false
}

// readAudit returns the records in the test's audit log
func readAudit(t *testing.T) []AuditRecord {
	t.Helper()
	f, err := os.Open(auditLogFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("open audit log: %v", err)
	}
	defer f.Close()

	var records []AuditRecord
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var rec AuditRecord
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			t.Fatalf("audit line %q: %v", scanner.Text(), err)
		}
		records = append(records, rec)
	}
	return records
}

var bothTokens = map[string]string{"sdm": "sdm-access", "target": "target-access"}

const (
	sdmAccess    = "Bearer sdm-access"
	targetAccess = "Bearer target-access"
)

func TestManagerCannotChangeImpersonatedTime(t *testing.T) {
	_, mock, do := newSDMTest(t, RoleManager, bothTokens)

	attempts := []struct{ method, target, body string }{
		{http.MethodPost, "/api/events", `{"issue_key":"ABC-1","start":"2026-10-13T09:00:00Z","duration_min":30}`},
		{http.MethodPut, "/api/events/ABC-1-100", `{"start":"2026-10-13T09:00:00Z","duration_min":30}`},
		{http.MethodDelete, "/api/events/ABC-1-100", ``},
		{http.MethodPost, "/api/events/ABC-1-100/split", `{"duration_min":30,"parent_key":"ABC-1"}`},
	}
	for _, a := range attempts {
		if rec := do(a.method, a.target, a.body); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s by a view-only manager: status %d, want 403", a.method, a.target, rec.Code)
		}
	}
	if n := requestCount(mock); n != 0 {
		t.Errorf("view-only manager's changes made %d Jira calls, want 0", n)
	}
}

func TestCanEditOthers(t *testing.T) {
	viper.Set("SUPER_USERS", []string{"admin@example.com"})
	t.Cleanup(func() { viper.Set("SUPER_USERS", nil) })
	setTestManager(t, "viewer", RoleManager)
	setTestManager(t, "fixer", RoleSDM)

	cases := map[string]struct {
		session           *UserSession
		edit, impersonate bool
	}{
		"super user": {&UserSession{AccountID: "admin", Email: "admin@example.com", EmailVerified: true}, true, true},
		"SDM":        {&UserSession{AccountID: "fixer"}, true, true},
		"manager":    {&UserSession{AccountID: "viewer"}, false, true},
		"anyone":     {&UserSession{AccountID: "nobody"}, false, false},
	}
	for name, c := range cases {
		if got := CanEditOthers(c.session); got != c.edit {
			t.Errorf("%s: CanEditOthers = %t, want %t", name, got, c.edit)
		}
		if got := CanImpersonate(c.session); got != c.impersonate {
			t.Errorf("%s: CanImpersonate = %t, want %t", name, got, c.impersonate)
		}
	}
}

func TestSuperUsersAreMatchedByVerifiedEmail(t *testing.T) {
	viper.Set("SUPER_USERS", []string{" Will.Stevens@Example.com ", "557058:old-account-id"})
	t.Cleanup(func() { viper.Set("SUPER_USERS", nil) })

	cases := []struct {
		name    string
		session UserSession
		want    bool
	}{
		{"same email, different case", UserSession{AccountID: "a", Email: "will.stevens@example.com", EmailVerified: true}, true},
		{"email not verified by Atlassian", UserSession{AccountID: "a", Email: "will.stevens@example.com"}, false},
		{"other email", UserSession{AccountID: "b", Email: "someone@example.com", EmailVerified: true}, false},
		{"no email", UserSession{AccountID: "c", EmailVerified: true}, false},
		// Account IDs no longer count (they only get a startup warning)
		{"account ID listed", UserSession{AccountID: "557058:old-account-id", EmailVerified: true}, false},
	}
	for _, c := range cases {
		if got := IsSuperUser(&c.session); got != c.want {
			t.Errorf("%s: IsSuperUser = %t, want %t", c.name, got, c.want)
		}
	}
}

func TestRestoredSessionGetsVerifiedEmail(t *testing.T) {
	t.Chdir(t.TempDir())

	identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(w, http.StatusOK, map[string]interface{}{"account_id": "acct", "email": "Admin@Example.com", "email_verified": true})
	}))
	t.Cleanup(identity.Close)
	original := atlassianMeURL
	atlassianMeURL = identity.URL
	t.Cleanup(func() { atlassianMeURL = original })

	store := testTokenStore(t, nil)
	store.set("acct", &oauth2.Token{AccessToken: "access", RefreshToken: "r", Expiry: time.Now().Add(time.Hour)})
	originalTokens := accountTokens
	accountTokens = store
	t.Cleanup(func() { accountTokens = originalTokens })

	// Saved before super users were matched by email: no verified email yet
	saveSessionData("acct", &UserSession{AccountID: "acct", CloudID: "cloud", Email: ""})

	session := tryRestoreSession("acct", "restored-session")
	t.Cleanup(func() { deleteSession("restored-session") })
	if session == nil || session.Email != "Admin@Example.com" || !session.EmailVerified {
		t.Fatalf("restored session %+v, want the verified email from Atlassian", session)
	}
	if saved := loadSessionData().Sessions["acct"]; !saved.EmailVerified {
		t.Error("verified email not saved, so it would be looked up again on every restore")
	}

	viper.Set("SUPER_USERS", []string{"admin@example.com"})
	t.Cleanup(func() { viper.Set("SUPER_USERS", nil) })
	if !IsSuperUser(session) {
		t.Error("restored super user not recognized")
	}
}

func TestSDMCreatesTimeAsTheViewedUser(t *testing.T) {
	_, mock, do := newSDMTest(t, RoleSDM, bothTokens)

	rec := do(http.MethodPost, "/api/events", `{"issue_key":"ABC-2","start":"2026-10-13T09:00:00Z","duration_min":30,"description":"Forgot this"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	if create := findRequest(t, mock, http.MethodPost, "/issue/ABC-2/worklog"); create.Auth != targetAccess {
		t.Errorf("worklog created with %q, want the viewed user's access so they are its author", create.Auth)
	}
	stamp := findRequest(t, mock, http.MethodPut, "/issue/ABC-2/worklog/200")
	if stamp.Auth != sdmAccess || stamp.Body["timeSpentSeconds"] != float64(1800) {
		t.Errorf("worklog re-saved with %q and %v, want the SDM's access and the same 1800 seconds", stamp.Auth, stamp.Body["timeSpentSeconds"])
	}
	if marker := findRequest(t, mock, http.MethodPut, "/issue/ABC-2/worklog/200/properties/"+sdmPropertyKey); marker.Body["account_id"] != "sdm" {
		t.Errorf("SDM marker %v does not name the SDM", marker.Body)
	}
	if perms := findRequest(t, mock, http.MethodGet, "/mypermissions"); perms.Auth != targetAccess || !strings.Contains(perms.Query, permWorkOnIssues) {
		t.Errorf("checked %q with %q, want the viewed user's Work On Issues", perms.Query, perms.Auth)
	}

	records := readAudit(t)
	if len(records) != 1 {
		t.Fatalf("got %d audit records, want 1", len(records))
	}
	if r := records[0]; r.Action != "create" || r.Outcome != auditOK || r.Actor != "sdm" || r.OnBehalfOf != "target" || r.IssueKey != "ABC-2" || r.WorklogID != "200" {
		t.Errorf("audit record %+v", r)
	}
}

func TestSDMCreateNeedsViewedUsersSignIn(t *testing.T) {
	_, mock, do := newSDMTest(t, RoleSDM, map[string]string{"sdm": "sdm-access"})

	rec := do(http.MethodPost, "/api/events", `{"issue_key":"ABC-2","start":"2026-10-13T09:00:00Z","duration_min":30,"description":"Forgot this"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Tara Target needs to sign in") {
		t.Errorf("message %q does not say who needs to sign in", rec.Body.String())
	}
	if n := requestCount(mock); n != 0 {
		t.Errorf("made %d Jira calls, want 0: nothing may be created as the SDM instead", n)
	}
}

func TestSDMCreateBlockedWhenViewedUserCannotLogWork(t *testing.T) {
	fake, mock, do := newSDMTest(t, RoleSDM, bothTokens)
	fake.missingPerms[permWorkOnIssues] = true

	rec := do(http.MethodPost, "/api/events", `{"issue_key":"ABC-2","start":"2026-10-13T09:00:00Z","duration_min":30,"description":"Forgot this"}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Work On Issues") {
		t.Fatalf("status %d %q, want 403 naming Work On Issues", rec.Code, rec.Body.String())
	}
	if writes := writesTo(mock); len(writes) != 0 {
		t.Errorf("wrote %v before failing", writes)
	}
}

func TestSDMUpdateUsesSDMsOwnAccess(t *testing.T) {
	_, mock, do := newSDMTest(t, RoleSDM, bothTokens)

	rec := do(http.MethodPut, "/api/events/ABC-1-100", `{"start":"2026-10-13T09:00:00Z","duration_min":60}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	for _, r := range writesTo(mock) {
		if r.Auth != sdmAccess {
			t.Errorf("%s %s used %q, want the SDM's own access", r.Method, r.Path, r.Auth)
		}
	}
	put := findRequest(t, mock, http.MethodPut, "/issue/ABC-1/worklog/100")
	if put.Body["timeSpentSeconds"] != float64(3600) {
		t.Errorf("updated to %v seconds, want 3600", put.Body["timeSpentSeconds"])
	}
	// A drag doesn't send the description: the comment must stay as stored
	if !sameJSON(put.Body["comment"], testWorklogComment) {
		t.Errorf("saved comment %v, want the stored one unchanged", put.Body["comment"])
	}

	records := readAudit(t)
	if len(records) != 1 || records[0].Before.TimeSpentSeconds != 5400 || records[0].After.TimeSpentSeconds != 3600 {
		t.Errorf("audit records %+v, want one update from 5400 to 3600 seconds", records)
	}
}

func TestSDMCannotChangeAnotherUsersWorklog(t *testing.T) {
	fake, mock, do := newSDMTest(t, RoleSDM, bothTokens)
	fake.author = "someone-else"

	if rec := do(http.MethodPut, "/api/events/ABC-1-100", `{"start":"2026-10-13T09:00:00Z","duration_min":60}`); rec.Code != http.StatusForbidden {
		t.Errorf("update: status %d, want 403", rec.Code)
	}
	if rec := do(http.MethodDelete, "/api/events/ABC-1-100", ``); rec.Code != http.StatusForbidden {
		t.Errorf("delete: status %d, want 403", rec.Code)
	}
	if rec := do(http.MethodPost, "/api/events/ABC-1-100/split", `{"duration_min":30,"parent_key":"ABC-1"}`); rec.Code != http.StatusForbidden {
		t.Errorf("split: status %d, want 403", rec.Code)
	}
	if writes := writesTo(mock); len(writes) != 0 {
		t.Errorf("changed another user's worklog: %v", writes)
	}

	records := readAudit(t)
	if len(records) != 3 || records[0].Outcome != auditDenied {
		t.Errorf("audit records %+v, want 3 denials", records)
	}
}

func TestSDMMoveRecreatesWorklogAsItsAuthor(t *testing.T) {
	_, mock, do := newSDMTest(t, RoleSDM, bothTokens)

	rec := do(http.MethodPut, "/api/events/ABC-1-100", `{"start":"2026-10-13T09:00:00Z","duration_min":90,"description":"Original note\nSecond paragraph","parent_key":"ABC-2"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	create := findRequest(t, mock, http.MethodPost, "/issue/ABC-2/worklog")
	if create.Auth != targetAccess {
		t.Errorf("copy created with %q, want the viewed user's access", create.Auth)
	}
	if comment, _ := json.Marshal(create.Body["comment"]); !strings.Contains(string(comment), "Second paragraph") {
		t.Errorf("copy's comment %s lost the original's second paragraph", comment)
	}
	if del := findRequest(t, mock, http.MethodDelete, "/issue/ABC-1/worklog/100"); del.Auth != sdmAccess {
		t.Errorf("original deleted with %q, want the SDM's own access", del.Auth)
	}
	if stamp := findRequest(t, mock, http.MethodPut, "/issue/ABC-2/worklog/200"); stamp.Auth != sdmAccess {
		t.Errorf("copy re-saved with %q, want the SDM's access so Jira shows them as its updater", stamp.Auth)
	}
	if !strings.Contains(rec.Body.String(), `"id":"ABC-2-200"`) {
		t.Errorf("response %s, want the copy's event ID ABC-2-200", rec.Body.String())
	}

	records := readAudit(t)
	if len(records) != 1 || records[0].Action != "move" || records[0].TargetKey != "ABC-2" || records[0].NewID != "200" {
		t.Errorf("audit records %+v, want one move to ABC-2 as worklog 200", records)
	}
}

func TestSDMMoveUndoneWhenOriginalCannotBeDeleted(t *testing.T) {
	fake, mock, do := newSDMTest(t, RoleSDM, bothTokens)
	fake.fail["DELETE /issue/ABC-1/worklog/100"] = http.StatusForbidden

	rec := do(http.MethodPut, "/api/events/ABC-1-100", `{"start":"2026-10-13T09:00:00Z","duration_min":90,"description":"Original note\nSecond paragraph","parent_key":"ABC-2"}`)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "rolled back") {
		t.Fatalf("status %d %q, want 500 saying the move was rolled back", rec.Code, rec.Body.String())
	}
	if del := findRequest(t, mock, http.MethodDelete, "/issue/ABC-2/worklog/200"); del.Auth != targetAccess {
		t.Errorf("copy removed with %q, want the viewed user's access", del.Auth)
	}
	if records := readAudit(t); len(records) != 0 {
		t.Errorf("audit records %+v for a move that was fully undone", records)
	}
}

func TestSDMMoveNeedsViewedUsersSignIn(t *testing.T) {
	_, mock, do := newSDMTest(t, RoleSDM, map[string]string{"sdm": "sdm-access"})

	rec := do(http.MethodPut, "/api/events/ABC-1-100", `{"start":"2026-10-13T09:00:00Z","duration_min":90,"description":"Original note\nSecond paragraph","parent_key":"ABC-2"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Tara Target needs to sign in") {
		t.Fatalf("status %d %q, want 409 saying who needs to sign in", rec.Code, rec.Body.String())
	}
	if writes := writesTo(mock); len(writes) != 0 {
		t.Errorf("wrote %v although the move can't be made as the worklog's author", writes)
	}
}

func TestSplitKeepsAuthorAndTotalTime(t *testing.T) {
	_, mock, do := newSDMTest(t, RoleSDM, bothTokens)

	rec := do(http.MethodPost, "/api/events/ABC-1-100/split", `{"duration_min":60,"parent_key":"ABC-2"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	writes := writesTo(mock)
	createAt, resizeAt := -1, -1
	for i, r := range writes {
		switch {
		case r.Method == http.MethodPost && r.Path == "/issue/ABC-2/worklog":
			createAt = i
		case r.Method == http.MethodPut && r.Path == "/issue/ABC-1/worklog/100":
			resizeAt = i
		}
	}
	if createAt == -1 || resizeAt == -1 || createAt > resizeAt {
		t.Fatalf("writes %v, want the new part created before the original is shortened", writes)
	}

	rest, original := writes[createAt], writes[resizeAt]
	if rest.Auth != targetAccess || rest.Body["started"] != "2026-10-13T10:00:00.000+0000" || rest.Body["timeSpentSeconds"] != float64(1800) {
		t.Errorf("new part %+v, want 30 minutes from 10:00 created with the viewed user's access", rest)
	}
	if comment, _ := json.Marshal(rest.Body["comment"]); !strings.Contains(string(comment), "Second paragraph") {
		t.Errorf("new part's comment %s is not a copy of the original's", comment)
	}
	if original.Auth != sdmAccess || original.Body["timeSpentSeconds"] != float64(3600) || original.Body["started"] != "2026-10-13T09:00:00.000+0000" {
		t.Errorf("original %+v, want 60 minutes from 09:00 saved with the SDM's access", original)
	}

	records := readAudit(t)
	if len(records) != 1 || records[0].Action != "split" || records[0].NewID != "200" {
		t.Errorf("audit records %+v, want one split", records)
	}
}

func TestSplitUndoneWhenOriginalCannotBeShortened(t *testing.T) {
	fake, mock, do := newSDMTest(t, RoleSDM, bothTokens)
	fake.fail["PUT /issue/ABC-1/worklog/100"] = http.StatusBadRequest

	rec := do(http.MethodPost, "/api/events/ABC-1-100/split", `{"duration_min":60,"parent_key":"ABC-2"}`)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "split undone") {
		t.Fatalf("status %d %q, want 500 saying the split was undone", rec.Code, rec.Body.String())
	}
	if del := findRequest(t, mock, http.MethodDelete, "/issue/ABC-2/worklog/200"); del.Auth != targetAccess {
		t.Errorf("new part removed with %q, want the viewed user's access", del.Auth)
	}
	if records := readAudit(t); len(records) != 0 {
		t.Errorf("audit records %+v for a change that was fully undone", records)
	}
}

func TestSplitReportsTimeLoggedTwiceWhenUndoFails(t *testing.T) {
	fake, _, do := newSDMTest(t, RoleSDM, bothTokens)
	fake.fail["PUT /issue/ABC-1/worklog/100"] = http.StatusBadRequest
	fake.fail["DELETE /issue/ABC-2/worklog/200"] = http.StatusBadRequest

	rec := do(http.MethodPost, "/api/events/ABC-1-100/split", `{"duration_min":60,"parent_key":"ABC-2"}`)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "logged twice") {
		t.Fatalf("status %d %q, want 500 saying time is logged twice", rec.Code, rec.Body.String())
	}

	records := readAudit(t)
	if len(records) != 1 || records[0].Outcome != auditPartial || records[0].NewID != "200" {
		t.Errorf("audit records %+v, want one partial split naming worklog 200", records)
	}
}

func TestSplitChecksBeforeWriting(t *testing.T) {
	fake, mock, do := newSDMTest(t, RoleSDM, bothTokens)

	for _, body := range []string{`{"duration_min":90,"parent_key":"ABC-2"}`, `{"duration_min":0,"parent_key":"ABC-2"}`} {
		if rec := do(http.MethodPost, "/api/events/ABC-1-100/split", body); rec.Code != http.StatusBadRequest {
			t.Errorf("split %s: status %d, want 400", body, rec.Code)
		}
	}

	fake.missingPerms[permEditAllWorklogs] = true
	rec := do(http.MethodPost, "/api/events/ABC-1-100/split", `{"duration_min":60,"parent_key":"ABC-2"}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Edit All Worklogs") {
		t.Errorf("status %d %q, want 403 naming Edit All Worklogs", rec.Code, rec.Body.String())
	}

	if writes := writesTo(mock); len(writes) != 0 {
		t.Errorf("wrote %v although the split was refused", writes)
	}
}

func TestKeysAreValidatedBeforeReachingJira(t *testing.T) {
	_, mock, do := newSDMTest(t, RoleSDM, bothTokens)

	attempts := []struct{ method, target, body string }{
		{http.MethodPost, "/api/events", `{"issue_key":"ABC-1/../../myself","start":"2026-10-13T09:00:00Z","duration_min":30,"description":"Note"}`},
		{http.MethodPost, "/api/events", `{"issue_key":"ABC-1","subtask_key":"x?y","start":"2026-10-13T09:00:00Z","duration_min":30,"description":"Note"}`},
		{http.MethodPut, "/api/events/ABC-1-1x", `{"start":"2026-10-13T09:00:00Z","duration_min":30}`},
		{http.MethodPut, "/api/events/ABC-1-100", `{"start":"2026-10-13T09:00:00Z","duration_min":30,"description":"Note","parent_key":"../ABC-2"}`},
		{http.MethodDelete, "/api/events/..-100", ``},
	}
	for _, a := range attempts {
		if rec := do(a.method, a.target, a.body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s %s: status %d, want 400", a.method, a.target, a.body, rec.Code)
		}
	}
	if n := requestCount(mock); n != 0 {
		t.Errorf("invalid keys made %d Jira calls, want 0", n)
	}
}

func TestDialogSavesNeedADescription(t *testing.T) {
	_, mock, do := newSDMTest(t, RoleSDM, bothTokens)

	attempts := []struct{ method, target, body string }{
		{http.MethodPost, "/api/events", `{"issue_key":"ABC-2","start":"2026-10-13T09:00:00Z","duration_min":30}`},
		{http.MethodPost, "/api/events", `{"issue_key":"ABC-2","start":"2026-10-13T09:00:00Z","duration_min":30,"description":" \n "}`},
		{http.MethodPut, "/api/events/ABC-1-100", `{"start":"2026-10-13T09:00:00Z","duration_min":90,"description":"","parent_key":"ABC-1"}`},
		{http.MethodPut, "/api/events/ABC-1-100", `{"start":"2026-10-13T09:00:00Z","duration_min":90,"description":" ","parent_key":"ABC-2"}`},
		{http.MethodPut, "/api/events/ABC-1-100", `{"start":"2026-10-13T09:00:00Z","duration_min":90,"description":"\n"}`},
	}
	for _, a := range attempts {
		if rec := do(a.method, a.target, a.body); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), descriptionRequired) {
			t.Errorf("%s %s %s: status %d %q, want 400 saying a description is required", a.method, a.target, a.body, rec.Code, rec.Body.String())
		}
	}
	if n := requestCount(mock); n != 0 {
		t.Errorf("entries without a description made %d Jira calls, want 0", n)
	}

	// Dragging or resizing doesn't edit the description, so entries logged
	// without one can still be moved
	if rec := do(http.MethodPut, "/api/events/ABC-1-100", `{"start":"2026-10-13T10:00:00Z","duration_min":90}`); rec.Code != http.StatusOK {
		t.Errorf("drag update without a description: status %d %q, want 200", rec.Code, rec.Body.String())
	}
}

// Changes that leave the description alone (drags, and dialog saves of the
// text as shown) keep the comment exactly as Jira stores it, formatting and all
func TestOwnCalendarChangesKeepTheStoredComment(t *testing.T) {
	_, mock, do := newOwnCalendarTest(t)

	if rec := do(http.MethodPut, "/api/events/ABC-1-100", `{"start":"2026-10-13T10:00:00Z","duration_min":60}`); rec.Code != http.StatusOK {
		t.Fatalf("drag: status %d: %s", rec.Code, rec.Body.String())
	}
	put := findRequest(t, mock, http.MethodPut, "/issue/ABC-1/worklog/100")
	if put.Auth != targetAccess || put.Body["timeSpentSeconds"] != float64(3600) {
		t.Errorf("updated with %q to %v seconds, want your own access and 3600", put.Auth, put.Body["timeSpentSeconds"])
	}
	if !sameJSON(put.Body["comment"], testWorklogComment) {
		t.Errorf("drag saved comment %v, want the stored one unchanged", put.Body["comment"])
	}

	if rec := do(http.MethodPut, "/api/events/ABC-1-100", `{"start":"2026-10-13T09:00:00Z","duration_min":90,"description":"Original note\nSecond paragraph","parent_key":"ABC-2"}`); rec.Code != http.StatusOK {
		t.Fatalf("move: status %d: %s", rec.Code, rec.Body.String())
	}
	create := findRequest(t, mock, http.MethodPost, "/issue/ABC-2/worklog")
	if !sameJSON(create.Body["comment"], testWorklogComment) {
		t.Errorf("moved worklog's comment %v, want the stored one unchanged", create.Body["comment"])
	}
	if !hasRequest(mock, http.MethodDelete, "/issue/ABC-1/worklog/100") {
		t.Error("move left the original worklog in place")
	}

	if records := readAudit(t); len(records) != 0 {
		t.Errorf("audit records %+v for changes to your own calendar", records)
	}
}

func TestOwnCalendarEditedDescriptionReplacesComment(t *testing.T) {
	_, mock, do := newOwnCalendarTest(t)

	if rec := do(http.MethodPut, "/api/events/ABC-1-100", `{"start":"2026-10-13T09:00:00Z","duration_min":90,"description":"Rewritten note\nLine two","parent_key":"ABC-1"}`); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	comment := findRequest(t, mock, http.MethodPut, "/issue/ABC-1/worklog/100").Body["comment"]
	if !sameJSON(comment, buildADFComment("Rewritten note\nLine two")) {
		t.Errorf("saved comment %v, want the edited description, one paragraph per line", comment)
	}
}

func TestManagerRoles(t *testing.T) {
	t.Chdir(t.TempDir())
	session := &UserSession{AccountID: "admin"}
	t.Cleanup(func() {
		managersLock.Lock()
		delete(managers, "m1")
		managersLock.Unlock()
	})

	add := func(body string) int {
		rec := httptest.NewRecorder()
		handleAddManager(rec, httptest.NewRequest(http.MethodPost, "/api/managers", strings.NewReader(body)), session)
		return rec.Code
	}

	steps := []struct {
		body       string
		wantStatus int
		wantRole   string
	}{
		{`{"account_id":"m1","display_name":"M One"}`, http.StatusCreated, RoleManager},
		{`{"account_id":"m1","display_name":"M One","role":"sdm"}`, http.StatusCreated, RoleSDM},
		// Re-adding from the search box sends no role and must not demote them
		{`{"account_id":"m1","display_name":"M One"}`, http.StatusCreated, RoleSDM},
		{`{"account_id":"m1","display_name":"M One","role":"admin"}`, http.StatusBadRequest, RoleSDM},
		{`{"account_id":"m1","display_name":"M One","role":"manager"}`, http.StatusCreated, RoleManager},
	}
	for _, s := range steps {
		if got := add(s.body); got != s.wantStatus {
			t.Errorf("%s: status %d, want %d", s.body, got, s.wantStatus)
		}
		managersLock.RLock()
		role := managers["m1"].Role
		managersLock.RUnlock()
		if role != s.wantRole {
			t.Errorf("after %s: role %q, want %q", s.body, role, s.wantRole)
		}
	}
}

func TestHierarchyNodes(t *testing.T) {
	issue := func(key string, level int, subtask bool, statusCategory string) JiraSearchIssue {
		var i JiraSearchIssue
		i.Key = key
		i.Fields.IssueType = JiraIssueTypeRef{HierarchyLevel: level, Subtask: subtask}
		i.Fields.Status.Name = statusCategory
		i.Fields.Status.StatusCategory.Key = statusCategory
		return i
	}

	withSub := issue("ABC-2", 0, false, "new")
	withSub.Fields.Subtasks = append(withSub.Fields.Subtasks, struct {
		Key    string `json:"key"`
		Fields struct {
			Summary string        `json:"summary"`
			Status  JiraStatusRef `json:"status"`
		} `json:"fields"`
	}{Key: "ABC-3"})

	search := []JiraSearchIssue{
		issue("ABC-1", 0, false, "done"),
		withSub,
		issue("ABC-4", 1, false, "new"), // epic
		issue("ABC-5", 2, false, "new"), // initiative
		issue("ABC-6", -1, true, "new"), // sub-task
	}

	issues, truncated := hierarchyNodes(search, false)
	if truncated || len(issues) != 2 || issues[0].Key != "ABC-2" || issues[1].Key != "ABC-1" || !issues[1].Done {
		t.Errorf("issue level %+v, want ABC-2 (open) then ABC-1 (done)", issues)
	}
	if len(issues[0].Subtasks) != 1 || issues[0].Subtasks[0].Key != "ABC-3" {
		t.Errorf("ABC-2 sub-tasks %+v, want ABC-3", issues[0].Subtasks)
	}

	epics, _ := hierarchyNodes(search, true)
	if len(epics) != 1 || epics[0].Key != "ABC-4" {
		t.Errorf("epic level %+v, want only ABC-4", epics)
	}

	many := make([]JiraSearchIssue, hierarchyLimit+1)
	for i := range many {
		many[i] = issue("ABC-1", 0, false, "new")
	}
	if nodes, truncated := hierarchyNodes(many, false); !truncated || len(nodes) != hierarchyLimit {
		t.Errorf("%d issues: got %d nodes, truncated %t", len(many), len(nodes), truncated)
	}
}

func TestGetProjectEpicsFindsEpicTypesByLevel(t *testing.T) {
	mock, client := newMockJira(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		switch r.URL.Path {
		case "/project/ABC":
			writeTestJSON(w, http.StatusOK, map[string]interface{}{
				"id": "1", "key": "ABC", "name": "Acme",
				"issueTypes": []interface{}{
					map[string]interface{}{"id": "10000", "name": "Feature", "hierarchyLevel": 1},
					map[string]interface{}{"id": "10001", "name": "Task", "hierarchyLevel": 0},
					map[string]interface{}{"id": "10002", "name": "Initiative", "hierarchyLevel": 2},
				},
			})
		case "/search/jql":
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"issues": []interface{}{searchIssue("ABC-9", "Feature", 1)}, "isLast": true})
		}
	})

	res, err := client.GetProjectEpics(context.Background(), "ABC")
	if err != nil {
		t.Fatalf("GetProjectEpics: %v", err)
	}
	if res.Project == nil || res.Project.Name != "Acme" || len(res.Epics) != 1 || res.Epics[0].Key != "ABC-9" {
		t.Errorf("result %+v", res)
	}
	if jql := mock.requestsTo("/search/jql")[0].Body["jql"].(string); !strings.Contains(jql, "issuetype in (10000)") {
		t.Errorf("JQL %q, want the level-1 type (renamed epics included, initiatives left out)", jql)
	}
}

func TestSearchIssuesForSDMs(t *testing.T) {
	viper.Set("DONE_ISSUES_DAYS", 90)
	mock, client := newMockJira(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		writeTestJSON(w, http.StatusOK, map[string]interface{}{"issues": []interface{}{}, "isLast": true})
	})

	if _, err := client.SearchIssues(context.Background(), `say "hi"`, true); err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	jql := mock.requestsTo("/search/jql")[0].Body["jql"].(string)
	if strings.Contains(jql, "statusCategoryChangedDate") {
		t.Errorf("JQL %q limits Done issues, but SDMs need all of them", jql)
	}
	if !strings.Contains(jql, `summary ~ "say \"hi\"*"`) {
		t.Errorf("JQL %q does not escape the quotes in the search term", jql)
	}
}

func TestParseEventID(t *testing.T) {
	cases := map[string]bool{
		"ABC-1-100":     true,
		"AB_C2-17-9":    true,
		"ABC-1-":        false,
		"ABC-1-10a":     false,
		"../x-1-100":    false,
		"ABC-1/2-100":   false,
		"100":           false,
		"ABC-1-100/../": false,
	}
	for id, want := range cases {
		if _, _, ok := parseEventID(id); ok != want {
			t.Errorf("parseEventID(%q) ok = %t, want %t", id, ok, want)
		}
	}
}

func TestAuditTimesAreUTC(t *testing.T) {
	// Jira reports starts in the user's offset, the browser sends UTC: the
	// audit record must show both the same way
	before := auditWorklogOf("RND-61", &JiraWorklog{Started: "2026-10-15T12:00:00.000-0400", TimeSpentSeconds: 3600})
	after, _ := time.Parse(time.RFC3339, "2026-10-15T16:00:00Z")
	if before.Started != "2026-10-15T16:00:00Z" || auditTime(after) != before.Started {
		t.Errorf("before %q, after %q: want the same UTC time", before.Started, auditTime(after))
	}
}

func TestOnlySuperUsersCanChangeRoles(t *testing.T) {
	t.Chdir(t.TempDir())
	viper.Set("SESSION_SECRET", "test-secret-that-is-at-least-32-characters")
	viper.Set("SUPER_USERS", []string{"admin@example.com"})
	t.Cleanup(func() { viper.Set("SUPER_USERS", nil) })
	setTestManager(t, "viewer", RoleManager)
	setTestManager(t, "fixer", RoleSDM)
	admin := &UserSession{AccountID: "admin", Email: "admin@example.com", EmailVerified: true}

	request := func(session *UserSession, method, target, body string) int {
		sessionID := createSession(session)
		defer deleteSession(sessionID)
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: sessionID + "." + signSession(sessionID)})
		rec := httptest.NewRecorder()
		if strings.HasPrefix(target, "/api/managers/") {
			handleManagerByIDRoute(rec, req)
		} else {
			handleManagersRoute(rec, req)
		}
		return rec.Code
	}

	attempts := []struct {
		who     string
		session *UserSession
	}{
		{"a manager", &UserSession{AccountID: "viewer"}},
		{"an SDM", &UserSession{AccountID: "fixer"}},
		// Impersonating a super user does not lend their rights
		{"an SDM viewing as a super user", &UserSession{AccountID: "fixer", ImpersonatingID: "admin"}},
	}
	for _, a := range attempts {
		if code := request(a.session, http.MethodPost, "/api/managers", `{"account_id":"viewer","display_name":"viewer","role":"sdm"}`); code != http.StatusForbidden {
			t.Errorf("%s promoting a manager: status %d, want 403", a.who, code)
		}
		if code := request(a.session, http.MethodDelete, "/api/managers/fixer", ``); code != http.StatusForbidden {
			t.Errorf("%s removing an SDM: status %d, want 403", a.who, code)
		}
		if code := request(a.session, http.MethodGet, "/api/managers", ``); code != http.StatusForbidden {
			t.Errorf("%s listing managers: status %d, want 403", a.who, code)
		}
	}
	if IsSDM("viewer") || !IsSDM("fixer") {
		t.Error("roles changed by someone who isn't a super user")
	}

	if code := request(admin, http.MethodPost, "/api/managers", `{"account_id":"viewer","display_name":"viewer","role":"sdm"}`); code != http.StatusCreated || !IsSDM("viewer") {
		t.Errorf("super user promoting a manager: status %d, want 201 and the SDM role", code)
	}
}
