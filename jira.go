package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"golang.org/x/oauth2"
)

// jiraAPIBase is a variable so tests can point the client at a mock server
var jiraAPIBase = "https://api.atlassian.com/ex/jira"

type JiraClient struct {
	token   *oauth2.Token
	cloudID string
	client  *http.Client
}

func NewJiraClient(token *oauth2.Token, cloudID string) *JiraClient {
	return &JiraClient{
		token:   token,
		cloudID: cloudID,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *JiraClient) baseURL() string {
	return fmt.Sprintf("%s/%s/rest/api/3", jiraAPIBase, c.cloudID)
}

func (c *JiraClient) doRequest(ctx context.Context, method, endpoint string, body interface{}) (*http.Response, error) {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL()+endpoint, reqBody)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+c.token.AccessToken)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.client.Do(req)
}

const maxReadRetries = 3

// doReadRequest is doRequest for read-only calls, retried when Jira is rate
// limiting (429) or briefly unavailable (502/503/504). Writes are not retried
// since repeating one could duplicate a worklog.
func (c *JiraClient) doReadRequest(ctx context.Context, method, endpoint string, body interface{}) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := c.doRequest(ctx, method, endpoint, body)
		if err != nil || attempt == maxReadRetries || !isRetryableStatus(resp.StatusCode) {
			return resp, err
		}

		wait := retryDelay(resp, attempt)
		resp.Body.Close()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

func isRetryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// retryDelay honours Jira's Retry-After header (in seconds), falling back to
// exponential backoff, capped so a request never stalls for long
func retryDelay(resp *http.Response, attempt int) time.Duration {
	delay := time.Duration(500<<attempt) * time.Millisecond // 0.5s, 1s, 2s
	if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
		delay = time.Duration(secs) * time.Second
	}
	return min(delay, 10*time.Second)
}

// archivedProjects controls whether a search returns issues in archived
// projects, which Jira leaves out of search results by default
type archivedProjects bool

const (
	excludeArchivedProjects archivedProjects = false
	includeArchivedProjects archivedProjects = true
)

// searchIssues runs a JQL search, following nextPageToken until limit issues
// have been collected (limit <= 0 means all of them). Jira can return fewer
// issues per page than requested, so one page is not necessarily everything.
func (c *JiraClient) searchIssues(ctx context.Context, jql string, fields []string, limit int, archived archivedProjects) ([]JiraSearchIssue, error) {
	const pageSize = 100

	var issues []JiraSearchIssue
	nextPageToken := ""
	for {
		maxResults := pageSize
		if limit > 0 {
			maxResults = min(pageSize, limit-len(issues))
		}

		searchReq := map[string]interface{}{
			"jql":        jql,
			"fields":     fields,
			"maxResults": maxResults,
		}
		if nextPageToken != "" {
			searchReq["nextPageToken"] = nextPageToken
		}
		if archived == includeArchivedProjects {
			searchReq["includeArchivedProjects"] = true
		}

		resp, err := c.doReadRequest(ctx, "POST", "/search/jql", searchReq)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("search issues failed: %d %s", resp.StatusCode, body)
		}

		var page JiraIssueSearchResponse
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		issues = append(issues, page.Issues...)
		if page.IsLast || page.NextPageToken == "" || (limit > 0 && len(issues) >= limit) {
			return issues, nil
		}
		nextPageToken = page.NextPageToken
	}
}

// isEpicLevel reports whether an issue type sits at Epic level or above in the
// issue hierarchy. Time is not logged against these, so they are left out of
// the issue pickers. Uses the hierarchy level rather than the name so renamed
// Epic types are still caught.
func isEpicLevel(issueType JiraIssueTypeRef) bool {
	return issueType.HierarchyLevel >= 1
}

// doneIssuesJQL limits Done issues to those completed within DONE_ISSUES_DAYS,
// so recently finished work can still have time logged against it
func doneIssuesJQL() string {
	return fmt.Sprintf("(statusCategory != Done OR statusCategoryChangedDate >= -%dd)", viper.GetInt("DONE_ISSUES_DAYS"))
}

// SearchUsers searches for users by query string
func (c *JiraClient) SearchUsers(ctx context.Context, query string) ([]JiraUser, error) {
	endpoint := fmt.Sprintf("/user/search?query=%s&maxResults=20", query)
	resp, err := c.doReadRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("search users failed: %d %s", resp.StatusCode, body)
	}

	var users []JiraUser
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return nil, err
	}

	return users, nil
}

func (c *JiraClient) GetCurrentUser(ctx context.Context) (*JiraUser, error) {
	resp, err := c.doReadRequest(ctx, "GET", "/myself", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get current user failed: %d %s", resp.StatusCode, body)
	}

	var user JiraUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}

	return &user, nil
}

func (c *JiraClient) GetMyIssues(ctx context.Context, accountID string) ([]IssuesByProject, error) {
	// JQL to get "active" issues: assigned to user, created by user, or user has logged time
	// Limited to issues with recent activity to avoid showing stale tickets.
	// Done issues are included for DONE_ISSUES_DAYS, but still need activity
	// within ACTIVE_ISSUES_WEEKS, so they leave the sidebar sooner than search.
	activeWeeks := viper.GetInt("ACTIVE_ISSUES_WEEKS")

	// Use accountID directly instead of currentUser() to support impersonation
	jql := fmt.Sprintf("(assignee = \"%s\" OR reporter = \"%s\" OR worklogAuthor = \"%s\") AND updated >= -%dw AND %s ORDER BY updated DESC", accountID, accountID, accountID, activeWeeks, doneIssuesJQL())

	issues, err := c.searchIssues(ctx, jql, []string{"summary", "project", "issuetype"}, 100, excludeArchivedProjects)
	if err != nil {
		return nil, err
	}

	// Group issues by project
	projectMap := make(map[string]*IssuesByProject)
	for _, issue := range issues {
		if isEpicLevel(issue.Fields.IssueType) {
			continue
		}

		projectKey := issue.Fields.Project.Key
		if _, ok := projectMap[projectKey]; !ok {
			projectMap[projectKey] = &IssuesByProject{
				Project: Project{
					ID:   issue.Fields.Project.ID,
					Key:  issue.Fields.Project.Key,
					Name: issue.Fields.Project.Name,
				},
				Issues: []Issue{},
			}
		}
		projectMap[projectKey].Issues = append(projectMap[projectKey].Issues, Issue{
			ID:         issue.ID,
			Key:        issue.Key,
			Summary:    issue.Fields.Summary,
			ProjectKey: projectKey,
		})
	}

	// Convert map to slice
	result := make([]IssuesByProject, 0, len(projectMap))
	for _, ibp := range projectMap {
		result = append(result, *ibp)
	}

	return result, nil
}

func (c *JiraClient) SearchIssues(ctx context.Context, query string) ([]Issue, error) {
	// Search for issues by text query (summary, key, or description)
	// Done issues stay searchable for DONE_ISSUES_DAYS after completion

	// Use wildcard (*) for partial matching in summary, key, and description
	jql := fmt.Sprintf("(summary ~ \"%s*\" OR key ~ \"%s*\" OR description ~ \"%s*\") AND %s ORDER BY updated DESC", query, query, query, doneIssuesJQL())

	results, err := c.searchIssues(ctx, jql, []string{"summary", "project", "issuetype"}, 100, excludeArchivedProjects)
	if err != nil {
		return nil, err
	}

	var issues []Issue
	for _, issue := range results {
		if isEpicLevel(issue.Fields.IssueType) {
			continue
		}

		issues = append(issues, Issue{
			ID:         issue.ID,
			Key:        issue.Key,
			Summary:    issue.Fields.Summary,
			ProjectKey: issue.Fields.Project.Key,
		})
	}

	return issues, nil
}

// errIssueNotFound is returned when an issue no longer exists (or is no longer
// visible to the user)
var errIssueNotFound = errors.New("issue not found")

// GetWorklogs returns an issue's worklogs started between startedAfter and
// startedBefore, following pagination. Properties are expanded inline so the
// JiraTime source marker needs no extra request per worklog.
func (c *JiraClient) GetWorklogs(ctx context.Context, issueKey string, startedAfter, startedBefore time.Time) ([]JiraWorklog, error) {
	var worklogs []JiraWorklog
	for {
		endpoint := fmt.Sprintf("/issue/%s/worklog?startAt=%d&startedAfter=%d&startedBefore=%d&expand=properties",
			issueKey, len(worklogs), startedAfter.UnixMilli(), startedBefore.UnixMilli())
		resp, err := c.doReadRequest(ctx, "GET", endpoint, nil)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			return nil, errIssueNotFound
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("get worklogs failed: %d %s", resp.StatusCode, body)
		}

		var page JiraWorklogResponse
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		worklogs = append(worklogs, page.Worklogs...)
		if len(page.Worklogs) == 0 || len(worklogs) >= page.Total {
			return worklogs, nil
		}
	}
}

// hasProperty reports whether a worklog carries the given property (requires
// the worklog to have been fetched with expand=properties)
func (wl *JiraWorklog) hasProperty(key string) bool {
	for _, p := range wl.Properties {
		if p.Key == key {
			return true
		}
	}
	return false
}

func (c *JiraClient) GetMyWorklogsForPeriod(ctx context.Context, start, end time.Time, accountID string) ([]CalendarEvent, error) {
	// Search for issues with worklogs in the date range by the specified user.
	// The window is widened by a day each side because worklogDate is evaluated
	// in the user's Jira profile timezone, which may differ from the browser's;
	// worklogs are filtered to the exact range below.
	startStr := start.AddDate(0, 0, -1).Format("2006-01-02")
	endStr := end.AddDate(0, 0, 1).Format("2006-01-02")

	// Use accountID directly instead of currentUser() to support impersonation
	jql := fmt.Sprintf("worklogDate >= %s AND worklogDate <= %s AND worklogAuthor = \"%s\"", startStr, endStr, accountID)

	// Archived projects are included so time logged on them stays on the
	// calendar and in the hours total (Jira hides them from search otherwise)
	issues, err := c.searchIssues(ctx, jql, []string{"summary", "project", "issuetype", "parent"}, 0, includeArchivedProjects)
	if err != nil {
		return nil, err
	}

	// Fetch worklogs for each issue and filter by user and date range
	var events []CalendarEvent
	for _, issue := range issues {
		worklogs, err := c.GetWorklogs(ctx, issue.Key, start.Add(-time.Minute), end.Add(time.Minute))
		if errors.Is(err, errIssueNotFound) {
			continue // Deleted since the search ran
		}
		if err != nil {
			// Fail visibly: silently skipping the issue would show an
			// incomplete calendar and hours total
			return nil, fmt.Errorf("failed to get worklogs for %s: %w", issue.Key, err)
		}

		// Detect worklogs living on configured sub-tasks (billable or
		// non-billable) so the calendar can show them under their parent issue
		// (works for time logged in Jira directly too)
		title := fmt.Sprintf("[%s] %s", issue.Key, issue.Fields.Summary)
		parentKey := ""
		subtaskTypeID := ""
		subtaskTypeName := ""
		if issue.Fields.IssueType.Subtask && issue.Fields.Parent != nil {
			configuredTypes, _, err := c.GetConfiguredSubtaskTypes(ctx, issue.Fields.Project.Key)
			if err == nil && subtaskTypeByID(configuredTypes, issue.Fields.IssueType.ID) != nil {
				parentKey = issue.Fields.Parent.Key
				subtaskTypeID = issue.Fields.IssueType.ID
				subtaskTypeName = issue.Fields.IssueType.Name
				title = fmt.Sprintf("[%s] %s • %s", issue.Fields.Parent.Key, issue.Fields.Parent.Fields.Summary, subtaskTypeName)
			}
		}

		issueStart := len(events)
		for _, wl := range worklogs {
			// Filter by current user
			if wl.Author.AccountID != accountID {
				continue
			}

			// Parse worklog start time
			startTime, err := parseJiraDateTime(wl.Started)
			if err != nil {
				continue
			}

			// Filter by date range
			if startTime.Before(start) || startTime.After(end) {
				continue
			}

			// Extract comment text
			description := ""
			if wl.Comment != nil && len(wl.Comment.Content) > 0 {
				if len(wl.Comment.Content[0].Content) > 0 {
					description = wl.Comment.Content[0].Content[0].Text
				}
			}

			events = append(events, CalendarEvent{
				ID:              fmt.Sprintf("%s-%s", issue.Key, wl.ID),
				Title:           title,
				Start:           startTime,
				End:             startTime.Add(time.Duration(wl.TimeSpentSeconds) * time.Second),
				IssueKey:        issue.Key,
				IssueID:         issue.ID,
				WorklogID:       wl.ID,
				Description:     description,
				FromJiraTime:    wl.hasProperty(worklogSourcePropertyKey),
				ParentKey:       parentKey,
				SubtaskTypeID:   subtaskTypeID,
				SubtaskTypeName: subtaskTypeName,
			})
		}

		// Issues in archived projects are read-only in Jira, so their entries
		// are shown but cannot be changed
		if len(events) > issueStart {
			archived, err := c.GetProjectArchivedCached(ctx, issue.Fields.Project.Key)
			if err != nil {
				// Still show the entries; Jira rejects any change if it is archived
				logrus.Warnf("Could not check whether project %s is archived: %v", issue.Fields.Project.Key, err)
			}
			for i := issueStart; i < len(events); i++ {
				events[i].ProjectArchived = archived
			}
		}
	}

	return events, nil
}

func (c *JiraClient) CreateWorklog(ctx context.Context, issueKey string, started time.Time, durationSeconds int, description string) (*JiraWorklog, error) {
	endpoint := fmt.Sprintf("/issue/%s/worklog", issueKey)

	body := map[string]interface{}{
		"started":          formatJiraDateTime(started),
		"timeSpentSeconds": durationSeconds,
	}

	if description != "" {
		body["comment"] = buildADFComment(description)
	}

	resp, err := c.doRequest(ctx, "POST", endpoint, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create worklog failed: %d %s", resp.StatusCode, respBody)
	}

	var worklog JiraWorklog
	if err := json.NewDecoder(resp.Body).Decode(&worklog); err != nil {
		return nil, err
	}

	return &worklog, nil
}

func (c *JiraClient) UpdateWorklog(ctx context.Context, issueKey, worklogID string, started time.Time, durationSeconds int, description string) (*JiraWorklog, error) {
	endpoint := fmt.Sprintf("/issue/%s/worklog/%s", issueKey, worklogID)

	body := map[string]interface{}{
		"started":          formatJiraDateTime(started),
		"timeSpentSeconds": durationSeconds,
	}

	if description != "" {
		body["comment"] = buildADFComment(description)
	}

	resp, err := c.doRequest(ctx, "PUT", endpoint, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("update worklog failed: %d %s", resp.StatusCode, respBody)
	}

	var worklog JiraWorklog
	if err := json.NewDecoder(resp.Body).Decode(&worklog); err != nil {
		return nil, err
	}

	return &worklog, nil
}

func (c *JiraClient) DeleteWorklog(ctx context.Context, issueKey, worklogID string) error {
	endpoint := fmt.Sprintf("/issue/%s/worklog/%s", issueKey, worklogID)

	resp, err := c.doRequest(ctx, "DELETE", endpoint, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete worklog failed: %d %s", resp.StatusCode, body)
	}

	return nil
}

// parseJiraDateTime parses Jira's datetime format: 2021-01-17T12:34:00.000+0000
func parseJiraDateTime(s string) (time.Time, error) {
	// Try multiple formats as Jira can return different formats
	formats := []string{
		"2006-01-02T15:04:05.000-0700",
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05-0700",
		"2006-01-02T15:04:05Z",
	}

	for _, format := range formats {
		if t, err := time.Parse(format, s); err == nil {
			return t, nil
		}
	}

	return time.Time{}, fmt.Errorf("unable to parse datetime: %s", s)
}

// formatJiraDateTime formats time for Jira API
func formatJiraDateTime(t time.Time) string {
	return t.Format("2006-01-02T15:04:05.000-0700")
}

// buildADFComment creates an Atlassian Document Format comment
func buildADFComment(text string) map[string]interface{} {
	return map[string]interface{}{
		"type":    "doc",
		"version": 1,
		"content": []map[string]interface{}{
			{
				"type": "paragraph",
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": text,
					},
				},
			},
		},
	}
}

// SetWorklogProperty stores a property on a worklog
func (c *JiraClient) SetWorklogProperty(ctx context.Context, issueKey, worklogID, propertyKey string, value interface{}) error {
	endpoint := fmt.Sprintf("/issue/%s/worklog/%s/properties/%s", issueKey, worklogID, propertyKey)

	resp, err := c.doRequest(ctx, "PUT", endpoint, value)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("set worklog property failed: %d %s", resp.StatusCode, respBody)
	}

	return nil
}

// flexBool decodes a JSON boolean that a loosely-coupled metadata source (the
// jirametadata Forge app is configured per deployment) may instead store as a
// string ("Yes"/"true"/"1") or a number. Decoding it as a plain bool would fail
// the whole property parse when the value is anything but a real boolean, which
// would silently drop every project's sub-task config; this keeps one
// unexpectedly-typed field from breaking the decode.
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	var boolVal bool
	if err := json.Unmarshal(data, &boolVal); err == nil {
		*b = flexBool(boolVal)
		return nil
	}
	var strVal string
	if err := json.Unmarshal(data, &strVal); err == nil {
		switch strings.ToLower(strings.TrimSpace(strVal)) {
		case "yes", "y", "true", "t", "1", "on":
			*b = true
		default:
			*b = false
		}
		return nil
	}
	var numVal float64
	if err := json.Unmarshal(data, &numVal); err == nil {
		*b = numVal != 0
		return nil
	}
	// null or any other shape: treat as false rather than failing the decode
	*b = false
	return nil
}

// GetProjectArchived reports whether a project is archived. A project that
// cannot be found is reported as archived: this is only asked about projects
// whose issues were just read, so a missing project means Jira is hiding it
// (or it was deleted moments ago, in which case its entries are gone on the
// next load anyway).
func (c *JiraClient) GetProjectArchived(ctx context.Context, projectKey string) (bool, error) {
	endpoint := fmt.Sprintf("/project/%s", projectKey)

	resp, err := c.doReadRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return true, nil
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("get project failed: %d %s", resp.StatusCode, body)
	}

	var project struct {
		Archived bool `json:"archived"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&project); err != nil {
		return false, err
	}

	return project.Archived, nil
}

// GetProjectArchivedCached is GetProjectArchived with a site-level cache
func (c *JiraClient) GetProjectArchivedCached(ctx context.Context, projectKey string) (bool, error) {
	if archived, ok := cache.GetProjectArchived(c.cloudID, projectKey); ok {
		return archived, nil
	}

	archived, err := c.GetProjectArchived(ctx, projectKey)
	if err != nil {
		return false, err
	}
	cache.SetProjectArchived(c.cloudID, projectKey, archived)

	return archived, nil
}

// GetProjectSubtaskConfig returns the sub-task configuration for a project from
// its jirametadata project property. A non-billable project may set billable
// sub-task types (billable_subtasks field); a billable project may set
// non-billable sub-task types (non_billable_subtasks field, or the hyphen-
// stripped nonbillable_subtasks alias). The two are mutually exclusive per
// project, so at most one mode is returned; an empty config means none are set.
func (c *JiraClient) GetProjectSubtaskConfig(ctx context.Context, projectKey string) (ProjectSubtaskConfig, error) {
	endpoint := fmt.Sprintf("/project/%s/properties/jirametadata", projectKey)

	resp, err := c.doReadRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return ProjectSubtaskConfig{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// Property not set for this project - no sub-tasks configured
		return ProjectSubtaskConfig{}, nil
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return ProjectSubtaskConfig{}, fmt.Errorf("get project property failed: %d %s", resp.StatusCode, body)
	}

	var prop struct {
		Value struct {
			Billable            flexBool `json:"billable"`
			BillableSubtasks    []string `json:"billable_subtasks"`
			NonBillableSubtasks []string `json:"non_billable_subtasks"`
			// Alias for admins who label the field "Non-Billable Subtasks":
			// JiraMetadata strips the hyphen when deriving the property key
			NonBillableSubtasksAlt []string `json:"nonbillable_subtasks"`
		} `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&prop); err != nil {
		return ProjectSubtaskConfig{}, err
	}

	nonBillable := prop.Value.NonBillableSubtasks
	if len(nonBillable) == 0 {
		nonBillable = prop.Value.NonBillableSubtasksAlt
	}
	hasBillable := len(prop.Value.BillableSubtasks) > 0
	hasNonBillable := len(nonBillable) > 0

	switch {
	case hasBillable && hasNonBillable:
		// Both configured (a project should be one or the other). The project's
		// billable flag is authoritative: billable projects use non-billable
		// sub-tasks, non-billable projects use billable sub-tasks.
		if bool(prop.Value.Billable) {
			return ProjectSubtaskConfig{TypeIDs: nonBillable, Mode: SubtaskModeNonBillable}, nil
		}
		return ProjectSubtaskConfig{TypeIDs: prop.Value.BillableSubtasks, Mode: SubtaskModeBillable}, nil
	case hasNonBillable:
		return ProjectSubtaskConfig{TypeIDs: nonBillable, Mode: SubtaskModeNonBillable}, nil
	case hasBillable:
		return ProjectSubtaskConfig{TypeIDs: prop.Value.BillableSubtasks, Mode: SubtaskModeBillable}, nil
	default:
		return ProjectSubtaskConfig{}, nil
	}
}

// GetProjectSubtaskConfigCached is GetProjectSubtaskConfig with a site-level
// cache (project properties change rarely)
func (c *JiraClient) GetProjectSubtaskConfigCached(ctx context.Context, projectKey string) (ProjectSubtaskConfig, error) {
	if cfg, ok := cache.GetProjectSubtaskConfig(c.cloudID, projectKey); ok {
		return cfg, nil
	}

	cfg, err := c.GetProjectSubtaskConfig(ctx, projectKey)
	if err != nil {
		return ProjectSubtaskConfig{}, err
	}
	cache.SetProjectSubtaskConfig(c.cloudID, projectKey, cfg)

	return cfg, nil
}

// GetIssueTypeNamesCached is GetIssueTypeNames with a site-level cache
func (c *JiraClient) GetIssueTypeNamesCached(ctx context.Context) (map[string]string, error) {
	if names, ok := cache.GetIssueTypes(c.cloudID); ok {
		return names, nil
	}

	names, err := c.GetIssueTypeNames(ctx)
	if err != nil {
		return nil, err
	}
	cache.SetIssueTypes(c.cloudID, names)

	return names, nil
}

// GetProjectIssueTypes returns the issue types that can be created in a
// project (from createmeta - respects the project's issue type scheme)
func (c *JiraClient) GetProjectIssueTypes(ctx context.Context, projectKey string) ([]JiraIssueType, error) {
	endpoint := fmt.Sprintf("/issue/createmeta/%s/issuetypes?maxResults=200", projectKey)

	resp, err := c.doReadRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get project issue types failed: %d %s", resp.StatusCode, body)
	}

	var meta struct {
		IssueTypes []JiraIssueType `json:"issueTypes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return nil, err
	}

	return meta.IssueTypes, nil
}

// GetProjectIssueTypesCached is GetProjectIssueTypes with a site-level cache
func (c *JiraClient) GetProjectIssueTypesCached(ctx context.Context, projectKey string) ([]JiraIssueType, error) {
	if types, ok := cache.GetProjectIssueTypes(c.cloudID, projectKey); ok {
		return types, nil
	}

	types, err := c.GetProjectIssueTypes(ctx, projectKey)
	if err != nil {
		return nil, err
	}
	cache.SetProjectIssueTypes(c.cloudID, projectKey, types)

	return types, nil
}

// GetConfiguredSubtaskTypes resolves a project's configured sub-task types
// (billable_subtasks or non_billable_subtasks) into sub-task types that are
// actually creatable in the project, along with the mode they represent. The
// configured IDs are matched against the project's issue type scheme by ID
// first, then by name - team-managed (next-gen) projects have their own
// per-project type IDs, so a same-named sub-task type still maps correctly.
// Configured types with no match in the project are omitted.
func (c *JiraClient) GetConfiguredSubtaskTypes(ctx context.Context, projectKey string) ([]SubtaskType, string, error) {
	cfg, err := c.GetProjectSubtaskConfigCached(ctx, projectKey)
	if err != nil {
		return nil, "", err
	}
	if len(cfg.TypeIDs) == 0 {
		return []SubtaskType{}, "", nil
	}

	projectTypes, err := c.GetProjectIssueTypesCached(ctx, projectKey)
	if err != nil {
		return nil, "", err
	}

	globalNames, err := c.GetIssueTypeNamesCached(ctx)
	if err != nil {
		return nil, "", err
	}

	result := []SubtaskType{}
	seen := make(map[string]bool)
	for _, id := range cfg.TypeIDs {
		var match *JiraIssueType

		for i, pt := range projectTypes {
			if pt.Subtask && pt.ID == id {
				match = &projectTypes[i]
				break
			}
		}

		if match == nil {
			// Fall back to matching the configured type's name against the
			// project's sub-task types
			name := globalNames[id]
			if name == "" {
				continue
			}
			for i, pt := range projectTypes {
				if pt.Subtask && strings.EqualFold(pt.Name, name) {
					match = &projectTypes[i]
					break
				}
			}
		}

		if match == nil || seen[match.ID] {
			continue
		}
		seen[match.ID] = true
		result = append(result, SubtaskType{ID: match.ID, Name: match.Name})
	}

	return result, cfg.Mode, nil
}

// subtaskTypeByID returns the type with the given ID, or nil
func subtaskTypeByID(types []SubtaskType, id string) *SubtaskType {
	for i := range types {
		if types[i].ID == id {
			return &types[i]
		}
	}
	return nil
}

// GetIssueTypeNames returns a map of issue type ID -> name for the Jira site
func (c *JiraClient) GetIssueTypeNames(ctx context.Context) (map[string]string, error) {
	resp, err := c.doReadRequest(ctx, "GET", "/issuetype", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get issue types failed: %d %s", resp.StatusCode, body)
	}

	var issueTypes []JiraIssueType
	if err := json.NewDecoder(resp.Body).Decode(&issueTypes); err != nil {
		return nil, err
	}

	names := make(map[string]string, len(issueTypes))
	for _, t := range issueTypes {
		names[t.ID] = t.Name
	}

	return names, nil
}

// GetIssueDetail fetches a single issue with hierarchy fields (issue type, parent, sub-tasks)
func (c *JiraClient) GetIssueDetail(ctx context.Context, issueKey string) (*JiraIssueDetail, error) {
	endpoint := fmt.Sprintf("/issue/%s?fields=summary,project,issuetype,parent,subtasks", issueKey)

	resp, err := c.doReadRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get issue failed: %d %s", resp.StatusCode, body)
	}

	var detail JiraIssueDetail
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		return nil, err
	}

	return &detail, nil
}

// CreateSubtask creates a sub-task of the given issue type under a parent issue
// and returns its key
func (c *JiraClient) CreateSubtask(ctx context.Context, projectID, parentKey, issueTypeID, summary string) (string, error) {
	body := map[string]interface{}{
		"fields": map[string]interface{}{
			"project":   map[string]string{"id": projectID},
			"parent":    map[string]string{"key": parentKey},
			"issuetype": map[string]string{"id": issueTypeID},
			"summary":   summary,
		},
	}

	resp, err := c.doRequest(ctx, "POST", "/issue", body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create sub-task failed: %d %s", resp.StatusCode, respBody)
	}

	var created struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return "", err
	}

	return created.Key, nil
}
