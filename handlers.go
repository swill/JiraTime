package main

import (
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
	_, err = client.UpdateWorklog(r.Context(), issueKey, worklogID, start, durationSeconds, req.Description)
	if err != nil {
		logrus.Errorf("Failed to update worklog: %v", err)
		http.Error(w, "Failed to update event", http.StatusInternalServerError)
		return
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
