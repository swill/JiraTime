package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

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
	events, err := client.GetMyWorklogsForPeriod(r.Context(), start, end, session.AccountID)
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
	worklog, err := client.CreateWorklog(r.Context(), req.IssueKey, start, durationSeconds, req.Description)
	if err != nil {
		logrus.Errorf("Failed to create worklog: %v", err)
		http.Error(w, "Failed to create event", http.StatusInternalServerError)
		return
	}

	// Handle custom field contributions if any selections provided
	if len(req.CustomFieldSelections) > 0 {
		if err := updateCustomFieldsForWorklog(r.Context(), client, req.IssueKey, worklog.ID, req.DurationMin, req.CustomFieldSelections, nil); err != nil {
			logrus.Errorf("Failed to update custom fields: %v", err)
			// Don't fail the request - worklog was created successfully
		}
	}

	// Return the created event
	event := CalendarEvent{
		ID:          req.IssueKey + "-" + worklog.ID,
		Title:       "[" + req.IssueKey + "]",
		Start:       start,
		End:         start.Add(time.Duration(durationSeconds) * time.Second),
		IssueKey:    req.IssueKey,
		WorklogID:   worklog.ID,
		Description: req.Description,
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

	// Always get previous contributions - needed for both explicit selections and drag-resize
	previousContributions, err := client.GetWorklogProperty(r.Context(), issueKey, worklogID, worklogPropertyKey)
	if err != nil {
		logrus.Warnf("Failed to get previous contributions: %v", err)
		previousContributions = &CustomFieldContributions{Contributions: make(map[string]int)}
	}

	_, err = client.UpdateWorklog(r.Context(), issueKey, worklogID, start, durationSeconds, req.Description)
	if err != nil {
		logrus.Errorf("Failed to update worklog: %v", err)
		http.Error(w, "Failed to update event", http.StatusInternalServerError)
		return
	}

	// Handle custom field contributions
	// If selections provided, use them; otherwise preserve existing contributions with new duration
	selections := req.CustomFieldSelections
	if selections == nil && len(previousContributions.Contributions) > 0 {
		// Drag-resize case: rebuild selections from existing contributions
		selections = make(map[string]bool)
		for fieldID := range previousContributions.Contributions {
			selections[fieldID] = true
		}
	}

	if selections != nil {
		if err := updateCustomFieldsForWorklog(r.Context(), client, issueKey, worklogID, req.DurationMin, selections, previousContributions); err != nil {
			logrus.Errorf("Failed to update custom fields: %v", err)
			// Don't fail the request - worklog was updated successfully
		}
	}

	w.WriteHeader(http.StatusOK)
	writeJSON(w, map[string]string{"status": "ok"})
}

func handleDeleteEvent(w http.ResponseWriter, r *http.Request) {
	session := getSessionFromRequest(r)
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
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

	// Remove custom field contributions before deleting worklog
	if err := removeCustomFieldContributions(r.Context(), client, issueKey, worklogID); err != nil {
		logrus.Warnf("Failed to remove custom field contributions: %v", err)
		// Continue with delete even if contribution removal fails
	}

	if err := client.DeleteWorklog(r.Context(), issueKey, worklogID); err != nil {
		logrus.Errorf("Failed to delete worklog: %v", err)
		http.Error(w, "Failed to delete event", http.StatusInternalServerError)
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

	// Check cache first
	if issues, ok := cache.GetIssues(session.AccountID); ok {
		writeJSON(w, issues)
		return
	}

	client := NewJiraClient(session.Token, session.CloudID)
	issues, err := client.GetMyIssues(r.Context())
	if err != nil {
		logrus.Errorf("Failed to get issues: %v", err)
		http.Error(w, "Failed to get issues", http.StatusInternalServerError)
		return
	}
	cache.SetIssues(session.AccountID, issues)
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
	events, err := client.GetMyWorklogsForPeriod(r.Context(), weekStart, weekEnd, session.AccountID)
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
	w.WriteHeader(http.StatusOK)
	writeJSON(w, map[string]string{"status": "ok"})
}

func handleGetUser(w http.ResponseWriter, r *http.Request) {
	session := getSessionFromRequest(r)
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	writeJSON(w, map[string]interface{}{
		"account_id":   session.AccountID,
		"display_name": session.DisplayName,
		"email":        session.Email,
		"avatar_url":   session.AvatarURL,
		"site_url":     session.SiteURL,
	})
}

func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		logrus.Errorf("Failed to encode JSON response: %v", err)
	}
}

// Worklog property key for custom field contributions
const worklogPropertyKey = "jiratime.customFieldContributions"

// handleGetIssueCustomFields returns available custom fields and their current values for an issue
func handleGetIssueCustomFields(w http.ResponseWriter, r *http.Request) {
	session := getSessionFromRequest(r)
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Extract issue key from URL path: /api/issues/{key}/custom-fields
	path := strings.TrimPrefix(r.URL.Path, "/api/issues/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[1] != "custom-fields" {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	issueKey := parts[0]

	customFields := GetCustomTimeFields()
	fieldIDs := make([]string, len(customFields))
	for i, cf := range customFields {
		fieldIDs[i] = cf.ID
	}

	client := NewJiraClient(session.Token, session.CloudID)
	values, err := client.GetIssueCustomFields(r.Context(), issueKey, fieldIDs)
	if err != nil {
		logrus.Errorf("Failed to get custom fields for issue %s: %v", issueKey, err)
		// Return fields as unavailable rather than erroring
		result := make([]CustomFieldInfo, len(customFields))
		for i, cf := range customFields {
			result[i] = CustomFieldInfo{
				ID:        cf.ID,
				Label:     cf.Label,
				Available: false,
			}
		}
		writeJSON(w, result)
		return
	}

	// Build response with availability info
	result := make([]CustomFieldInfo, len(customFields))
	for i, cf := range customFields {
		info := CustomFieldInfo{
			ID:        cf.ID,
			Label:     cf.Label,
			Available: false,
		}
		if val, ok := values[cf.ID]; ok {
			info.Available = true
			info.CurrentValue = val
		}
		result[i] = info
	}

	writeJSON(w, result)
}

// handleGetEventContributions returns the custom field contributions for a specific worklog
func handleGetEventContributions(w http.ResponseWriter, r *http.Request) {
	session := getSessionFromRequest(r)
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Extract event ID from URL path: /api/events/{id}/contributions
	path := strings.TrimPrefix(r.URL.Path, "/api/events/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[1] != "contributions" {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	eventID := parts[0]

	// Parse event ID: {issueKey}-{worklogID}
	lastDash := strings.LastIndex(eventID, "-")
	if lastDash == -1 {
		http.Error(w, "Invalid event ID format", http.StatusBadRequest)
		return
	}
	issueKey := eventID[:lastDash]
	worklogID := eventID[lastDash+1:]

	client := NewJiraClient(session.Token, session.CloudID)
	contributions, err := client.GetWorklogProperty(r.Context(), issueKey, worklogID, worklogPropertyKey)
	if err != nil {
		logrus.Errorf("Failed to get worklog contributions: %v", err)
		// Return empty contributions on error
		writeJSON(w, CustomFieldContributions{Contributions: make(map[string]int)})
		return
	}

	writeJSON(w, contributions)
}

// updateCustomFieldsForWorklog handles custom field updates for create/update operations
func updateCustomFieldsForWorklog(ctx context.Context, client *JiraClient, issueKey, worklogID string, durationMin int, selections map[string]bool, previousContributions *CustomFieldContributions) error {
	customFields := GetCustomTimeFields()

	// Get current field values
	fieldIDs := make([]string, len(customFields))
	for i, cf := range customFields {
		fieldIDs[i] = cf.ID
	}

	currentValues, err := client.GetIssueCustomFields(ctx, issueKey, fieldIDs)
	if err != nil {
		logrus.Warnf("Failed to get current custom field values: %v", err)
		currentValues = make(map[string]int)
	}

	// Calculate new contributions
	newContributions := make(map[string]int)
	for _, cf := range customFields {
		if selections != nil && selections[cf.ID] {
			newContributions[cf.ID] = durationMin
		}
	}

	// Apply deltas to each field
	for _, cf := range customFields {
		oldContribution := 0
		if previousContributions != nil {
			oldContribution = previousContributions.Contributions[cf.ID]
		}
		newContribution := newContributions[cf.ID]

		delta := newContribution - oldContribution
		if delta == 0 {
			continue
		}

		// Calculate new value (floor at 0)
		currentValue := currentValues[cf.ID]
		newValue := currentValue + delta
		if newValue < 0 {
			newValue = 0
		}

		if err := client.UpdateIssueCustomField(ctx, issueKey, cf.ID, newValue); err != nil {
			logrus.Warnf("Failed to update custom field %s: %v", cf.ID, err)
			// Continue with other fields
		}
	}

	// Store new contributions if any, or delete property if empty
	if len(newContributions) > 0 {
		if err := client.SetWorklogProperty(ctx, issueKey, worklogID, worklogPropertyKey, CustomFieldContributions{Contributions: newContributions}); err != nil {
			logrus.Warnf("Failed to store worklog contributions: %v", err)
		}
	} else if previousContributions != nil && len(previousContributions.Contributions) > 0 {
		// Had contributions before but now none - delete the property
		if err := client.DeleteWorklogProperty(ctx, issueKey, worklogID, worklogPropertyKey); err != nil {
			logrus.Warnf("Failed to delete worklog contributions: %v", err)
		}
	}

	return nil
}

// removeCustomFieldContributions removes a worklog's contributions from custom fields
func removeCustomFieldContributions(ctx context.Context, client *JiraClient, issueKey, worklogID string) error {
	// Get existing contributions
	contributions, err := client.GetWorklogProperty(ctx, issueKey, worklogID, worklogPropertyKey)
	if err != nil {
		logrus.Warnf("Failed to get worklog contributions for removal: %v", err)
		return nil // Don't fail delete if we can't get contributions
	}

	if contributions == nil || len(contributions.Contributions) == 0 {
		return nil // Nothing to remove
	}

	customFields := GetCustomTimeFields()
	fieldIDs := make([]string, len(customFields))
	for i, cf := range customFields {
		fieldIDs[i] = cf.ID
	}

	currentValues, err := client.GetIssueCustomFields(ctx, issueKey, fieldIDs)
	if err != nil {
		logrus.Warnf("Failed to get current custom field values for removal: %v", err)
		return nil
	}

	// Subtract contributions from each field
	for fieldID, contribution := range contributions.Contributions {
		if contribution == 0 {
			continue
		}

		currentValue := currentValues[fieldID]
		newValue := currentValue - contribution
		if newValue < 0 {
			newValue = 0
		}

		if err := client.UpdateIssueCustomField(ctx, issueKey, fieldID, newValue); err != nil {
			logrus.Warnf("Failed to update custom field %s during removal: %v", fieldID, err)
		}
	}

	return nil
}
