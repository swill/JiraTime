package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

// sdmPropertyKey is the worklog property recording the last change an SDM
// made to the worklog on its author's behalf (the calendar marks those)
const sdmPropertyKey = "jiratime.sdm"

// SDMChange is the value of the jiratime.sdm worklog property
type SDMChange struct {
	AccountID   string    `json:"account_id"`
	DisplayName string    `json:"display_name"`
	Action      string    `json:"action"`
	At          time.Time `json:"at"`
}

var (
	// errNotOwnWorklog: an SDM tried to change a worklog that is not the
	// viewed user's (e.g. a hand-crafted event ID)
	errNotOwnWorklog = errors.New("worklog does not belong to the user being viewed")
	// errOwnerNotSignedIn: a new worklog has to be authored by the viewed user,
	// but they have no usable JiraTime sign-in
	errOwnerNotSignedIn = errors.New("the user being viewed has no usable JiraTime sign-in")
	// errJiraUnavailable: a token refresh failed for a transient reason
	errJiraUnavailable = errors.New("unable to reach Jira")
)

// partialError is a change that was only partly applied and could not be
// undone, so it needs fixing in Jira by hand; the message says what to fix
type partialError struct {
	msg string
}

func (e *partialError) Error() string {
	return e.msg
}

// missingPermissionError is a Jira permission found missing before a
// multi-step change wrote anything
type missingPermissionError struct {
	who         string // empty for the signed-in user, else the viewed user's name
	issueKey    string
	permissions []string
}

func (e *missingPermissionError) Error() string {
	names := make([]string, len(e.permissions))
	for i, p := range e.permissions {
		names[i] = permissionNames[p]
	}
	project := strings.SplitN(e.issueKey, "-", 2)[0]
	if e.who == "" {
		return fmt.Sprintf("You need the %s permission in project %s for this change. Ask a Jira admin to add it; nothing was changed.", strings.Join(names, " and "), project)
	}
	return fmt.Sprintf("%s doesn't have the %s permission in project %s, so time can't be logged there for them; nothing was changed.", e.who, strings.Join(names, " and "), project)
}

// worklogWriter changes the time on the calendar a session is looking at.
//
// On your own calendar everything uses your own Jira access. When an SDM (or
// super user) changes someone else's calendar, edits, deletes and moves use
// the SDM's own access, so Jira records the SDM as the one who changed the
// worklog (this needs Edit All Worklogs and Delete All Worklogs). New worklogs
// are created with the viewed user's saved access instead, because Jira
// always makes the caller the author; they are then re-saved with the SDM's
// access so Jira's own "updated by" names the SDM too.
type worklogWriter struct {
	r       *http.Request
	session *UserSession
	edit    *JiraClient // the signed-in user's own access
	sdm     bool        // changing someone else's calendar
}

// newWorklogWriter checks that the session may change the calendar it is
// viewing, writing the error response if not
func newWorklogWriter(w http.ResponseWriter, r *http.Request) (*worklogWriter, bool) {
	session := getSessionFromRequest(r)
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return nil, false
	}

	// Managers only view; SDMs and super users can change what they view
	if isImpersonating(session) && !CanEditOthers(session) {
		http.Error(w, "Cannot change time while viewing another user's calendar", http.StatusForbidden)
		return nil, false
	}

	return &worklogWriter{r: r, session: session, edit: jiraClientFor(session), sdm: isImpersonating(session)}, true
}

// authorClient returns the access new worklogs are created with, so they are
// authored by the calendar's owner
func (wr *worklogWriter) authorClient() (*JiraClient, error) {
	if !wr.sdm {
		return wr.edit, nil
	}

	token, err := accountTokens.validToken(wr.session.ImpersonatingID)
	if err != nil {
		if isTokenRejected(err) {
			return nil, errOwnerNotSignedIn
		}
		return nil, fmt.Errorf("%w: %v", errJiraUnavailable, err)
	}
	return NewJiraClient(token, wr.session.CloudID), nil
}

// loadWorklog fetches the worklog about to be changed and makes sure it
// belongs to the calendar's owner: an SDM may only change the worklogs of
// the user they are viewing
func (wr *worklogWriter) loadWorklog(ctx context.Context, issueKey, worklogID string) (*JiraWorklog, error) {
	wl, err := wr.edit.GetWorklog(ctx, issueKey, worklogID)
	if err != nil {
		return nil, err
	}
	if wl.Author.AccountID != getEffectiveAccountID(wr.session) {
		return nil, errNotOwnWorklog
	}
	return wl, nil
}

// requirePermissions checks, before a multi-step change writes anything, that
// the user behind client holds the given Jira permissions on an issue, so the
// change cannot fail halfway for want of one. owner says whether client is
// the viewed user's access (for the error message).
func (wr *worklogWriter) requirePermissions(ctx context.Context, client *JiraClient, owner bool, issueKey string, permissions ...string) error {
	missing, err := client.MissingPermissions(ctx, issueKey, permissions...)
	if err != nil {
		return fmt.Errorf("failed to check Jira permissions on %s: %w", issueKey, err)
	}
	if len(missing) == 0 {
		return nil
	}

	who := ""
	if owner {
		who = wr.ownerName()
	}
	return &missingPermissionError{who: who, issueKey: issueKey, permissions: missing}
}

func (wr *worklogWriter) ownerName() string {
	if wr.session.ImpersonatingName != "" {
		return wr.session.ImpersonatingName
	}
	return "The user you're viewing"
}

// markChange records on a worklog which SDM last changed it and how
func (wr *worklogWriter) markChange(ctx context.Context, client *JiraClient, issueKey, worklogID, action string) {
	change := SDMChange{
		AccountID:   wr.session.AccountID,
		DisplayName: wr.session.DisplayName,
		Action:      action,
		At:          time.Now().UTC(),
	}
	if err := client.SetWorklogProperty(ctx, issueKey, worklogID, sdmPropertyKey, change); err != nil {
		logrus.Warnf("Failed to record SDM change on worklog %s/%s: %v", issueKey, worklogID, err)
	}
}

// stampCreated re-saves a worklog just created with the viewed user's access,
// unchanged, using the SDM's access: Jira then shows the SDM as the worklog's
// last updater, a record kept in Jira rather than only in JiraTime. Returns a
// note for the audit record when that fails (the worklog itself is fine).
func (wr *worklogWriter) stampCreated(ctx context.Context, issueKey, worklogID string, body map[string]interface{}) string {
	if _, err := wr.edit.putWorklog(ctx, issueKey, worklogID, body); err != nil {
		logrus.Warnf("Failed to record SDM %s as updater of worklog %s/%s: %v", wr.session.AccountID, issueKey, worklogID, err)
		return "worklog created, but Jira does not show the SDM as its updater: " + err.Error()
	}
	return ""
}

// audit records a change to someone else's time (own-calendar changes are
// not audited)
func (wr *worklogWriter) audit(rec AuditRecord) {
	if wr.sdm {
		writeAudit(wr.r, wr.session, rec)
	}
}

// auditFailure records a refused or partly applied change to someone else's
// time; other failures changed nothing and only go to the process log
func (wr *worklogWriter) auditFailure(rec AuditRecord, err error) {
	var partial *partialError
	switch {
	case errors.As(err, &partial):
		rec.Outcome = auditPartial
	case errors.Is(err, errNotOwnWorklog):
		rec.Outcome = auditDenied
	default:
		return
	}
	rec.Detail = err.Error()
	wr.audit(rec)
}

// move moves a worklog on someone else's calendar to another issue, applying
// any change to its time or description as well. The copy on the target is
// created with the viewed user's access (so they stay the author) and the
// original deleted with the SDM's: Jira records an ordinary create and delete,
// which time tracking, issue history and the worklog change feeds reporting
// syncs from all pick up. (Jira's worklog move API is not used: in testing it
// gave the worklog a new ID anyway and left the old one counted by JiraReports.)
// Returns the moved worklog's event ID, and a note for the audit record when
// Jira does not show the SDM as the copy's updater.
func (wr *worklogWriter) move(ctx context.Context, fromKey string, before *JiraWorklog, toKey string, start time.Time, durationSeconds int, description string) (string, string, error) {
	author, err := wr.authorClient()
	if err != nil {
		return "", "", err
	}

	body := worklogBody(formatJiraDateTime(start), durationSeconds, commentFor(before, description))

	eventID, err := moveWorklog(ctx, author, wr.edit, fromKey, before.ID, toKey, body)
	if err != nil {
		return "", "", err
	}
	newID := strings.TrimPrefix(eventID, toKey+"-")
	note := wr.stampCreated(ctx, toKey, newID, body)
	wr.markChange(ctx, author, toKey, newID, "move")
	return eventID, note, nil
}

// checkMove makes sure an SDM move can finish before anything is written: the
// viewed user must be able to log work on the target, where the copy is
// created as them, and the SDM must be able to delete the original
func (wr *worklogWriter) checkMove(ctx context.Context, fromKey, toKey string) error {
	author, err := wr.authorClient()
	if err != nil {
		return err
	}
	if err := wr.requirePermissions(ctx, author, true, toKey, permWorkOnIssues); err != nil {
		return err
	}
	return wr.requirePermissions(ctx, wr.edit, false, fromKey, permDeleteAllWorklogs)
}

// update changes another user's worklog with the SDM's access, keeping its
// stored comment unless the description was edited
func (wr *worklogWriter) update(ctx context.Context, issueKey string, before *JiraWorklog, start time.Time, durationSeconds int, description string) error {
	_, err := wr.edit.putWorklog(ctx, issueKey, before.ID, worklogBody(formatJiraDateTime(start), durationSeconds, commentFor(before, description)))
	return err
}

// commentFor is the comment to save for a worklog: the one Jira stores when
// the description was left alone (the dialog only shows its first line, so
// saving that would cut a longer comment down), otherwise the new description
func commentFor(wl *JiraWorklog, description string) interface{} {
	if wl != nil && description == wl.description() {
		return rawComment(wl.RawComment)
	}
	return descriptionComment(description)
}

// description returns a worklog comment's first line of text, which is what
// the edit dialog shows and saves
func (wl *JiraWorklog) description() string {
	if wl.Comment != nil && len(wl.Comment.Content) > 0 && len(wl.Comment.Content[0].Content) > 0 {
		return wl.Comment.Content[0].Content[0].Text
	}
	return ""
}

// worklogIDPattern matches Jira worklog IDs
var worklogIDPattern = regexp.MustCompile(`^[0-9]+$`)

// validIssueKeys reports whether every non-empty key looks like a Jira issue
// key. Keys from the browser are put into Jira API paths, which are called
// with another user's access while an SDM is editing, so anything else is
// rejected before it reaches Jira.
func validIssueKeys(keys ...string) bool {
	for _, key := range keys {
		if key != "" && !issueKeyPattern.MatchString(key) {
			return false
		}
	}
	return true
}

// parseEventID splits an event ID ({issueKey}-{worklogID}) into its parts
func parseEventID(eventID string) (issueKey, worklogID string, ok bool) {
	lastDash := strings.LastIndex(eventID, "-")
	if lastDash == -1 {
		return "", "", false
	}
	issueKey, worklogID = eventID[:lastDash], eventID[lastDash+1:]
	if !issueKeyPattern.MatchString(issueKey) || !worklogIDPattern.MatchString(worklogID) {
		return "", "", false
	}
	return issueKey, worklogID, true
}

// writeChangeError reports why a change failed: a clear message and status
// for the expected cases (SDM rules, sign-in, Jira permissions), and Jira's
// own error otherwise
func writeChangeError(w http.ResponseWriter, wr *worklogWriter, action string, err error) {
	var permErr *missingPermissionError
	var partial *partialError
	switch {
	case errors.Is(err, errNotOwnWorklog):
		logrus.Warnf("%s tried to %s a worklog not belonging to %s", wr.session.AccountID, action, getEffectiveAccountID(wr.session))
		http.Error(w, "This entry doesn't belong to the user you're viewing", http.StatusForbidden)
	case errors.Is(err, errOwnerNotSignedIn):
		http.Error(w, fmt.Sprintf("%s needs to sign in to JiraTime first: new worklogs for them (adding, copying, splitting or moving time) have to be created with their own sign-in so they stay the author. Nothing was changed.", wr.ownerName()), http.StatusConflict)
	case errors.Is(err, errJiraUnavailable):
		logrus.Warnf("Failed to %s: %v", action, err)
		http.Error(w, "Unable to reach Jira, please try again shortly", http.StatusServiceUnavailable)
	case errors.As(err, &permErr):
		http.Error(w, permErr.Error(), http.StatusForbidden)
	case errors.Is(err, errIssueNotFound) && wr.sdm:
		// The calendar is read with the viewed user's access, so it can show
		// entries on projects the SDM can't open
		http.Error(w, "Jira didn't find this entry with your access: you may not have access to its project, or it was deleted. Nothing was changed.", http.StatusNotFound)
	case errors.Is(err, errIssueNotFound):
		http.Error(w, "Time entry not found: it may have been changed or deleted in Jira. Refresh and try again.", http.StatusNotFound)
	case errors.As(err, &partial):
		logrus.Errorf("Failed to %s, partly applied: %v", action, err)
		http.Error(w, partial.Error(), http.StatusInternalServerError)
	default:
		logrus.Errorf("Failed to %s: %v", action, err)
		http.Error(w, fmt.Sprintf("Failed to %s: %v", action, err), http.StatusInternalServerError)
	}
}
