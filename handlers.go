package main

import (
	"context"
	"encoding/json"
	"errors"
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

	client, limited := viewClientFor(session)
	effectiveAccountID := getEffectiveAccountID(session)
	events, err := client.GetMyWorklogsForPeriod(r.Context(), start, end, effectiveAccountID)
	if err != nil {
		logrus.Errorf("Failed to get worklogs: %v", err)
		http.Error(w, "Failed to get events", http.StatusInternalServerError)
		return
	}

	// Lets the "Viewing as" bar warn that entries may be missing
	if limited {
		w.Header().Set("X-View-Limited", "1")
	}
	writeJSON(w, events)
}

// descriptionRequired is the error for a new or dialog-saved entry without a
// description (the worklog comment)
const descriptionRequired = "A description is required"

func handleCreateEvent(w http.ResponseWriter, r *http.Request) {
	wr, ok := newWorklogWriter(w, r)
	if !ok {
		return
	}

	var req CreateEventReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.IssueKey == "" || !validIssueKeys(req.IssueKey, req.SubtaskKey) {
		http.Error(w, "Invalid issue key", http.StatusBadRequest)
		return
	}

	// Jira can't make a worklog comment mandatory, so JiraTime does
	if strings.TrimSpace(req.Description) == "" {
		http.Error(w, descriptionRequired, http.StatusBadRequest)
		return
	}

	start, err := time.Parse(time.RFC3339, req.Start)
	if err != nil {
		http.Error(w, "Invalid start date", http.StatusBadRequest)
		return
	}

	durationSeconds := req.DurationMin * 60
	ctx := r.Context()

	// Created with the calendar owner's access so they are the author. Checked
	// first: without it nothing may be written, not even a new sub-task.
	author, err := wr.authorClient()
	if err != nil {
		writeChangeError(w, wr, "create worklog", err)
		return
	}

	// Route the worklog to a billable sub-task when one was selected
	target, err := findWorklogTarget(ctx, wr.edit, req.IssueKey, req.SubtaskTypeID, req.SubtaskKey)
	if err != nil {
		logrus.Errorf("Failed to resolve worklog target: %v", err)
		http.Error(w, fmt.Sprintf("Failed to resolve sub-task: %v", err), http.StatusInternalServerError)
		return
	}

	if wr.sdm {
		// A sub-task created for a worklog the user then can't log is left empty
		if err := wr.requirePermissions(ctx, author, true, target.keyOrParent(), permWorkOnIssues); err != nil {
			writeChangeError(w, wr, "create worklog", err)
			return
		}
	}

	if err := target.createIfNeeded(ctx, wr.edit); err != nil {
		logrus.Errorf("Failed to resolve worklog target: %v", err)
		http.Error(w, fmt.Sprintf("Failed to resolve sub-task: %v", err), http.StatusInternalServerError)
		return
	}
	targetKey := target.key

	body := worklogBody(formatJiraDateTime(start), durationSeconds, descriptionComment(req.Description))
	worklog, err := author.createWorklog(ctx, targetKey, body)
	if err != nil {
		writeChangeError(w, wr, "create worklog", target.noteCreated(err))
		return
	}

	// Mark this worklog as created by JiraTime
	if err := author.SetWorklogProperty(ctx, targetKey, worklog.ID, worklogSourcePropertyKey, WorklogSource{CreatedBy: "jiratime"}); err != nil {
		logrus.Warnf("Failed to set worklog source property: %v", err)
		// Don't fail - worklog was created successfully
	}

	if wr.sdm {
		detail := wr.stampCreated(ctx, targetKey, worklog.ID, body)
		wr.markChange(ctx, author, targetKey, worklog.ID, "create")
		if target.created {
			cache.InvalidateHierarchy(wr.session.AccountID)
		}
		wr.audit(AuditRecord{
			Action:      "create",
			Outcome:     auditOK,
			IssueKey:    targetKey,
			WorklogID:   worklog.ID,
			After:       &AuditWorklog{IssueKey: targetKey, Started: auditTime(start), TimeSpentSeconds: durationSeconds},
			PriorPeriod: isPriorPeriod(start),
			Detail:      detail,
		})
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
	wr, ok := newWorklogWriter(w, r)
	if !ok {
		return
	}

	// Event ID from URL path: /api/events/{issueKey}-{worklogID}
	issueKey, worklogID, ok := parseEventID(strings.TrimPrefix(r.URL.Path, "/api/events/"))
	if !ok {
		http.Error(w, "Invalid event ID format", http.StatusBadRequest)
		return
	}

	var req UpdateEventReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if !validIssueKeys(req.ParentKey, req.SubtaskKey) {
		http.Error(w, "Invalid issue key", http.StatusBadRequest)
		return
	}

	// Dialog saves need a description like new entries do. Drag/resize
	// updates don't send one (the comment is kept as it is), so entries
	// logged without one can still be moved.
	if req.Description != nil && strings.TrimSpace(*req.Description) == "" {
		http.Error(w, descriptionRequired, http.StatusBadRequest)
		return
	}

	start, err := time.Parse(time.RFC3339, req.Start)
	if err != nil {
		http.Error(w, "Invalid start date", http.StatusBadRequest)
		return
	}

	durationSeconds := req.DurationMin * 60
	ctx := r.Context()
	rec := AuditRecord{Action: "update", IssueKey: issueKey, WorklogID: worklogID}

	// The current worklog: its stored comment is kept unless the description
	// was edited (commentFor), an SDM may only change the viewed user's own
	// worklogs, and the current values go in the audit record
	before, err := wr.loadWorklog(ctx, issueKey, worklogID)
	if err != nil {
		wr.auditFailure(rec, err)
		writeChangeError(w, wr, "update worklog", err)
		return
	}
	rec.Before = auditWorklogOf(issueKey, before)
	started, _ := parseJiraDateTime(before.Started)
	rec.PriorPeriod = isPriorPeriod(started) || isPriorPeriod(start)

	// Dialog saves send ParentKey so the sub-task association can change; when
	// the target differs from where the worklog lives, it has to move
	// (drag/resize updates omit ParentKey and leave the association alone)
	if req.ParentKey != "" {
		target, err := findWorklogTarget(ctx, wr.edit, req.ParentKey, req.SubtaskTypeID, req.SubtaskKey)
		if err != nil {
			logrus.Errorf("Failed to resolve worklog target: %v", err)
			http.Error(w, fmt.Sprintf("Failed to resolve sub-task: %v", err), http.StatusInternalServerError)
			return
		}

		if target.key != issueKey {
			rec.Action = "move"
			if wr.sdm {
				if err := wr.checkMove(ctx, issueKey, target.keyOrParent()); err != nil {
					writeChangeError(w, wr, "move worklog", err)
					return
				}
			}

			if err := target.createIfNeeded(ctx, wr.edit); err != nil {
				logrus.Errorf("Failed to resolve worklog target: %v", err)
				http.Error(w, fmt.Sprintf("Failed to resolve sub-task: %v", err), http.StatusInternalServerError)
				return
			}
			rec.TargetKey = target.key

			var newID string
			if wr.sdm {
				newID, rec.Detail, err = wr.move(ctx, issueKey, before, target.key, start, durationSeconds, req.Description)
			} else {
				body := worklogBody(formatJiraDateTime(start), durationSeconds, commentFor(before, req.Description))
				newID, err = moveWorklog(ctx, wr.edit, wr.edit, issueKey, worklogID, target.key, body)
			}
			if err != nil {
				err = target.noteCreated(err)
				wr.auditFailure(rec, err)
				writeChangeError(w, wr, "move worklog", err)
				return
			}

			if target.created {
				cache.InvalidateHierarchy(wr.session.AccountID)
			}
			rec.Outcome = auditOK
			rec.NewID = strings.TrimPrefix(newID, target.key+"-")
			rec.After = &AuditWorklog{IssueKey: target.key, Started: auditTime(start), TimeSpentSeconds: durationSeconds}
			wr.audit(rec)

			w.WriteHeader(http.StatusOK)
			writeJSON(w, map[string]string{"status": "ok", "id": newID})
			return
		}
	}

	if err := wr.update(ctx, issueKey, before, start, durationSeconds, req.Description); err != nil {
		writeChangeError(w, wr, "update worklog", err)
		return
	}

	if wr.sdm {
		wr.markChange(ctx, wr.edit, issueKey, worklogID, "update")
		rec.Outcome = auditOK
		rec.After = &AuditWorklog{IssueKey: issueKey, Started: auditTime(start), TimeSpentSeconds: durationSeconds}
		wr.audit(rec)
	}

	w.WriteHeader(http.StatusOK)
	writeJSON(w, map[string]string{"status": "ok"})
}

// moveWorklog re-homes a worklog onto another issue by creating it on the
// target first (with createClient, whose user becomes the author) and then
// deleting the original (with deleteClient) - the time is never silently
// lost, and a failed delete is rolled back so it is never doubled either.
// body is the new worklog. Returns the new event ID.
func moveWorklog(ctx context.Context, createClient, deleteClient *JiraClient, fromKey, worklogID, toKey string, body map[string]interface{}) (string, error) {
	worklog, err := createClient.createWorklog(ctx, toKey, body)
	if err != nil {
		return "", fmt.Errorf("failed to create worklog on %s: %w", toKey, err)
	}

	if err := createClient.SetWorklogProperty(ctx, toKey, worklog.ID, worklogSourcePropertyKey, WorklogSource{CreatedBy: "jiratime"}); err != nil {
		logrus.Warnf("Failed to set worklog source property: %v", err)
	}

	if err := deleteClient.DeleteWorklog(ctx, fromKey, worklogID); err != nil {
		// Roll back the copy so time is not double-counted
		if rbErr := createClient.RollbackWorklog(ctx, toKey, worklog.ID); rbErr != nil {
			return "", &partialError{msg: fmt.Sprintf("Failed to remove the original worklog from %s (%v) and failed to remove the copy (worklog %s) on %s (%v): the time is logged twice, please fix it in Jira.", fromKey, err, worklog.ID, toKey, rbErr)}
		}
		return "", fmt.Errorf("failed to remove original worklog from %s (change rolled back): %w", fromKey, err)
	}

	return toKey + "-" + worklog.ID, nil
}

// handleSplitEvent splits a worklog in two: the original keeps the first part
// of its time, and the rest becomes a new worklog by the same author, starting
// where the first part ends, on the chosen issue (which may be the same one).
func handleSplitEvent(w http.ResponseWriter, r *http.Request, eventID string) {
	wr, ok := newWorklogWriter(w, r)
	if !ok {
		return
	}

	issueKey, worklogID, ok := parseEventID(eventID)
	if !ok {
		http.Error(w, "Invalid event ID format", http.StatusBadRequest)
		return
	}

	var req SplitEventReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.ParentKey == "" || !validIssueKeys(req.ParentKey, req.SubtaskKey) {
		http.Error(w, "Invalid issue key", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	rec := AuditRecord{Action: "split", IssueKey: issueKey, WorklogID: worklogID}

	original, err := wr.loadWorklog(ctx, issueKey, worklogID)
	if err != nil {
		wr.auditFailure(rec, err)
		writeChangeError(w, wr, "split worklog", err)
		return
	}
	rec.Before = auditWorklogOf(issueKey, original)

	start, err := parseJiraDateTime(original.Started)
	if err != nil {
		http.Error(w, "Failed to read the entry's start time", http.StatusInternalServerError)
		return
	}
	rec.PriorPeriod = isPriorPeriod(start)

	firstSeconds := req.DurationMin * 60
	restSeconds := original.TimeSpentSeconds - firstSeconds
	if firstSeconds <= 0 || restSeconds <= 0 {
		http.Error(w, "The part kept on this entry must be shorter than the entry", http.StatusBadRequest)
		return
	}

	// The new part is authored by the calendar owner; without their access
	// nothing may be written
	author, err := wr.authorClient()
	if err != nil {
		writeChangeError(w, wr, "split worklog", err)
		return
	}

	target, err := findWorklogTarget(ctx, wr.edit, req.ParentKey, req.SubtaskTypeID, req.SubtaskKey)
	if err != nil {
		logrus.Errorf("Failed to resolve worklog target: %v", err)
		http.Error(w, fmt.Sprintf("Failed to resolve sub-task: %v", err), http.StatusInternalServerError)
		return
	}

	// Make sure both halves can be written before writing either
	if wr.sdm {
		if err := wr.requirePermissions(ctx, wr.edit, false, issueKey, permEditAllWorklogs); err != nil {
			writeChangeError(w, wr, "split worklog", err)
			return
		}
		if err := wr.requirePermissions(ctx, author, true, target.keyOrParent(), permWorkOnIssues); err != nil {
			writeChangeError(w, wr, "split worklog", err)
			return
		}
	}

	if err := target.createIfNeeded(ctx, wr.edit); err != nil {
		logrus.Errorf("Failed to resolve worklog target: %v", err)
		http.Error(w, fmt.Sprintf("Failed to resolve sub-task: %v", err), http.StatusInternalServerError)
		return
	}
	rec.TargetKey = target.key

	// Create the new part first and then shorten the original, undoing the
	// new part if that fails: time is never lost, and never counted twice
	restStart := start.Add(time.Duration(firstSeconds) * time.Second)
	restBody := worklogBody(formatJiraDateTime(restStart), restSeconds, rawComment(original.RawComment))
	rest, err := author.createWorklog(ctx, target.key, restBody)
	if err != nil {
		writeChangeError(w, wr, "split worklog", target.noteCreated(err))
		return
	}

	if err := author.SetWorklogProperty(ctx, target.key, rest.ID, worklogSourcePropertyKey, WorklogSource{CreatedBy: "jiratime"}); err != nil {
		logrus.Warnf("Failed to set worklog source property: %v", err)
	}

	if err := wr.edit.ResizeWorklog(ctx, issueKey, original, firstSeconds); err != nil {
		if rbErr := author.RollbackWorklog(ctx, target.key, rest.ID); rbErr != nil {
			err = &partialError{msg: fmt.Sprintf("Failed to shorten the original entry on %s (%v) and failed to remove the new part (worklog %s) on %s (%v): %d minutes are logged twice, please fix it in Jira.", issueKey, err, rest.ID, target.key, rbErr, restSeconds/60)}
			rec.NewID = rest.ID
			wr.auditFailure(rec, err)
			writeChangeError(w, wr, "split worklog", err)
			return
		}
		writeChangeError(w, wr, "split worklog", fmt.Errorf("failed to shorten the original entry (split undone): %w", target.noteCreated(err)))
		return
	}

	if wr.sdm {
		rec.Detail = wr.stampCreated(ctx, target.key, rest.ID, restBody)
		wr.markChange(ctx, wr.edit, issueKey, worklogID, "split")
		wr.markChange(ctx, author, target.key, rest.ID, "split")
		if target.created {
			cache.InvalidateHierarchy(wr.session.AccountID)
		}
		rec.Outcome = auditOK
		rec.NewID = rest.ID
		rec.After = &AuditWorklog{IssueKey: issueKey, Started: auditTime(start), TimeSpentSeconds: firstSeconds}
		rec.Extra = map[string]any{"new_worklog": AuditWorklog{IssueKey: target.key, Started: auditTime(restStart), TimeSpentSeconds: restSeconds}}
		wr.audit(rec)
	}

	writeJSON(w, map[string]interface{}{"status": "ok", "ids": []string{issueKey + "-" + worklogID, target.key + "-" + rest.ID}})
}

func handleDeleteEvent(w http.ResponseWriter, r *http.Request) {
	wr, ok := newWorklogWriter(w, r)
	if !ok {
		return
	}

	// Event ID from URL path: /api/events/{issueKey}-{worklogID}
	issueKey, worklogID, ok := parseEventID(strings.TrimPrefix(r.URL.Path, "/api/events/"))
	if !ok {
		http.Error(w, "Invalid event ID format", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	rec := AuditRecord{Action: "delete", IssueKey: issueKey, WorklogID: worklogID}

	if wr.sdm {
		before, err := wr.loadWorklog(ctx, issueKey, worklogID)
		if err != nil {
			wr.auditFailure(rec, err)
			writeChangeError(w, wr, "delete worklog", err)
			return
		}
		rec.Before = auditWorklogOf(issueKey, before)
		started, _ := parseJiraDateTime(before.Started)
		rec.PriorPeriod = isPriorPeriod(started)
	}

	if err := wr.edit.DeleteWorklog(ctx, issueKey, worklogID); err != nil {
		writeChangeError(w, wr, "delete worklog", err)
		return
	}

	rec.Outcome = auditOK
	wr.audit(rec)

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

	client, limited := viewClientFor(session)
	issues, err := client.GetMyIssues(r.Context(), effectiveAccountID)
	if err != nil {
		logrus.Errorf("Failed to get issues: %v", err)
		http.Error(w, "Failed to get issues", http.StatusInternalServerError)
		return
	}
	// A limited view can be missing issues, so it must not become what the
	// user sees in their own sidebar
	if !limited {
		cache.SetIssues(effectiveAccountID, issues)
	}
	writeJSON(w, issues)
}

func handleGetHours(w http.ResponseWriter, r *http.Request) {
	session := getSessionFromRequest(r)
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// The client sends its local week bounds (Monday 00:00 to the next Monday
	// 00:00, RFC 3339 with offset) so the week matches the user's timezone
	startStr := r.URL.Query().Get("start")
	endStr := r.URL.Query().Get("end")
	var weekStart, weekEnd time.Time
	var err error

	if startStr != "" || endStr != "" {
		weekStart, err = time.Parse(time.RFC3339, startStr)
		if err != nil {
			http.Error(w, "Invalid start date", http.StatusBadRequest)
			return
		}
		weekEnd, err = time.Parse(time.RFC3339, endStr)
		if err != nil {
			http.Error(w, "Invalid end date", http.StatusBadRequest)
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
		weekEnd = weekStart.AddDate(0, 0, 7)
	}

	client, _ := viewClientFor(session)
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

	// SDMs correcting someone's time need issues that closed long ago too
	allDone := isImpersonating(session) && CanEditOthers(session)

	client := jiraClientFor(session)
	issues, err := client.SearchIssues(r.Context(), query, allDone)
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
	if isImpersonating(session) {
		cache.InvalidateAll(session.ImpersonatingID)
	}
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
		"is_super_user":   IsSuperUser(session),
		"is_manager":      IsManager(session.AccountID),
		"is_sdm":          IsSDM(session.AccountID),
		"can_impersonate": CanImpersonate(session),
		"can_edit_others": CanEditOthers(session),
	}

	// Include impersonation info if active
	if session.ImpersonatingID != "" {
		response["impersonating_id"] = session.ImpersonatingID
		response["impersonating_name"] = session.ImpersonatingName
		// Whether time can be created (added, copied, split or moved) for
		// them: it must be authored with their own JiraTime sign-in, so SDMs
		// know before they try
		_, err := accountTokens.validToken(session.ImpersonatingID)
		response["impersonating_signed_in"] = err == nil || !isTokenRejected(err)
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
	if !CanImpersonate(session) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	query := r.URL.Query().Get("q")
	if query == "" || len(query) < 2 {
		writeJSON(w, []JiraUser{})
		return
	}

	client := jiraClientFor(session)
	users, err := client.SearchUsers(r.Context(), query)
	if err != nil {
		logrus.Errorf("Failed to search users: %v", err)
		http.Error(w, "Failed to search users", http.StatusInternalServerError)
		return
	}

	// Return simplified user info. HasJiraTime says whether JiraTime holds a
	// sign-in for them, which an SDM needs to add time on their behalf.
	type UserInfo struct {
		AccountID   string `json:"account_id"`
		DisplayName string `json:"display_name"`
		AvatarURL   string `json:"avatar_url"`
		HasJiraTime bool   `json:"has_jiratime"`
	}

	result := make([]UserInfo, len(users))
	for i, u := range users {
		result[i] = UserInfo{
			AccountID:   u.AccountID,
			DisplayName: u.DisplayName,
			AvatarURL:   u.AvatarURLs.Large,
			HasJiraTime: accountTokens.get(u.AccountID) != nil,
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
	if !CanImpersonate(session) {
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

	// Audit trail: the viewer reads this user's time with the user's own Jira
	// access (and SDMs can change it)
	logrus.Infof("%s started viewing %s's calendar (can edit: %t)", session.AccountID, req.AccountID, CanEditOthers(session))

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

	if session.ImpersonatingID != "" {
		logrus.Infof("%s stopped viewing %s's calendar", session.AccountID, session.ImpersonatingID)
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

// handleSubtaskOptions returns the configured sub-task choices for an issue
// (billable or non-billable, per the project's mode). If the issue is itself a
// configured sub-task, it is resolved to its parent and the current association
// is reported so the edit dialog can pre-check it.
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

	client := jiraClientFor(session)

	detail, err := client.GetIssueDetail(r.Context(), issueKey)
	if err != nil {
		logrus.Errorf("Failed to get issue %s: %v", issueKey, err)
		http.Error(w, "Failed to get issue", http.StatusInternalServerError)
		return
	}

	res := SubtaskOptionsRes{
		SubtaskTypes: []SubtaskType{},
		Subtasks:     []SubtaskInfo{},
	}

	configuredTypes, mode, err := client.GetConfiguredSubtaskTypes(r.Context(), detail.Fields.Project.Key)
	if err != nil {
		logrus.Warnf("Failed to resolve sub-task types for project %s: %v", detail.Fields.Project.Key, err)
		configuredTypes = []SubtaskType{}
		mode = ""
	}

	// Sub-tasks can't have sub-tasks, so one of a type that isn't configured
	// gets no options (logged, as it looks like a missing configuration)
	if detail.Fields.IssueType.Subtask && len(configuredTypes) > 0 && subtaskTypeByID(configuredTypes, detail.Fields.IssueType.ID) == nil {
		logrus.Infof("%s is a %q sub-task, which isn't one of project %s's configured sub-task types, so no sub-task options are offered", detail.Key, detail.Fields.IssueType.Name, detail.Fields.Project.Key)
	}

	// A configured sub-task resolves to its parent with the association pre-set
	if detail.Fields.IssueType.Subtask && detail.Fields.Parent != nil && subtaskTypeByID(configuredTypes, detail.Fields.IssueType.ID) != nil {
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
	res.ProjectKey = detail.Fields.Project.Key
	res.IssueSummary = detail.Fields.Summary
	res.IssueTitle = fmt.Sprintf("[%s] %s", detail.Key, detail.Fields.Summary)

	// Sub-tasks cannot have sub-tasks of their own, so an issue that is itself a
	// configured sub-task gets no checkbox options
	if len(configuredTypes) > 0 && !detail.Fields.IssueType.Subtask {
		res.SubtaskTypes = configuredTypes
		res.SubtaskMode = mode

		for _, st := range detail.Fields.Subtasks {
			if subtaskTypeByID(configuredTypes, st.Fields.IssueType.ID) == nil {
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

// worklogTarget is the issue a worklog should be logged against: a specific
// existing sub-task, a get-or-create sub-task of a configured type (named
// after the type), or the parent issue itself. The configured types are
// billable or non-billable depending on the project. Finding the target and
// creating a missing sub-task are separate steps, so a change can be checked
// before anything is created.
type worklogTarget struct {
	key       string // empty until a missing sub-task is created
	parentKey string
	created   bool // the sub-task was created for this change

	// Set while the sub-task of this type still has to be created
	createType *SubtaskType
	projectID  string
}

// findWorklogTarget works out where a worklog should go without creating
// anything
func findWorklogTarget(ctx context.Context, client *JiraClient, parentKey, subtaskTypeID, subtaskKey string) (*worklogTarget, error) {
	target := &worklogTarget{parentKey: parentKey}
	if subtaskTypeID == "" && subtaskKey == "" {
		target.key = parentKey
		return target, nil
	}

	detail, err := client.GetIssueDetail(ctx, parentKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get issue %s: %w", parentKey, err)
	}

	configuredTypes, _, err := client.GetConfiguredSubtaskTypes(ctx, detail.Fields.Project.Key)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve sub-task types: %w", err)
	}

	// Specific existing sub-task chosen - verify it belongs to the parent and
	// is a configured type before trusting the client-supplied key
	if subtaskKey != "" {
		for _, st := range detail.Fields.Subtasks {
			if st.Key == subtaskKey && subtaskTypeByID(configuredTypes, st.Fields.IssueType.ID) != nil {
				target.key = subtaskKey
				return target, nil
			}
		}
		return nil, fmt.Errorf("sub-task %s is not a configured sub-task of %s", subtaskKey, parentKey)
	}

	// Type-only selection: reuse the sub-task named after the type, or create it
	configuredType := subtaskTypeByID(configuredTypes, subtaskTypeID)
	if configuredType == nil {
		return nil, fmt.Errorf("sub-task type %s is not available in project %s", subtaskTypeID, detail.Fields.Project.Key)
	}

	for _, st := range detail.Fields.Subtasks {
		if st.Fields.IssueType.ID == configuredType.ID && strings.EqualFold(st.Fields.Summary, configuredType.Name) {
			target.key = st.Key
			return target, nil
		}
	}

	target.createType = configuredType
	target.projectID = detail.Fields.Project.ID
	return target, nil
}

// createIfNeeded creates the target sub-task if it does not exist yet
func (t *worklogTarget) createIfNeeded(ctx context.Context, client *JiraClient) error {
	if t.createType == nil {
		return nil
	}

	newKey, err := client.CreateSubtask(ctx, t.projectID, t.parentKey, t.createType.ID, t.createType.Name)
	if err != nil {
		return fmt.Errorf("failed to create sub-task: %w", err)
	}

	t.key = newKey
	t.created = true
	t.createType = nil
	return nil
}

// keyOrParent is the target, or its parent while the target sub-task has not
// been created yet (permissions are checked there instead)
func (t *worklogTarget) keyOrParent() string {
	if t.key != "" {
		return t.key
	}
	return t.parentKey
}

// noteCreated adds to an error that the target sub-task was created for this
// change, so it is not left empty unnoticed
func (t *worklogTarget) noteCreated(err error) error {
	if !t.created {
		return err
	}
	return fmt.Errorf("%w (sub-task %s was created for this change but has no time on it)", err, t.key)
}

// handleHierarchy returns one level of the SDM Move-to tree, loaded as it is
// expanded:
//   - ?project=KEY             the project and its epics
//   - ?project=KEY&no_epic=1   the project's issues that have no epic
//   - ?parent=KEY              an epic's child issues
//
// Issues come with their sub-tasks. It uses the viewer's own Jira access (it
// picks where time goes, like search), and results are cached per viewer.
func handleHierarchy(w http.ResponseWriter, r *http.Request) {
	session := getSessionFromRequest(r)
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	query := r.URL.Query()
	project, parent := query.Get("project"), query.Get("parent")
	client := jiraClientFor(session)

	var cacheKey string
	var load func(context.Context) (HierarchyRes, error)
	switch {
	case parent != "":
		if !issueKeyPattern.MatchString(parent) {
			http.Error(w, "Invalid issue key", http.StatusBadRequest)
			return
		}
		cacheKey = "parent:" + parent
		load = func(ctx context.Context) (HierarchyRes, error) { return client.GetChildIssues(ctx, parent) }
	case project != "":
		if !projectKeyPattern.MatchString(project) {
			http.Error(w, "Invalid project key", http.StatusBadRequest)
			return
		}
		if query.Get("no_epic") == "1" {
			cacheKey = "noepic:" + project
			load = func(ctx context.Context) (HierarchyRes, error) { return client.GetIssuesWithoutEpic(ctx, project) }
		} else {
			cacheKey = "project:" + project
			load = func(ctx context.Context) (HierarchyRes, error) { return client.GetProjectEpics(ctx, project) }
		}
	default:
		http.Error(w, "project or parent is required", http.StatusBadRequest)
		return
	}

	if res, ok := cache.GetHierarchy(session.AccountID, cacheKey); ok {
		writeJSON(w, res)
		return
	}

	res, err := load(r.Context())
	if errors.Is(err, errIssueNotFound) {
		http.Error(w, "Project not found", http.StatusNotFound)
		return
	}
	if err != nil {
		logrus.Errorf("Failed to load issue hierarchy (%s): %v", cacheKey, err)
		http.Error(w, "Failed to load issues", http.StatusInternalServerError)
		return
	}
	if res.Truncated {
		logrus.Infof("Issue hierarchy %s hit the %d issue limit", cacheKey, hierarchyLimit)
	}

	cache.SetHierarchy(session.AccountID, cacheKey, res)
	writeJSON(w, res)
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
