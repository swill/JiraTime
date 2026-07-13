package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// getEffectiveAccountID returns the impersonated account ID if impersonation is active,
// otherwise returns the session's own account ID
func getEffectiveAccountID(session *UserSession) string {
	if session.ImpersonatingID != "" {
		return session.ImpersonatingID
	}
	return session.AccountID
}

// isImpersonating returns true if the session is currently impersonating another user
func isImpersonating(session *UserSession) bool {
	return session.ImpersonatingID != ""
}

func handleGetEvents(w http.ResponseWriter, r *http.Request) {
	session := getSessionFromRequest(r)
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	startStr := r.URL.Query().Get("start")
	endStr := r.URL.Query().Get("end")

	start, err := time.Parse(time.RFC3339, startStr)
	if err != nil {
		http.Error(w, "Invalid start date", http.StatusBadRequest)
		return
	}

	end, err := time.Parse(time.RFC3339, endStr)
	if err != nil {
		http.Error(w, "Invalid end date", http.StatusBadRequest)
		return
	}

	client := NewJiraClient(session.Token, session.CloudID)
	effectiveAccountID := getEffectiveAccountID(session)
	events, err := client.GetMyWorklogsForPeriod(r.Context(), start, end, effectiveAccountID)
	if err != nil {
		logrus.Errorf("Failed to get worklogs: %v", err)
		http.Error(w, "Failed to get events", http.StatusInternalServerError)
		return
	}

	writeJSON(w, events)
}

func handleCreateEvent(w http.ResponseWriter, r *http.Request) {
	session := getSessionFromRequest(r)
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Block modifications when impersonating (view-only mode)
	if isImpersonating(session) {
		http.Error(w, "Cannot create events while impersonating another user", http.StatusForbidden)
		return
	}

	var req CreateEventReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	start, err := time.Parse(time.RFC3339, req.Start)
	if err != nil {
		http.Error(w, "Invalid start date", http.StatusBadRequest)
		return
	}

	durationSeconds := req.DurationMin * 60

	client := NewJiraClient(session.Token, session.CloudID)

	// Route the worklog to a billable sub-task when one was selected
	targetKey, err := resolveWorklogTarget(r.Context(), client, req.IssueKey, req.SubtaskTypeID, req.SubtaskKey)
	if err != nil {
		logrus.Errorf("Failed to resolve worklog target: %v", err)
		http.Error(w, fmt.Sprintf("Failed to resolve sub-task: %v", err), http.StatusInternalServerError)
		return
	}

	worklog, err := client.CreateWorklog(r.Context(), targetKey, start, durationSeconds, req.Description)
	if err != nil {
		logrus.Errorf("Failed to create worklog: %v", err)
		http.Error(w, fmt.Sprintf("Failed to create worklog: %v", err), http.StatusInternalServerError)
		return
	}

	// Mark this worklog as created by JiraTime
	if err := client.SetWorklogProperty(r.Context(), targetKey, worklog.ID, worklogSourcePropertyKey, WorklogSource{CreatedBy: "jiratime"}); err != nil {
		logrus.Warnf("Failed to set worklog source property: %v", err)
		// Don't fail - worklog was created successfully
	}

	// Return the created event
	event := CalendarEvent{
		ID:           targetKey + "-" + worklog.ID,
		Title:        "[" + targetKey + "]",
		Start:        start,
		End:          start.Add(time.Duration(durationSeconds) * time.Second),
		IssueKey:     targetKey,
		WorklogID:    worklog.ID,
		Description:  req.Description,
		FromJiraTime: true,
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, event)
}

func handleUpdateEvent(w http.ResponseWriter, r *http.Request) {
	session := getSessionFromRequest(r)
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Block modifications when impersonating (view-only mode)
	if isImpersonating(session) {
		http.Error(w, "Cannot update events while impersonating another user", http.StatusForbidden)
		return
	}

	// Extract event ID from URL path: /api/events/{id}
	eventID := strings.TrimPrefix(r.URL.Path, "/api/events/")
	if eventID == "" {
		http.Error(w, "Missing event ID", http.StatusBadRequest)
		return
	}

	// Parse event ID: {issueKey}-{worklogID}
	lastDash := strings.LastIndex(eventID, "-")
	if lastDash == -1 {
		http.Error(w, "Invalid event ID format", http.StatusBadRequest)
		return
	}
	issueKey := eventID[:lastDash]
	worklogID := eventID[lastDash+1:]

	var req UpdateEventReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	start, err := time.Parse(time.RFC3339, req.Start)
	if err != nil {
		http.Error(w, "Invalid start date", http.StatusBadRequest)
		return
	}

	durationSeconds := req.DurationMin * 60

	client := NewJiraClient(session.Token, session.CloudID)

	// Dialog saves send ParentKey so the sub-task association can change; when
	// the target differs from where the worklog lives, it has to move
	// (drag/resize updates omit ParentKey and leave the association alone)
	if req.ParentKey != "" {
		targetKey, err := resolveWorklogTarget(r.Context(), client, req.ParentKey, req.SubtaskTypeID, req.SubtaskKey)
		if err != nil {
			logrus.Errorf("Failed to resolve worklog target: %v", err)
			http.Error(w, fmt.Sprintf("Failed to resolve sub-task: %v", err), http.StatusInternalServerError)
			return
		}

		if targetKey != issueKey {
			newID, err := moveWorklog(r.Context(), client, issueKey, worklogID, targetKey, start, durationSeconds, req.Description)
			if err != nil {
				logrus.Errorf("Failed to move worklog: %v", err)
				http.Error(w, fmt.Sprintf("Failed to move worklog: %v", err), http.StatusInternalServerError)
				return
			}

			w.WriteHeader(http.StatusOK)
			writeJSON(w, map[string]string{"status": "ok", "id": newID})
			return
		}
	}

	_, err = client.UpdateWorklog(r.Context(), issueKey, worklogID, start, durationSeconds, req.Description)
	if err != nil {
		logrus.Errorf("Failed to update worklog: %v", err)
		http.Error(w, fmt.Sprintf("Failed to update worklog: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	writeJSON(w, map[string]string{"status": "ok"})
}

// moveWorklog re-homes a worklog onto another issue. Jira has no move API, so
// this creates the worklog on the target first and then deletes the original -
// the time is never silently lost, and a failed delete is rolled back so it is
// never doubled either. Returns the new event ID.
func moveWorklog(ctx context.Context, client *JiraClient, fromKey, worklogID, toKey string, start time.Time, durationSeconds int, description string) (string, error) {
	worklog, err := client.CreateWorklog(ctx, toKey, start, durationSeconds, description)
	if err != nil {
		return "", fmt.Errorf("failed to create worklog on %s: %w", toKey, err)
	}

	if err := client.SetWorklogProperty(ctx, toKey, worklog.ID, worklogSourcePropertyKey, WorklogSource{CreatedBy: "jiratime"}); err != nil {
		logrus.Warnf("Failed to set worklog source property: %v", err)
	}

	if err := client.DeleteWorklog(ctx, fromKey, worklogID); err != nil {
		// Best-effort rollback of the copy so time is not double-counted
		if rbErr := client.DeleteWorklog(ctx, toKey, worklog.ID); rbErr != nil {
			return "", fmt.Errorf("failed to remove original worklog from %s AND failed to roll back the copy on %s - time may be logged twice, please fix in Jira: %v", fromKey, toKey, err)
		}
		return "", fmt.Errorf("failed to remove original worklog from %s (change rolled back): %w", fromKey, err)
	}

	return toKey + "-" + worklog.ID, nil
}

func handleDeleteEvent(w http.ResponseWriter, r *http.Request) {
	session := getSessionFromRequest(r)
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Block modifications when impersonating (view-only mode)
	if isImpersonating(session) {
		http.Error(w, "Cannot delete events while impersonating another user", http.StatusForbidden)
		return
	}

	// Extract event ID from URL path: /api/events/{id}
	eventID := strings.TrimPrefix(r.URL.Path, "/api/events/")
	if eventID == "" {
		http.Error(w, "Missing event ID", http.StatusBadRequest)
		return
	}

	// Parse event ID: {issueKey}-{worklogID}
	lastDash := strings.LastIndex(eventID, "-")
	if lastDash == -1 {
		http.Error(w, "Invalid event ID format", http.StatusBadRequest)
		return
	}
	issueKey := eventID[:lastDash]
	worklogID := eventID[lastDash+1:]

	client := NewJiraClient(session.Token, session.CloudID)

	if err := client.DeleteWorklog(r.Context(), issueKey, worklogID); err != nil {
		logrus.Errorf("Failed to delete worklog: %v", err)
		http.Error(w, fmt.Sprintf("Failed to delete worklog: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func handleGetIssues(w http.ResponseWriter, r *http.Request) {
	session := getSessionFromRequest(r)
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	effectiveAccountID := getEffectiveAccountID(session)

	// Check cache first (using effective account ID for impersonation support)
	if issues, ok := cache.GetIssues(effectiveAccountID); ok {
		writeJSON(w, issues)
		return
	}

	client := NewJiraClient(session.Token, session.CloudID)
	issues, err := client.GetMyIssues(r.Context(), effectiveAccountID)
	if err != nil {
		logrus.Errorf("Failed to get issues: %v", err)
		http.Error(w, "Failed to get issues", http.StatusInternalServerError)
		return
	}
	cache.SetIssues(effectiveAccountID, issues)
	writeJSON(w, issues)
}

func handleGetHours(w http.ResponseWriter, r *http.Request) {
	session := getSessionFromRequest(r)
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	weekStr := r.URL.Query().Get("week")
	var weekStart time.Time
	var err error

	if weekStr != "" {
		weekStart, err = time.Parse("2006-01-02", weekStr)
		if err != nil {
			http.Error(w, "Invalid week date", http.StatusBadRequest)
			return
		}
	} else {
		// Default to current week (Monday)
		now := time.Now()
		weekday := int(now.Weekday())
		if weekday == 0 {
			weekday = 7 // Sunday
		}
		weekStart = now.AddDate(0, 0, -(weekday - 1)).Truncate(24 * time.Hour)
	}

	weekEnd := weekStart.AddDate(0, 0, 7)

	client := NewJiraClient(session.Token, session.CloudID)
	effectiveAccountID := getEffectiveAccountID(session)
	events, err := client.GetMyWorklogsForPeriod(r.Context(), weekStart, weekEnd, effectiveAccountID)
	if err != nil {
		logrus.Errorf("Failed to get worklogs for hours: %v", err)
		http.Error(w, "Failed to get hours", http.StatusInternalServerError)
		return
	}

	// Calculate total hours
	var totalSeconds int
	for _, event := range events {
		totalSeconds += int(event.End.Sub(event.Start).Seconds())
	}

	summary := HoursSummary{
		WeekStart:   weekStart.Format("2006-01-02"),
		HoursLogged: float64(totalSeconds) / 3600,
		HoursTarget: float64(viper.GetInt("HOURS_TARGET")),
	}

	writeJSON(w, summary)
}

func handleSearchIssues(w http.ResponseWriter, r *http.Request) {
	session := getSessionFromRequest(r)
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	query := r.URL.Query().Get("q")
	if query == "" || len(query) < 2 {
		writeJSON(w, []Issue{})
		return
	}

	client := NewJiraClient(session.Token, session.CloudID)
	issues, err := client.SearchIssues(r.Context(), query)
	if err != nil {
		logrus.Errorf("Failed to search issues: %v", err)
		http.Error(w, "Failed to search issues", http.StatusInternalServerError)
		return
	}

	if issues == nil {
		issues = []Issue{}
	}

	writeJSON(w, issues)
}

func handleRefresh(w http.ResponseWriter, r *http.Request) {
	session := getSessionFromRequest(r)
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	cache.InvalidateAll(session.AccountID)
	cache.InvalidateSite(session.CloudID)
	w.WriteHeader(http.StatusOK)
	writeJSON(w, map[string]string{"status": "ok"})
}

func handleGetUser(w http.ResponseWriter, r *http.Request) {
	session := getSessionFromRequest(r)
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	response := map[string]interface{}{
		"account_id":      session.AccountID,
		"display_name":    session.DisplayName,
		"email":           session.Email,
		"avatar_url":      session.AvatarURL,
		"site_url":        session.SiteURL,
		"is_super_user":   IsSuperUser(session.AccountID),
		"is_manager":      IsManager(session.AccountID),
		"can_impersonate": CanImpersonate(session.AccountID),
	}

	// Include impersonation info if active
	if session.ImpersonatingID != "" {
		response["impersonating_id"] = session.ImpersonatingID
		response["impersonating_name"] = session.ImpersonatingName
	}

	writeJSON(w, response)
}

func handleSearchUsers(w http.ResponseWriter, r *http.Request) {
	session := getSessionFromRequest(r)
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Only super users and managers can search for users to impersonate
	if !CanImpersonate(session.AccountID) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	query := r.URL.Query().Get("q")
	if query == "" || len(query) < 2 {
		writeJSON(w, []JiraUser{})
		return
	}

	client := NewJiraClient(session.Token, session.CloudID)
	users, err := client.SearchUsers(r.Context(), query)
	if err != nil {
		logrus.Errorf("Failed to search users: %v", err)
		http.Error(w, "Failed to search users", http.StatusInternalServerError)
		return
	}

	// Return simplified user info
	type UserInfo struct {
		AccountID   string `json:"account_id"`
		DisplayName string `json:"display_name"`
		AvatarURL   string `json:"avatar_url"`
	}

	result := make([]UserInfo, len(users))
	for i, u := range users {
		result[i] = UserInfo{
			AccountID:   u.AccountID,
			DisplayName: u.DisplayName,
			AvatarURL:   u.AvatarURLs.Large,
		}
	}

	writeJSON(w, result)
}

func handleImpersonate(w http.ResponseWriter, r *http.Request) {
	session := getSessionFromRequest(r)
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Only super users and managers can impersonate
	if !CanImpersonate(session.AccountID) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	var req struct {
		AccountID   string `json:"account_id"`
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.AccountID == "" {
		http.Error(w, "account_id is required", http.StatusBadRequest)
		return
	}

	// Update session with impersonation info
	session.ImpersonatingID = req.AccountID
	session.ImpersonatingName = req.DisplayName
	saveSession(session)

	writeJSON(w, map[string]string{"status": "ok"})
}

func handleStopImpersonate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	session := getSessionFromRequest(r)
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Clear impersonation
	session.ImpersonatingID = ""
	session.ImpersonatingName = ""
	saveSession(session)

	writeJSON(w, map[string]string{"status": "ok"})
}

// issueKeyPattern matches Jira issue keys like "PROJ-123" (project keys may
// contain digits and underscores after the first letter)
var issueKeyPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*-[0-9]+$`)

// handleSubtaskOptions returns the billable sub-task choices for an issue.
// If the issue is itself a billable sub-task, it is resolved to its parent and
// the current association is reported so the edit dialog can pre-check it.
func handleSubtaskOptions(w http.ResponseWriter, r *http.Request, issueKey string) {
	session := getSessionFromRequest(r)
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if !issueKeyPattern.MatchString(issueKey) {
		http.Error(w, "Invalid issue key", http.StatusBadRequest)
		return
	}

	client := NewJiraClient(session.Token, session.CloudID)

	detail, err := client.GetIssueDetail(r.Context(), issueKey)
	if err != nil {
		logrus.Errorf("Failed to get issue %s: %v", issueKey, err)
		http.Error(w, "Failed to get issue", http.StatusInternalServerError)
		return
	}

	res := SubtaskOptionsRes{
		BillableTypes: []SubtaskType{},
		Subtasks:      []SubtaskInfo{},
	}

	billableTypes, err := client.GetBillableSubtaskTypes(r.Context(), detail.Fields.Project.Key)
	if err != nil {
		logrus.Warnf("Failed to resolve billable sub-task types for project %s: %v", detail.Fields.Project.Key, err)
		billableTypes = []SubtaskType{}
	}

	// A billable sub-task resolves to its parent with the association pre-set
	if detail.Fields.IssueType.Subtask && detail.Fields.Parent != nil && subtaskTypeByID(billableTypes, detail.Fields.IssueType.ID) != nil {
		res.CurrentTypeID = detail.Fields.IssueType.ID
		res.CurrentSubtask = detail.Key

		detail, err = client.GetIssueDetail(r.Context(), detail.Fields.Parent.Key)
		if err != nil {
			logrus.Errorf("Failed to get parent issue: %v", err)
			http.Error(w, "Failed to get parent issue", http.StatusInternalServerError)
			return
		}
	}

	res.IssueKey = detail.Key
	res.IssueSummary = detail.Fields.Summary
	res.IssueTitle = fmt.Sprintf("[%s] %s", detail.Key, detail.Fields.Summary)

	// Sub-tasks cannot have sub-tasks of their own, so a non-billable sub-task
	// gets no checkbox options
	if len(billableTypes) > 0 && !detail.Fields.IssueType.Subtask {
		res.BillableTypes = billableTypes

		for _, st := range detail.Fields.Subtasks {
			if subtaskTypeByID(billableTypes, st.Fields.IssueType.ID) == nil {
				continue
			}
			res.Subtasks = append(res.Subtasks, SubtaskInfo{
				Key:     st.Key,
				Summary: st.Fields.Summary,
				TypeID:  st.Fields.IssueType.ID,
				Status:  st.Fields.Status.Name,
			})
		}
	}

	writeJSON(w, res)
}

// resolveWorklogTarget determines which issue a worklog should be logged
// against: a specific existing sub-task, a get-or-create sub-task of a billable
// type (named after the type), or the parent issue itself.
func resolveWorklogTarget(ctx context.Context, client *JiraClient, parentKey, subtaskTypeID, subtaskKey string) (string, error) {
	if subtaskTypeID == "" && subtaskKey == "" {
		return parentKey, nil
	}

	detail, err := client.GetIssueDetail(ctx, parentKey)
	if err != nil {
		return "", fmt.Errorf("failed to get issue %s: %w", parentKey, err)
	}

	billableTypes, err := client.GetBillableSubtaskTypes(ctx, detail.Fields.Project.Key)
	if err != nil {
		return "", fmt.Errorf("failed to resolve billable sub-task types: %w", err)
	}

	// Specific existing sub-task chosen - verify it belongs to the parent and
	// is a billable type before trusting the client-supplied key
	if subtaskKey != "" {
		for _, st := range detail.Fields.Subtasks {
			if st.Key == subtaskKey && subtaskTypeByID(billableTypes, st.Fields.IssueType.ID) != nil {
				return subtaskKey, nil
			}
		}
		return "", fmt.Errorf("sub-task %s is not a billable sub-task of %s", subtaskKey, parentKey)
	}

	// Type-only selection: reuse the sub-task named after the type, or create it
	billableType := subtaskTypeByID(billableTypes, subtaskTypeID)
	if billableType == nil {
		return "", fmt.Errorf("sub-task type %s is not available in project %s", subtaskTypeID, detail.Fields.Project.Key)
	}

	for _, st := range detail.Fields.Subtasks {
		if st.Fields.IssueType.ID == billableType.ID && strings.EqualFold(st.Fields.Summary, billableType.Name) {
			return st.Key, nil
		}
	}

	newKey, err := client.CreateSubtask(ctx, detail.Fields.Project.ID, parentKey, billableType.ID, billableType.Name)
	if err != nil {
		return "", fmt.Errorf("failed to create sub-task: %w", err)
	}

	return newKey, nil
}

func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		logrus.Errorf("Failed to encode JSON response: %v", err)
	}
}

// Worklog property key for source tracking
const worklogSourcePropertyKey = "jiratime.source"

// WorklogSource indicates the worklog was created by JiraTime
type WorklogSource struct {
	CreatedBy string `json:"created_by"`
}
