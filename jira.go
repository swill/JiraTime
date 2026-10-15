package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/viper"
	"golang.org/x/oauth2"
)

const jiraAPIBase = "https://api.atlassian.com/ex/jira"

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

// SearchUsers searches for users by query string
func (c *JiraClient) SearchUsers(ctx context.Context, query string) ([]JiraUser, error) {
	endpoint := fmt.Sprintf("/user/search?query=%s&maxResults=20", query)
	resp, err := c.doRequest(ctx, "GET", endpoint, nil)
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
	resp, err := c.doRequest(ctx, "GET", "/myself", nil)
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
	// Limited to issues with recent activity to avoid showing stale tickets
	// Include Done issues for configurable period so users can still log time on recently completed work
	activeWeeks := viper.GetInt("ACTIVE_ISSUES_WEEKS")
	doneWeeks := viper.GetInt("DONE_ISSUES_WEEKS")

	// Use accountID directly instead of currentUser() to support impersonation
	jql := fmt.Sprintf("(assignee = \"%s\" OR reporter = \"%s\" OR worklogAuthor = \"%s\") AND updated >= -%dw AND (statusCategory != Done OR (statusCategory = Done AND statusCategoryChangedDate >= -%dw)) ORDER BY updated DESC", accountID, accountID, accountID, activeWeeks, doneWeeks)

	searchReq := map[string]interface{}{
		"jql":        jql,
		"fields":     []string{"summary", "project"},
		"maxResults": 100,
	}

	resp, err := c.doRequest(ctx, "POST", "/search/jql", searchReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("search issues failed: %d %s", resp.StatusCode, body)
	}

	var searchResp JiraIssueSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, err
	}

	// Group issues by project
	projectMap := make(map[string]*IssuesByProject)
	for _, issue := range searchResp.Issues {
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
	// Apply Done filter for consistency with Active Issues
	doneWeeks := viper.GetInt("DONE_ISSUES_WEEKS")

	// Use wildcard (*) for partial matching in summary, key, and description
	jql := fmt.Sprintf("(summary ~ \"%s*\" OR key ~ \"%s*\" OR description ~ \"%s*\") AND (statusCategory != Done OR (statusCategory = Done AND statusCategoryChangedDate >= -%dw)) ORDER BY updated DESC", query, query, query, doneWeeks)

	searchReq := map[string]interface{}{
		"jql":        jql,
		"fields":     []string{"summary", "project"},
		"maxResults": 100,
	}

	resp, err := c.doRequest(ctx, "POST", "/search/jql", searchReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("search issues failed: %d %s", resp.StatusCode, body)
	}

	var searchResp JiraIssueSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, err
	}

	var issues []Issue
	for _, issue := range searchResp.Issues {
		issues = append(issues, Issue{
			ID:         issue.ID,
			Key:        issue.Key,
			Summary:    issue.Fields.Summary,
			ProjectKey: issue.Fields.Project.Key,
		})
	}

	return issues, nil
}

func (c *JiraClient) GetWorklogs(ctx context.Context, issueKey string) ([]JiraWorklog, error) {
	endpoint := fmt.Sprintf("/issue/%s/worklog", issueKey)
	resp, err := c.doRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get worklogs failed: %d %s", resp.StatusCode, body)
	}

	var worklogResp JiraWorklogResponse
	if err := json.NewDecoder(resp.Body).Decode(&worklogResp); err != nil {
		return nil, err
	}

	return worklogResp.Worklogs, nil
}

func (c *JiraClient) GetMyWorklogsForPeriod(ctx context.Context, start, end time.Time, accountID string) ([]CalendarEvent, error) {
	// Search for issues with worklogs in the date range by the specified user
	startStr := start.Format("2006-01-02")
	endStr := end.Format("2006-01-02")

	// Use accountID directly instead of currentUser() to support impersonation
	jql := fmt.Sprintf("worklogDate >= %s AND worklogDate <= %s AND worklogAuthor = \"%s\"", startStr, endStr, accountID)

	searchReq := map[string]interface{}{
		"jql":        jql,
		"fields":     []string{"summary", "project", "issuetype", "parent"},
		"maxResults": 100,
	}

	resp, err := c.doRequest(ctx, "POST", "/search/jql", searchReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("search worklogs failed: %d %s", resp.StatusCode, body)
	}

	var searchResp JiraIssueSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, err
	}

	// Fetch worklogs for each issue and filter by user and date range
	var events []CalendarEvent
	for _, issue := range searchResp.Issues {
		worklogs, err := c.GetWorklogs(ctx, issue.Key)
		if err != nil {
			continue // Skip issues we can't get worklogs for
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

			// Check if worklog was created by JiraTime
			fromJiraTime := c.IsWorklogFromJiraTime(ctx, issue.Key, wl.ID)

			events = append(events, CalendarEvent{
				ID:              fmt.Sprintf("%s-%s", issue.Key, wl.ID),
				Title:           title,
				Start:           startTime,
				End:             startTime.Add(time.Duration(wl.TimeSpentSeconds) * time.Second),
				IssueKey:        issue.Key,
				IssueID:         issue.ID,
				WorklogID:       wl.ID,
				Description:     description,
				FromJiraTime:    fromJiraTime,
				ParentKey:       parentKey,
				SubtaskTypeID:   subtaskTypeID,
				SubtaskTypeName: subtaskTypeName,
			})
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

// IsWorklogFromJiraTime checks if a worklog was created by JiraTime
func (c *JiraClient) IsWorklogFromJiraTime(ctx context.Context, issueKey, worklogID string) bool {
	endpoint := fmt.Sprintf("/issue/%s/worklog/%s/properties/jiratime.source", issueKey, worklogID)

	resp, err := c.doRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	// If property exists, it's from JiraTime
	return resp.StatusCode == http.StatusOK
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

// GetProjectSubtaskConfig returns the sub-task configuration for a project from
// its jirametadata project property. A non-billable project may set billable
// sub-task types (billable_subtasks field); a billable project may set
// non-billable sub-task types (non_billable_subtasks field, or the hyphen-
// stripped nonbillable_subtasks alias). The two are mutually exclusive per
// project, so at most one mode is returned; an empty config means none are set.
func (c *JiraClient) GetProjectSubtaskConfig(ctx context.Context, projectKey string) (ProjectSubtaskConfig, error) {
	endpoint := fmt.Sprintf("/project/%s/properties/jirametadata", projectKey)

	resp, err := c.doRequest(ctx, "GET", endpoint, nil)
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

	resp, err := c.doRequest(ctx, "GET", endpoint, nil)
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
	resp, err := c.doRequest(ctx, "GET", "/issuetype", nil)
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

	resp, err := c.doRequest(ctx, "GET", endpoint, nil)
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
