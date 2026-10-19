package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/viper"
	"golang.org/x/oauth2"
)

// mockJira serves canned Jira API responses keyed by request path and records
// the requests it received
type mockJira struct {
	t       *testing.T
	handler func(w http.ResponseWriter, r *http.Request, body map[string]interface{})

	mu       sync.Mutex
	requests []recordedRequest
}

type recordedRequest struct {
	Method string
	Path   string
	Query  string
	Auth   string
	Body   map[string]interface{}
}

func newMockJira(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, body map[string]interface{})) (*mockJira, *JiraClient) {
	t.Helper()
	m := &mockJira{t: t, handler: handler}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		path := strings.TrimPrefix(r.URL.Path, "/cloud/rest/api/3")
		m.mu.Lock()
		m.requests = append(m.requests, recordedRequest{Method: r.Method, Path: path, Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization"), Body: body})
		m.mu.Unlock()
		r.URL.Path = path
		handler(w, r, body)
	}))
	t.Cleanup(server.Close)

	original := jiraAPIBase
	jiraAPIBase = server.URL
	t.Cleanup(func() { jiraAPIBase = original })

	// Site-level caches (e.g. archived projects) must not leak between tests
	cache.InvalidateSite("cloud")
	t.Cleanup(func() { cache.InvalidateSite("cloud") })

	return m, NewJiraClient(&oauth2.Token{AccessToken: "test"}, "cloud")
}

func (m *mockJira) requestsTo(path string) []recordedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	var matched []recordedRequest
	for _, r := range m.requests {
		if r.Path == path {
			matched = append(matched, r)
		}
	}
	return matched
}

func writeTestJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func searchIssue(key, typeName string, hierarchyLevel int) map[string]interface{} {
	return map[string]interface{}{
		"id":  key + "-id",
		"key": key,
		"fields": map[string]interface{}{
			"summary":   "Summary of " + key,
			"project":   map[string]interface{}{"id": "1", "key": strings.Split(key, "-")[0], "name": "Project"},
			"issuetype": map[string]interface{}{"id": "1", "name": typeName, "subtask": false, "hierarchyLevel": hierarchyLevel},
		},
	}
}

func TestSearchIssuesFollowsPagesAndSkipsEpics(t *testing.T) {
	viper.Set("DONE_ISSUES_DAYS", 90)

	mock, client := newMockJira(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		if body["nextPageToken"] == nil {
			writeTestJSON(w, http.StatusOK, map[string]interface{}{
				"issues":        []interface{}{searchIssue("ABC-1", "Task", 0), searchIssue("ABC-2", "Epic", 1)},
				"nextPageToken": "page2",
				"isLast":        false,
			})
			return
		}
		writeTestJSON(w, http.StatusOK, map[string]interface{}{
			"issues": []interface{}{searchIssue("ABC-3", "Bug", 0), searchIssue("ABC-4", "Initiative", 2)},
			"isLast": true,
		})
	})

	issues, err := client.SearchIssues(context.Background(), "ABC", false)
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}

	var keys []string
	for _, issue := range issues {
		keys = append(keys, issue.Key)
	}
	if strings.Join(keys, ",") != "ABC-1,ABC-3" {
		t.Errorf("got issues %v, want ABC-1,ABC-3 (Epic-level issues excluded, both pages read)", keys)
	}

	searches := mock.requestsTo("/search/jql")
	if len(searches) != 2 {
		t.Fatalf("got %d search requests, want 2", len(searches))
	}
	if searches[1].Body["nextPageToken"] != "page2" {
		t.Errorf("second request nextPageToken = %v, want page2", searches[1].Body["nextPageToken"])
	}
	jql := searches[0].Body["jql"].(string)
	if !strings.Contains(jql, "statusCategoryChangedDate >= -90d") {
		t.Errorf("JQL %q does not keep Done issues for 90 days", jql)
	}
	if fields := searches[0].Body["fields"].([]interface{}); !containsValue(fields, "issuetype") {
		t.Errorf("search fields %v do not include issuetype", fields)
	}
	if _, ok := searches[0].Body["includeArchivedProjects"]; ok {
		t.Error("issue search includes archived projects, whose issues cannot have time logged")
	}
}

func TestSearchIssuesRespectsLimit(t *testing.T) {
	mock, client := newMockJira(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		writeTestJSON(w, http.StatusOK, map[string]interface{}{
			"issues":        []interface{}{searchIssue("ABC-1", "Task", 0), searchIssue("ABC-2", "Task", 0)},
			"nextPageToken": "more",
		})
	})

	issues, err := client.searchIssues(context.Background(), "project = ABC", []string{"summary"}, 3, excludeArchivedProjects)
	if err != nil {
		t.Fatalf("searchIssues: %v", err)
	}
	if len(issues) < 3 {
		t.Errorf("got %d issues, want at least the limit of 3", len(issues))
	}
	searches := mock.requestsTo("/search/jql")
	if len(searches) != 2 {
		t.Errorf("got %d requests, want 2 (stop once the limit is reached)", len(searches))
	}
	if got := searches[1].Body["maxResults"]; got != float64(1) {
		t.Errorf("second page maxResults = %v, want 1 (only what is left of the limit)", got)
	}
}

func TestGetMyIssuesSkipsEpicsAndKeepsActivityWindow(t *testing.T) {
	viper.Set("ACTIVE_ISSUES_WEEKS", 4)
	viper.Set("DONE_ISSUES_DAYS", 90)

	mock, client := newMockJira(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		writeTestJSON(w, http.StatusOK, map[string]interface{}{
			"issues": []interface{}{searchIssue("ABC-1", "Task", 0), searchIssue("ABC-2", "Epic", 1)},
			"isLast": true,
		})
	})

	groups, err := client.GetMyIssues(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("GetMyIssues: %v", err)
	}
	if len(groups) != 1 || len(groups[0].Issues) != 1 || groups[0].Issues[0].Key != "ABC-1" {
		t.Errorf("got %+v, want only ABC-1", groups)
	}

	search := mock.requestsTo("/search/jql")[0]
	jql := search.Body["jql"].(string)
	for _, want := range []string{"updated >= -4w", "statusCategoryChangedDate >= -90d", `assignee = "user-1"`} {
		if !strings.Contains(jql, want) {
			t.Errorf("JQL %q missing %q", jql, want)
		}
	}
	if _, ok := search.Body["includeArchivedProjects"]; ok {
		t.Error("Active Issues search includes archived projects, whose issues cannot have time logged")
	}
}

func worklog(id, author, started string, seconds int, fromJiraTime bool) map[string]interface{} {
	wl := map[string]interface{}{
		"id":               id,
		"started":          started,
		"timeSpentSeconds": seconds,
		"author":           map[string]interface{}{"accountId": author},
	}
	if fromJiraTime {
		wl["properties"] = []interface{}{map[string]interface{}{"key": worklogSourcePropertyKey, "value": map[string]string{"created_by": "jiratime"}}}
	}
	return wl
}

func TestGetMyWorklogsForPeriod(t *testing.T) {
	start := time.Date(2026, 10, 12, 0, 0, 0, 0, time.FixedZone("EDT", -4*3600))
	end := start.AddDate(0, 0, 7)

	mock, client := newMockJira(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		switch r.URL.Path {
		case "/search/jql":
			writeTestJSON(w, http.StatusOK, map[string]interface{}{
				"issues": []interface{}{searchIssue("ABC-1", "Task", 0), searchIssue("ABC-2", "Task", 0), searchIssue("ABC-3", "Epic", 1)},
				"isLast": true,
			})
		case "/issue/ABC-1/worklog":
			// Two pages: JiraTime + external worklogs, another author, one outside the range
			if r.URL.Query().Get("startAt") == "0" {
				writeTestJSON(w, http.StatusOK, map[string]interface{}{"startAt": 0, "total": 4, "worklogs": []interface{}{
					worklog("1", "me", "2026-10-13T09:00:00.000-0400", 900, true),
					worklog("2", "someone-else", "2026-10-13T10:00:00.000-0400", 3600, false),
				}})
				return
			}
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"startAt": 2, "total": 4, "worklogs": []interface{}{
				worklog("3", "me", "2026-10-14T23:00:00.000-0400", 1800, false),
				worklog("4", "me", "2026-10-19T09:00:00.000-0400", 3600, false),
			}})
		case "/issue/ABC-2/worklog":
			// Deleted between search and fetch
			writeTestJSON(w, http.StatusNotFound, map[string]interface{}{})
		case "/issue/ABC-3/worklog":
			// Existing time on an Epic must still show on the calendar
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"startAt": 0, "total": 1, "worklogs": []interface{}{
				worklog("5", "me", "2026-10-15T14:00:00.000-0400", 3600, false),
			}})
		case "/project/ABC":
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"key": "ABC", "archived": false})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			writeTestJSON(w, http.StatusNotFound, nil)
		}
	})

	events, err := client.GetMyWorklogsForPeriod(context.Background(), start, end, "me")
	if err != nil {
		t.Fatalf("GetMyWorklogsForPeriod: %v", err)
	}

	got := map[string]bool{}
	for _, e := range events {
		got[e.ID] = e.FromJiraTime
	}
	want := map[string]bool{"ABC-1-1": true, "ABC-1-3": false, "ABC-3-5": false}
	if len(got) != len(want) {
		t.Errorf("got events %v, want %v", got, want)
	}
	for id, fromJiraTime := range want {
		if gotFrom, ok := got[id]; !ok || gotFrom != fromJiraTime {
			t.Errorf("event %s: present=%v from_jiratime=%v, want from_jiratime=%v", id, ok, gotFrom, fromJiraTime)
		}
	}

	jql := mock.requestsTo("/search/jql")[0].Body["jql"].(string)
	if !strings.Contains(jql, "worklogDate >= 2026-10-11") || !strings.Contains(jql, "worklogDate <= 2026-10-20") {
		t.Errorf("JQL %q does not widen the date window by a day each side", jql)
	}

	wlReq := mock.requestsTo("/issue/ABC-1/worklog")
	if len(wlReq) != 2 {
		t.Fatalf("got %d worklog page requests for ABC-1, want 2", len(wlReq))
	}
	if !strings.Contains(wlReq[0].Query, "expand=properties") || !strings.Contains(wlReq[0].Query, "startedAfter=") {
		t.Errorf("worklog query %q missing expand=properties / startedAfter", wlReq[0].Query)
	}
	if !strings.Contains(wlReq[1].Query, "startAt=2") {
		t.Errorf("second worklog page query %q, want startAt=2", wlReq[1].Query)
	}
	for _, r := range mock.requests {
		if strings.Contains(r.Path, "/properties/") {
			t.Errorf("unexpected per-worklog property request %s (properties should be expanded inline)", r.Path)
		}
	}
}

func TestGetMyWorklogsForPeriodIncludesArchivedProjects(t *testing.T) {
	start := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 7)

	mock, client := newMockJira(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		switch r.URL.Path {
		case "/search/jql":
			writeTestJSON(w, http.StatusOK, map[string]interface{}{
				"issues": []interface{}{
					searchIssue("LIVE-1", "Task", 0),
					searchIssue("OLD-1", "Task", 0),
					searchIssue("HIDDEN-1", "Task", 0),
					searchIssue("FLAKY-1", "Task", 0),
					searchIssue("NOTIME-1", "Task", 0),
				},
				"isLast": true,
			})
		case "/issue/LIVE-1/worklog", "/issue/OLD-1/worklog", "/issue/HIDDEN-1/worklog", "/issue/FLAKY-1/worklog":
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"startAt": 0, "total": 1, "worklogs": []interface{}{
				worklog("1", "me", "2026-10-13T09:00:00.000+0000", 900, true),
			}})
		case "/issue/NOTIME-1/worklog":
			// Only someone else's time in range: no entries, so no project lookup
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"startAt": 0, "total": 1, "worklogs": []interface{}{
				worklog("2", "someone-else", "2026-10-13T09:00:00.000+0000", 900, false),
			}})
		case "/project/LIVE":
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"key": "LIVE", "archived": false})
		case "/project/OLD":
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"key": "OLD", "archived": true})
		case "/project/HIDDEN":
			// Jira may hide archived projects from this endpoint
			writeTestJSON(w, http.StatusNotFound, map[string]interface{}{"errorMessages": []string{"No project could be found"}})
		case "/project/FLAKY":
			writeTestJSON(w, http.StatusInternalServerError, map[string]interface{}{"errorMessages": []string{"boom"}})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			writeTestJSON(w, http.StatusNotFound, nil)
		}
	})

	for load := 1; load <= 2; load++ {
		events, err := client.GetMyWorklogsForPeriod(context.Background(), start, end, "me")
		if err != nil {
			t.Fatalf("load %d: GetMyWorklogsForPeriod: %v", load, err)
		}

		got := map[string]bool{}
		for _, e := range events {
			got[e.IssueKey] = e.ProjectArchived
		}
		// A failed lookup still shows the entry, as editable
		want := map[string]bool{"LIVE-1": false, "OLD-1": true, "HIDDEN-1": true, "FLAKY-1": false}
		if len(got) != len(want) {
			t.Errorf("load %d: got entries %v, want %v", load, got, want)
		}
		for key, archived := range want {
			if gotArchived, ok := got[key]; !ok || gotArchived != archived {
				t.Errorf("load %d: %s present=%v project_archived=%v, want project_archived=%v", load, key, ok, gotArchived, archived)
			}
		}
	}

	if search := mock.requestsTo("/search/jql")[0]; search.Body["includeArchivedProjects"] != true {
		t.Errorf("worklog search includeArchivedProjects = %v, want true (Jira leaves archived projects out by default)", search.Body["includeArchivedProjects"])
	}
	for _, project := range []string{"LIVE", "OLD", "HIDDEN"} {
		if n := len(mock.requestsTo("/project/" + project)); n != 1 {
			t.Errorf("project %s looked up %d times over two loads, want 1 (cached)", project, n)
		}
	}
	if n := len(mock.requestsTo("/project/NOTIME")); n != 0 {
		t.Errorf("project NOTIME looked up %d times, want 0 (no entries on it)", n)
	}
}

func TestGetMyWorklogsForPeriodFailsInsteadOfDroppingIssues(t *testing.T) {
	start := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)

	_, client := newMockJira(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		if r.URL.Path == "/search/jql" {
			writeTestJSON(w, http.StatusOK, map[string]interface{}{"issues": []interface{}{searchIssue("ABC-1", "Task", 0)}, "isLast": true})
			return
		}
		writeTestJSON(w, http.StatusInternalServerError, map[string]interface{}{"errorMessages": []string{"boom"}})
	})

	if _, err := client.GetMyWorklogsForPeriod(context.Background(), start, start.AddDate(0, 0, 7), "me"); err == nil {
		t.Fatal("got no error, want the worklog failure surfaced rather than the issue silently skipped")
	}
}

func TestDoReadRequestRetriesWhenRateLimited(t *testing.T) {
	calls := 0
	_, client := newMockJira(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		calls++
		if calls == 1 {
			writeTestJSON(w, http.StatusTooManyRequests, nil)
			return
		}
		writeTestJSON(w, http.StatusOK, map[string]interface{}{"accountId": "me"})
	})

	user, err := client.GetCurrentUser(context.Background())
	if err != nil {
		t.Fatalf("GetCurrentUser: %v", err)
	}
	if user.AccountID != "me" || calls != 2 {
		t.Errorf("got account %q after %d calls, want me after 2", user.AccountID, calls)
	}
}

func TestWritesAreNotRetried(t *testing.T) {
	calls := 0
	_, client := newMockJira(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
		calls++
		writeTestJSON(w, http.StatusTooManyRequests, nil)
	})

	if _, err := client.CreateWorklog(context.Background(), "ABC-1", time.Now(), 900, ""); err == nil {
		t.Error("got no error for a rate-limited create")
	}
	if calls != 1 {
		t.Errorf("create was sent %d times, want 1 (retrying could duplicate the worklog)", calls)
	}
}

func TestRetryDelay(t *testing.T) {
	resp := func(retryAfter string) *http.Response {
		r := &http.Response{Header: http.Header{}}
		if retryAfter != "" {
			r.Header.Set("Retry-After", retryAfter)
		}
		return r
	}

	cases := []struct {
		retryAfter string
		attempt    int
		want       time.Duration
	}{
		{"", 0, 500 * time.Millisecond},
		{"", 2, 2 * time.Second},
		{"3", 0, 3 * time.Second},
		{"120", 0, 10 * time.Second},
		{"not-a-number", 1, time.Second},
	}
	for _, c := range cases {
		if got := retryDelay(resp(c.retryAfter), c.attempt); got != c.want {
			t.Errorf("retryDelay(Retry-After=%q, attempt=%d) = %v, want %v", c.retryAfter, c.attempt, got, c.want)
		}
	}
}

func containsValue(values []interface{}, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func TestGetProjectSubtaskConfigToleratesOddFieldShapes(t *testing.T) {
	cases := []struct {
		name     string
		value    map[string]interface{}
		wantIDs  string
		wantMode string
	}{
		{"JiraMetadata label \"Non-Billable Sub-tasks\"", map[string]interface{}{"billable": "Yes", "billable_subtasks": nil, "nonbillable_subtasks": []string{"11457", "11490"}}, "11457,11490", SubtaskModeNonBillable},
		{"cleared field stored as empty string", map[string]interface{}{"billable": true, "billable_subtasks": "", "non_billable_subtasks": []string{"11492"}}, "11492", SubtaskModeNonBillable},
		{"IDs stored as numbers", map[string]interface{}{"billable": false, "billable_subtasks": []int{11457}}, "11457", SubtaskModeBillable},
		{"single ID stored as a string", map[string]interface{}{"billable": false, "billable_subtasks": "11457"}, "11457", SubtaskModeBillable},
		{"nothing configured", map[string]interface{}{"billable": true, "tags": []string{"PS"}}, "", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, client := newMockJira(t, func(w http.ResponseWriter, r *http.Request, body map[string]interface{}) {
				writeTestJSON(w, http.StatusOK, map[string]interface{}{"key": "jirametadata", "value": c.value})
			})

			cfg, err := client.GetProjectSubtaskConfig(context.Background(), "AC2643")
			if err != nil {
				t.Fatalf("GetProjectSubtaskConfig: %v", err)
			}
			if got := strings.Join(cfg.TypeIDs, ","); got != c.wantIDs || cfg.Mode != c.wantMode {
				t.Errorf("got %q (%s), want %q (%s)", got, cfg.Mode, c.wantIDs, c.wantMode)
			}
		})
	}
}
