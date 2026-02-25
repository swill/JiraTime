package main

import (
	"time"

	"golang.org/x/oauth2"
)

// UserSession represents an authenticated user session
type UserSession struct {
	AccountID   string        `json:"account_id"`
	CloudID     string        `json:"cloud_id"`
	SiteURL     string        `json:"site_url"`
	DisplayName string        `json:"display_name"`
	Email       string        `json:"email"`
	AvatarURL   string        `json:"avatar_url"`
	Token       *oauth2.Token `json:"token"`
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
}

// CalendarEvent represents a worklog as a calendar event
type CalendarEvent struct {
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	Start         time.Time `json:"start"`
	End           time.Time `json:"end"`
	IssueKey      string    `json:"issue_key"`
	IssueID       string    `json:"issue_id"`
	WorklogID     string    `json:"worklog_id"`
	Description   string    `json:"description,omitempty"`
	FromJiraTime  bool      `json:"from_jiratime"`
}

// CreateEventReq is the request body for creating a worklog
type CreateEventReq struct {
	IssueKey              string          `json:"issue_key"`
	Start                 string          `json:"start"` // ISO 8601 format
	DurationMin           int             `json:"duration_min"`
	Description           string          `json:"description,omitempty"`
	CustomFieldSelections map[string]bool `json:"custom_field_selections,omitempty"`
}

// UpdateEventReq is the request body for updating a worklog
type UpdateEventReq struct {
	Start                 string          `json:"start,omitempty"` // ISO 8601 format
	DurationMin           int             `json:"duration_min,omitempty"`
	Description           string          `json:"description,omitempty"`
	CustomFieldSelections map[string]bool `json:"custom_field_selections,omitempty"`
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
	AccountID   string `json:"accountId"`
	DisplayName string `json:"displayName"`
	EmailAddress string `json:"emailAddress"`
	AvatarURLs  struct {
		Large string `json:"48x48"`
	} `json:"avatarUrls"`
}

type JiraAccessibleResource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

type JiraIssueSearchResponse struct {
	Issues []struct {
		ID     string `json:"id"`
		Key    string `json:"key"`
		Fields struct {
			Summary string `json:"summary"`
			Project struct {
				ID   string `json:"id"`
				Key  string `json:"key"`
				Name string `json:"name"`
			} `json:"project"`
		} `json:"fields"`
	} `json:"issues"`
}

type JiraWorklog struct {
	ID               string `json:"id"`
	IssueID          string `json:"issueId"`
	TimeSpentSeconds int    `json:"timeSpentSeconds"`
	Started          string `json:"started"` // Format: 2021-01-17T12:34:00.000+0000
	Comment          *struct {
		Content []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"content"`
	} `json:"comment,omitempty"`
	Author struct {
		AccountID string `json:"accountId"`
	} `json:"author"`
}

type JiraWorklogResponse struct {
	Worklogs []JiraWorklog `json:"worklogs"`
}

// StoredTokens holds refresh tokens for persistence
type StoredTokens struct {
	Tokens map[string]*oauth2.Token `json:"tokens"` // keyed by account_id
}

// CustomTimeField represents a custom field that tracks time in minutes
type CustomTimeField struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// CustomFieldContributions tracks how much time a worklog has contributed to custom fields
type CustomFieldContributions struct {
	Contributions map[string]int `json:"contributions"` // fieldID -> minutes contributed
}

// CustomFieldInfo represents the state of a custom field for an issue
type CustomFieldInfo struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	CurrentValue int    `json:"current_value"` // Current value in minutes
	Available    bool   `json:"available"`     // Whether field exists on the issue
}
