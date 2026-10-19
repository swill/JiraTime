package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// auditLogFile records every change an SDM makes to someone else's time, one
// JSON object per line. It is only ever appended to and is kept apart from the
// process log (which Supervisor rotates), so forward it to central logging to
// keep it out of reach of anyone who can edit files on this server.
const auditLogFile = "audit.log"

// Outcomes of an audited change
const (
	auditOK      = "ok"      // fully applied
	auditPartial = "partial" // some steps applied and could not be undone: needs fixing in Jira
	auditDenied  = "denied"  // refused before anything was written
)

// AuditRecord is one SDM change. Worklog values use Jira's own fields so a
// record can be matched against Jira (and, for moves, undone) by hand.
type AuditRecord struct {
	Time        time.Time      `json:"time"`
	Actor       string         `json:"actor"`        // account ID of the SDM
	OnBehalfOf  string         `json:"on_behalf_of"` // account ID of the user whose time changed
	IP          string         `json:"ip"`
	Action      string         `json:"action"` // create, update, move, split or delete
	Outcome     string         `json:"outcome"`
	IssueKey    string         `json:"issue_key,omitempty"`
	WorklogID   string         `json:"worklog_id,omitempty"`
	TargetKey   string         `json:"target_key,omitempty"` // destination of a move or split
	NewID       string         `json:"new_worklog_id,omitempty"`
	Before      *AuditWorklog  `json:"before,omitempty"`
	After       *AuditWorklog  `json:"after,omitempty"`
	PriorPeriod bool           `json:"prior_period,omitempty"` // the entry starts before the current month
	Detail      string         `json:"detail,omitempty"`
	Extra       map[string]any `json:"extra,omitempty"`
}

// AuditWorklog is the state of a worklog before or after a change. Started is
// in UTC (RFC 3339) whatever offset Jira or the browser used, so before and
// after values compare at a glance.
type AuditWorklog struct {
	IssueKey         string `json:"issue_key"`
	Started          string `json:"started"`
	TimeSpentSeconds int    `json:"time_spent_seconds"`
}

var auditLock sync.Mutex

// writeAudit appends an SDM change to the audit log and echoes it to the
// process log. Failing to write the audit file is logged loudly but does not
// fail the request: the change has already been made in Jira by then.
func writeAudit(r *http.Request, session *UserSession, rec AuditRecord) {
	rec.Time = time.Now().UTC()
	rec.Actor = session.AccountID
	rec.OnBehalfOf = getEffectiveAccountID(session)
	rec.IP = clientIP(r)

	entry := logrus.WithFields(logrus.Fields{
		"audit":        true,
		"actor":        rec.Actor,
		"on_behalf_of": rec.OnBehalfOf,
		"ip":           rec.IP,
		"action":       rec.Action,
		"outcome":      rec.Outcome,
		"issue":        rec.IssueKey,
		"worklog":      rec.WorklogID,
		"target":       rec.TargetKey,
	})
	if rec.Outcome == auditPartial {
		entry.Errorf("SDM change partly applied, needs fixing in Jira: %s", rec.Detail)
	} else {
		entry.Infof("SDM %s %s", rec.Action, rec.Outcome)
	}

	line, err := json.Marshal(rec)
	if err != nil {
		logrus.Errorf("Failed to marshal audit record: %v", err)
		return
	}

	auditLock.Lock()
	defer auditLock.Unlock()

	f, err := os.OpenFile(auditLogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		logrus.Errorf("Failed to open %s, audit record only in process log: %v", auditLogFile, err)
		return
	}
	defer f.Close()

	if _, err := f.Write(append(line, '\n')); err != nil {
		logrus.Errorf("Failed to write %s, audit record only in process log: %v", auditLogFile, err)
	}
}

// clientIP returns the address the request came from. JiraTime serves
// browsers directly, so forwarding headers (which clients can forge) are not
// trusted.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// auditWorklogOf captures a worklog's state for an audit record
func auditWorklogOf(issueKey string, wl *JiraWorklog) *AuditWorklog {
	if wl == nil {
		return nil
	}
	started := wl.Started
	if t, err := parseJiraDateTime(wl.Started); err == nil {
		started = auditTime(t)
	}
	return &AuditWorklog{IssueKey: issueKey, Started: started, TimeSpentSeconds: wl.TimeSpentSeconds}
}

// auditTime formats a worklog start for an audit record
func auditTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// isPriorPeriod reports whether a worklog starts before the current calendar
// month, so it may already have been invoiced
func isPriorPeriod(started time.Time) bool {
	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	return started.Before(monthStart)
}
