package main

import (
	"encoding/json"
	"time"

	"golang.org/x/oauth2"
)

// UserSession represents an authenticated user session. The OAuth token is not
// held here: it lives in accountTokens, shared by all of the account's sessions.
type UserSession struct {
	AccountID   string `json:"account_id"`
	CloudID     string `json:"cloud_id"`
	SiteURL     string `json:"site_url"`
	DisplayName string `json:"display_name"`
	// Sign-in email from Atlassian's identity service; super users are
	// matched on it, and only when Atlassian reports it verified
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified,omitempty"`
	AvatarURL     string `json:"avatar_url"`
	// Impersonation fields
	ImpersonatingID   string `json:"impersonating_id,omitempty"`
	ImpersonatingName string `json:"impersonating_name,omitempty"`
}

// Project represents a Jira project
type Project struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

// Issue represents a Jira issue
type Issue struct {
	ID         string `json:"id"`
	Key        string `json:"key"`
	Summary    string `json:"summary"`
	ProjectKey string `json:"project_key"`
	// Set by search so the SDM Move-to picker can show where a result sits
	ParentKey     string `json:"parent_key,omitempty"`
	ParentSummary string `json:"parent_summary,omitempty"`
}

// CalendarEvent represents a worklog as a calendar event
type CalendarEvent struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	IssueKey  string    `json:"issue_key"`
	IssueID   string    `json:"issue_id"`
	WorklogID string    `json:"worklog_id"`
	// The whole worklog comment as plain text (see adfText)
	Description string `json:"description,omitempty"`
	// Set when the comment has formatting, mentions, images, etc. that
	// editing the description (saved as plain text) would lose
	CommentFormatted bool `json:"comment_formatted,omitempty"`
	FromJiraTime     bool `json:"from_jiratime"`
	// Set when the worklog lives on a configured sub-task (see the
	// billable_subtasks / non_billable_subtasks project properties)
	ParentKey       string `json:"parent_key,omitempty"`
	SubtaskTypeID   string `json:"subtask_type_id,omitempty"`
	SubtaskTypeName string `json:"subtask_type_name,omitempty"`
	// Set when the issue's project is archived: Jira makes its issues
	// read-only, so the entry can be viewed but not changed
	ProjectArchived bool `json:"project_archived,omitempty"`
	// Set when an SDM last changed the entry on the user's behalf (from the
	// jiratime.sdm worklog property)
	SDMChangedBy string     `json:"sdm_changed_by,omitempty"`
	SDMChangedAt *time.Time `json:"sdm_changed_at,omitempty"`
}

// CreateEventReq is the request body for creating a worklog
type CreateEventReq struct {
	IssueKey    string `json:"issue_key"`
	Start       string `json:"start"` // ISO 8601 format
	DurationMin int    `json:"duration_min"`
	Description string `json:"description,omitempty"`
	// Optional billable sub-task association: log against a specific existing
	// sub-task, or get-or-create a sub-task of the given type under IssueKey
	SubtaskKey    string `json:"subtask_key,omitempty"`
	SubtaskTypeID string `json:"subtask_type_id,omitempty"`
}

// UpdateEventReq is the request body for updating a worklog
type UpdateEventReq struct {
	Start       string `json:"start,omitempty"` // ISO 8601 format
	DurationMin int    `json:"duration_min,omitempty"`
	// Sent by dialog saves only; drag/resize leave it out so the stored
	// comment is never rewritten by them
	Description *string `json:"description,omitempty"`
	// Sub-task association, only honoured when ParentKey is set (dialog saves).
	// Drag/resize updates omit ParentKey and keep the worklog where it is.
	ParentKey     string `json:"parent_key,omitempty"`
	SubtaskKey    string `json:"subtask_key,omitempty"`
	SubtaskTypeID string `json:"subtask_type_id,omitempty"`
}

// SplitEventReq is the request body for splitting a worklog in two: the
// original keeps DurationMin minutes and the rest becomes a new worklog,
// starting where the first part ends, on the issue chosen by ParentKey and
// the optional sub-task fields (resolved like a dialog save)
type SplitEventReq struct {
	DurationMin   int    `json:"duration_min"`
	ParentKey     string `json:"parent_key"`
	SubtaskKey    string `json:"subtask_key,omitempty"`
	SubtaskTypeID string `json:"subtask_type_id,omitempty"`
}

// HierarchyNode is an issue in the SDM Move-to picker's Epic > Issue >
// Sub-task tree
type HierarchyNode struct {
	Key      string          `json:"key"`
	Summary  string          `json:"summary"`
	Status   string          `json:"status"`
	Done     bool            `json:"done"`
	Subtasks []HierarchyNode `json:"subtasks,omitempty"`
}

// HierarchyRes is one level of the Move-to tree, loaded as it is expanded:
// a project's epics, an epic's child issues, or a project's issues without an
// epic. Truncated means only the most recently updated issues are included.
type HierarchyRes struct {
	Project   *Project        `json:"project,omitempty"`
	Epics     []HierarchyNode `json:"epics,omitempty"`
	Issues    []HierarchyNode `json:"issues,omitempty"`
	Truncated bool            `json:"truncated"`
}

// Sub-task configuration modes. A project configures either billable sub-tasks
// (on a non-billable project, so logged time is reclassified as billable) or
// non-billable sub-tasks (on a billable project, so time is reclassified as
// non-billable). The two are mutually exclusive per project.
const (
	SubtaskModeBillable    = "billable"
	SubtaskModeNonBillable = "non_billable"
)

// ProjectSubtaskConfig is the sub-task configuration resolved from a project's
// jirametadata property: the configured sub-task issue type IDs and the mode
// (billable vs non-billable) they represent. Mode is empty when none are set.
type ProjectSubtaskConfig struct {
	TypeIDs []string
	Mode    string
}

// SubtaskType is a sub-task issue type configured for a project (billable or
// non-billable, per the project's ProjectSubtaskConfig mode)
type SubtaskType struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SubtaskInfo is an existing sub-task under a parent issue
type SubtaskInfo struct {
	Key     string `json:"key"`
	Summary string `json:"summary"`
	TypeID  string `json:"type_id"`
	Status  string `json:"status"`
}

// SubtaskOptionsRes is the response for GET /api/issues/{key}/subtask-options.
// If the requested issue is itself a configured sub-task, it is resolved to its
// parent and CurrentTypeID/CurrentSubtask reflect the existing association.
// SubtaskMode ("billable" or "non_billable") tells the UI how to label the
// choices; it is empty when the project has no sub-tasks configured.
type SubtaskOptionsRes struct {
	IssueKey       string        `json:"issue_key"`
	ProjectKey     string        `json:"project_key"`
	IssueSummary   string        `json:"issue_summary"`
	IssueTitle     string        `json:"issue_title"`
	SubtaskMode    string        `json:"subtask_mode,omitempty"`
	SubtaskTypes   []SubtaskType `json:"subtask_types"`
	Subtasks       []SubtaskInfo `json:"subtasks"`
	CurrentTypeID  string        `json:"current_type_id,omitempty"`
	CurrentSubtask string        `json:"current_subtask,omitempty"`
}

// HoursSummary represents weekly hours tracking
type HoursSummary struct {
	WeekStart   string  `json:"week_start"`
	HoursLogged float64 `json:"hours_logged"`
	HoursTarget float64 `json:"hours_target"`
}

// IssuesByProject groups issues by their project
type IssuesByProject struct {
	Project Project `json:"project"`
	Issues  []Issue `json:"issues"`
}

// Jira API response types

type JiraUser struct {
	AccountID    string `json:"accountId"`
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress"`
	AvatarURLs   struct {
		Large string `json:"48x48"`
	} `json:"avatarUrls"`
}

type JiraAccessibleResource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// JiraIssueSearchResponse is one page of results from POST /search/jql
type JiraIssueSearchResponse struct {
	Issues        []JiraSearchIssue `json:"issues"`
	NextPageToken string            `json:"nextPageToken"`
	IsLast        bool              `json:"isLast"`
}

// JiraSearchIssue is an issue as returned by the JQL search
type JiraSearchIssue struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Fields struct {
		Summary string `json:"summary"`
		Project struct {
			ID   string `json:"id"`
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"project"`
		IssueType JiraIssueTypeRef `json:"issuetype"`
		Parent    *JiraParentRef   `json:"parent,omitempty"`
		// Only requested by the Move-to tree
		Status   JiraStatusRef `json:"status"`
		Subtasks []struct {
			Key    string `json:"key"`
			Fields struct {
				Summary string        `json:"summary"`
				Status  JiraStatusRef `json:"status"`
			} `json:"fields"`
		} `json:"subtasks,omitempty"`
	} `json:"fields"`
}

// JiraStatusRef is an issue status as returned in issue fields
type JiraStatusRef struct {
	Name           string `json:"name"`
	StatusCategory struct {
		Key string `json:"key"` // "new", "indeterminate" or "done"
	} `json:"statusCategory"`
}

// JiraIssueTypeRef is an issue type reference as returned in issue fields
type JiraIssueTypeRef struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Subtask bool   `json:"subtask"`
	// Position in the issue hierarchy: -1 sub-task, 0 standard issue, 1 Epic,
	// 2+ levels above Epic
	HierarchyLevel int `json:"hierarchyLevel"`
}

// JiraParentRef is a parent issue reference as returned in issue fields
type JiraParentRef struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Fields struct {
		Summary string `json:"summary"`
	} `json:"fields"`
}

// JiraIssueDetail is a single issue fetched with hierarchy fields
type JiraIssueDetail struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Fields struct {
		Summary string `json:"summary"`
		Project struct {
			ID   string `json:"id"`
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"project"`
		IssueType JiraIssueTypeRef `json:"issuetype"`
		Parent    *JiraParentRef   `json:"parent,omitempty"`
		Subtasks  []struct {
			ID     string `json:"id"`
			Key    string `json:"key"`
			Fields struct {
				Summary string `json:"summary"`
				Status  struct {
					Name string `json:"name"`
				} `json:"status"`
				IssueType JiraIssueTypeRef `json:"issuetype"`
			} `json:"fields"`
		} `json:"subtasks"`
	} `json:"fields"`
}

// JiraIssueType is an entry from the /issuetype endpoint
type JiraIssueType struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Subtask bool   `json:"subtask"`
}

type JiraWorklog struct {
	ID               string   `json:"id"`
	IssueID          string   `json:"issueId"`
	TimeSpentSeconds int      `json:"timeSpentSeconds"`
	Started          string   `json:"started"` // Format: 2021-01-17T12:34:00.000+0000
	Comment          *adfNode `json:"comment,omitempty"`
	Author           struct {
		AccountID string `json:"accountId"`
	} `json:"author"`
	// Only populated when fetched with expand=properties
	Properties []struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	} `json:"properties,omitempty"`
	// The comment exactly as Jira stores it (Atlassian Document Format), so
	// it can be copied or written back unchanged. Only set by GetWorklog.
	RawComment json.RawMessage `json:"-"`
}

// JiraWorklogResponse is one page of an issue's worklogs
type JiraWorklogResponse struct {
	StartAt  int           `json:"startAt"`
	Total    int           `json:"total"`
	Worklogs []JiraWorklog `json:"worklogs"`
}

// StoredTokens holds refresh tokens for persistence
type StoredTokens struct {
	Tokens map[string]*oauth2.Token `json:"tokens"` // keyed by account_id
}

// Roles for entries on the manager list
const (
	RoleManager = "manager" // views other users' calendars read-only
	RoleSDM     = "sdm"     // Service Delivery Manager: can also change their time
)

// Manager is a user who can view other users' calendars. Managers are
// administered in-app by super users; the role decides whether they can also
// change what they see (RoleSDM) or only view it (RoleManager).
type Manager struct {
	AccountID   string    `json:"account_id"`
	DisplayName string    `json:"display_name"`
	AvatarURL   string    `json:"avatar_url,omitempty"`
	Role        string    `json:"role"`
	AddedBy     string    `json:"added_by"` // account ID of the super user who added them
	AddedAt     time.Time `json:"added_at"`
}

// StoredManagers holds the manager list for persistence
type StoredManagers struct {
	Managers []Manager `json:"managers"`
}

// ManagerReq is the request body for creating/updating a manager. An empty
// Role keeps an existing manager's role (RoleManager for a new one).
type ManagerReq struct {
	AccountID   string `json:"account_id"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	Role        string `json:"role,omitempty"`
}
