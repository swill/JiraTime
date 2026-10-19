package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"golang.org/x/oauth2"
)

// newViewTest sets up a view-only manager ("manager") session, saved tokens
// for the given accounts, and a mock Jira that records which token each call
// used
func newViewTest(t *testing.T, impersonating string, tokens map[string]string) (*mockJira, func(method, target string) *httptest.ResponseRecorder) {
	t.Helper()
	viper.Set("SESSION_SECRET", "test-secret-that-is-at-least-32-characters")
	viper.Set("BASE_URL", "https://jiratime.example")
	setTestManager(t, "manager", RoleManager)

	store := testTokenStore(t, nil)
	for account, access := range tokens {
		store.set(account, &oauth2.Token{AccessToken: access, RefreshToken: "r", Expiry: time.Now().Add(time.Hour)})
	}
	original := accountTokens
	accountTokens = store
	t.Cleanup(func() { accountTokens = original })

	mock, _ := newMockJira(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		switch {
		case r.URL.Path == "/search/jql":
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"issues": []interface{}{}, "isLast": true})
		case r.URL.Path == "/issue/ABC-1":
			writeTestJSON(w, http.StatusOK, searchIssue("ABC-1", "Task", 0))
		default:
			writeTestJSON(w, http.StatusNotFound, map[string]interface{}{})
		}
	})

	t.Cleanup(func() {
		cache.InvalidateAll("manager")
		cache.InvalidateAll("target")
	})

	session := &UserSession{AccountID: "manager", CloudID: "cloud"}
	if impersonating != "" {
		session.ImpersonatingID = impersonating
		session.ImpersonatingName = "Target User"
	}
	sessionID := createSession(session)
	t.Cleanup(func() { deleteSession(sessionID) })

	routes := map[string]http.HandlerFunc{
		"/api/events":        handleEvents,
		"/api/hours":         handleGetHours,
		"/api/issues":        handleGetIssues,
		"/api/issues/search": handleSearchIssues,
		"/api/issues/":       handleIssueByKey,
	}

	do := func(method, target string) *httptest.ResponseRecorder {
		path := strings.SplitN(target, "?", 2)[0]
		handler, ok := routes[path]
		if !ok {
			handler = routes["/api/issues/"]
		}
		var body *strings.Reader
		if method == http.MethodPost {
			body = strings.NewReader(`{"issue_key":"ABC-1","start":"2026-10-13T09:00:00Z","duration_min":30}`)
		} else {
			body = strings.NewReader("")
		}
		req := httptest.NewRequest(method, target, body)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: sessionID + "." + signSession(sessionID)})
		req.AddCookie(&http.Cookie{Name: accountIDCookie, Value: "manager." + signSession("manager")})
		rec := httptest.NewRecorder()
		requireAuth(handler)(rec, req)
		return rec
	}
	return mock, do
}

// tokensUsed returns the distinct Authorization headers Jira saw since index from
func tokensUsed(mock *mockJira, from int) []string {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	seen := map[string]bool{}
	var used []string
	for _, r := range mock.requests[from:] {
		if !seen[r.Auth] {
			seen[r.Auth] = true
			used = append(used, r.Auth)
		}
	}
	return used
}

func requestCount(mock *mockJira) int {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	return len(mock.requests)
}

const weekQuery = "?start=2026-10-12T00:00:00Z&end=2026-10-19T00:00:00Z"

func TestImpersonatedViewUsesViewedUsersJiraAccess(t *testing.T) {
	mock, do := newViewTest(t, "target", map[string]string{"manager": "manager-access", "target": "target-access"})

	cases := []struct {
		name, method, target, wantToken string
	}{
		// Scoped to the viewed user's own work: their access, so nothing is hidden
		{"calendar", http.MethodGet, "/api/events" + weekQuery, "Bearer target-access"},
		{"hours total", http.MethodGet, "/api/hours" + weekQuery, "Bearer target-access"},
		{"active issues", http.MethodGet, "/api/issues", "Bearer target-access"},
		// Not scoped to their work: the viewer's own access
		{"issue search", http.MethodGet, "/api/issues/search?q=abc", "Bearer manager-access"},
		{"issue lookup", http.MethodGet, "/api/issues/ABC-1/subtask-options", "Bearer manager-access"},
	}
	for _, c := range cases {
		from := requestCount(mock)
		rec := do(c.method, c.target)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d: %s", c.name, rec.Code, rec.Body.String())
			continue
		}
		if used := tokensUsed(mock, from); len(used) != 1 || used[0] != c.wantToken {
			t.Errorf("%s: Jira called with %v, want only %q", c.name, used, c.wantToken)
		}
		if rec.Header().Get("X-View-Limited") != "" {
			t.Errorf("%s: flagged as limited although the viewed user's access was available", c.name)
		}
	}

	// Writes stay blocked and never reach Jira with anyone's token
	from := requestCount(mock)
	if rec := do(http.MethodPost, "/api/events"); rec.Code != http.StatusForbidden {
		t.Errorf("create while impersonating: status %d, want 403", rec.Code)
	}
	if n := requestCount(mock) - from; n != 0 {
		t.Errorf("create while impersonating made %d Jira calls, want 0", n)
	}
}

func TestImpersonatedViewWithoutViewedUsersTokenIsFlagged(t *testing.T) {
	mock, do := newViewTest(t, "target", map[string]string{"manager": "manager-access"})

	from := requestCount(mock)
	rec := do(http.MethodGet, "/api/events"+weekQuery)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if used := tokensUsed(mock, from); len(used) != 1 || used[0] != "Bearer manager-access" {
		t.Errorf("Jira called with %v, want the viewer's own access as a fallback", used)
	}
	if rec.Header().Get("X-View-Limited") != "1" {
		t.Error("calendar not flagged as limited, so the manager would not know entries may be missing")
	}

	// The limited Active Issues list must not be cached as the user's own
	do(http.MethodGet, "/api/issues")
	if _, ok := cache.GetIssues("target"); ok {
		t.Error("limited Active Issues cached for the viewed user; their own sidebar would be missing issues")
	}
}

func TestOwnViewUsesOwnJiraAccess(t *testing.T) {
	mock, do := newViewTest(t, "", map[string]string{"manager": "manager-access", "target": "target-access"})

	rec := do(http.MethodGet, "/api/events"+weekQuery)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if used := tokensUsed(mock, 0); len(used) != 1 || used[0] != "Bearer manager-access" {
		t.Errorf("Jira called with %v, want only the signed-in user's own access", used)
	}
	if rec.Header().Get("X-View-Limited") != "" {
		t.Error("own calendar flagged as limited")
	}
}
