package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

func (c *JiraClient) GetMyIssues(ctx context.Context) ([]IssuesByProject, error) {
	// JQL to get "active" issues: assigned to user, created by user, or user has logged time
	// Limited to issues with recent activity to avoid showing stale tickets
	// Include Done issues for configurable period so users can still log time on recently completed work
	activeWeeks := viper.GetInt("ACTIVE_ISSUES_WEEKS")
	doneWeeks := viper.GetInt("DONE_ISSUES_WEEKS")

	jql := fmt.Sprintf("(assignee = currentUser() OR reporter = currentUser() OR worklogAuthor = currentUser()) AND updated >= -%dw AND (statusCategory != Done OR (statusCategory = Done AND statusCategoryChangedDate >= -%dw)) ORDER BY updated DESC", activeWeeks, doneWeeks)

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

	jql := fmt.Sprintf("(summary ~ \"%s\" OR key = \"%s\" OR description ~ \"%s\") AND (statusCategory != Done OR (statusCategory = Done AND statusCategoryChangedDate >= -%dw)) ORDER BY updated DESC", query, query, query, doneWeeks)

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
	// Search for issues with worklogs in the date range by the current user
	startStr := start.Format("2006-01-02")
	endStr := end.Format("2006-01-02")

	jql := fmt.Sprintf("worklogDate >= %s AND worklogDate <= %s AND worklogAuthor = currentUser()", startStr, endStr)

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
				ID:          fmt.Sprintf("%s-%s", issue.Key, wl.ID),
				Title:       fmt.Sprintf("[%s] %s", issue.Key, issue.Fields.Summary),
				Start:       startTime,
				End:         startTime.Add(time.Duration(wl.TimeSpentSeconds) * time.Second),
				IssueKey:    issue.Key,
				IssueID:     issue.ID,
				WorklogID:   wl.ID,
				Description: description,
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

// GetIssueCustomFields fetches the current values of specified custom fields for an issue
func (c *JiraClient) GetIssueCustomFields(ctx context.Context, issueKey string, fieldIDs []string) (map[string]int, error) {
	// Build fields parameter
	fields := make([]string, len(fieldIDs))
	copy(fields, fieldIDs)

	endpoint := fmt.Sprintf("/issue/%s?fields=%s", issueKey, joinStrings(fields, ","))
	resp, err := c.doRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get issue custom fields failed: %d %s", resp.StatusCode, body)
	}

	var result struct {
		Fields map[string]interface{} `json:"fields"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	// Extract numeric values from custom fields
	// A field is considered "available" if it exists in the response (even if null)
	values := make(map[string]int)
	for _, fieldID := range fieldIDs {
		if val, ok := result.Fields[fieldID]; ok {
			// Field exists on this issue
			if val == nil {
				values[fieldID] = 0
			} else {
				switch v := val.(type) {
				case float64:
					values[fieldID] = int(v)
				case int:
					values[fieldID] = v
				default:
					// Field exists but has unexpected type, treat as 0
					values[fieldID] = 0
				}
			}
		}
		// If field key doesn't exist in response, it's not available on this issue
	}

	return values, nil
}

// UpdateIssueCustomField updates a numeric custom field on an issue
func (c *JiraClient) UpdateIssueCustomField(ctx context.Context, issueKey, fieldID string, value int) error {
	endpoint := fmt.Sprintf("/issue/%s", issueKey)

	body := map[string]interface{}{
		"fields": map[string]interface{}{
			fieldID: value,
		},
	}

	resp, err := c.doRequest(ctx, "PUT", endpoint, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("update issue custom field failed: %d %s", resp.StatusCode, respBody)
	}

	return nil
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

// GetWorklogProperty retrieves a property from a worklog
func (c *JiraClient) GetWorklogProperty(ctx context.Context, issueKey, worklogID, propertyKey string) (*CustomFieldContributions, error) {
	endpoint := fmt.Sprintf("/issue/%s/worklog/%s/properties/%s", issueKey, worklogID, propertyKey)

	resp, err := c.doRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// 404 means property doesn't exist - return empty contributions
	if resp.StatusCode == http.StatusNotFound {
		return &CustomFieldContributions{Contributions: make(map[string]int)}, nil
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get worklog property failed: %d %s", resp.StatusCode, body)
	}

	var result struct {
		Value CustomFieldContributions `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	if result.Value.Contributions == nil {
		result.Value.Contributions = make(map[string]int)
	}

	return &result.Value, nil
}

// DeleteWorklogProperty removes a property from a worklog
func (c *JiraClient) DeleteWorklogProperty(ctx context.Context, issueKey, worklogID, propertyKey string) error {
	endpoint := fmt.Sprintf("/issue/%s/worklog/%s/properties/%s", issueKey, worklogID, propertyKey)

	resp, err := c.doRequest(ctx, "DELETE", endpoint, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 404 is okay - property didn't exist
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}

	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete worklog property failed: %d %s", resp.StatusCode, body)
	}

	return nil
}

// joinStrings joins strings with a separator
func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}
