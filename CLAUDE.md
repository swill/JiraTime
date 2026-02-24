# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

JiraTime is a Go application for calendar-based Jira time tracking. It replaces a similar Teamwork-based application (`../betterwork`).

## Build Commands

```bash
make          # Build frontend and Go binary
make dev      # Run development server
go test ./... # Run all tests
go vet ./...  # Run static analysis
```

## Architecture

### Tech Stack
- **Backend:** Go with embedded static files (`//go:embed static`)
- **Frontend:** Vanilla JavaScript + FullCalendar (CDN, no build step)
- **Calendar:** FullCalendar 6.x (https://fullcalendar.io/) with interaction plugin
- **Auth:** Jira Cloud OAuth 2.0 (3LO)
- **Target:** Single binary deployment
- **HTTPS:** Let's Encrypt via `golang.org/x/crypto/acme/autocert` (enabled when PORT=443)

### Design Principles
- Reduce dependencies wherever possible to simplify maintenance
- Use best practices throughout
- Makefile for common commands

### Configuration
- Use Viper (`github.com/spf13/viper`) for config management
- Config file: `config.toml` with `SCREAMING_SNAKE_CASE` keys
- Environment variables override config file values (`viper.AutomaticEnv()`)
- Validate required config keys at startup, fatal if missing
- Key config options:
  - `JIRA_CLIENT_ID`, `JIRA_CLIENT_SECRET`: OAuth credentials (required)
  - `BASE_URL`: Application URL for OAuth callback (required)
  - `SESSION_SECRET`: 32+ char secret for session signing (required)
  - `PORT`: HTTP server port (default: 8080)
  - `HOURS_TARGET`: Weekly hours target for widget (default: 40)
  - `ACTIVE_ISSUES_WEEKS`: Activity window for Active Issues (default: 4)
  - `DONE_ISSUES_WEEKS`: How long Done issues stay visible (default: 2)

### Code Style Reference
The `../fitops` and `../timework` projects demonstrate preferred patterns:
- Handler functions: `handleXxx`
- Page structs: `PageXxx`
- Request/response structs: `XxxReq` / `XxxRes`
- JSON tags: `snake_case`
- Config keys: `SCREAMING_SNAKE_CASE`

### Jira Integration
- Jira Cloud only (works with both Jira and Jira Service Management)
- OAuth 2.0 (3LO) for authentication (user-friendly, no API token setup)
- Uses Jira REST API v3 (POST `/search/jql` endpoint)
- OAuth scopes: `read:me`, `read:account`, `read:jira-user`, `read:jira-work`, `write:jira-work`, `read:servicedesk-request`, `write:servicedesk-request`
- `offline_access` scope is added in code to obtain refresh tokens
- Data hierarchy: Project → Issue → Worklog
- External links use site URL obtained during OAuth flow

### Reference Implementation
The `../betterwork` codebase is useful for understanding the existing functionality and user workflows, but should not dictate code structure. Follow Go best practices for project organization rather than mirroring betterwork's layout.

## Features

### Calendar Functionality
- Day/Week views with 30-minute increments
- Drag & drop event moving
- Event resize for duration adjustment
- Shift+drag to copy events
- Custom event edit dialog (native `<dialog>` element)
- Search dims non-matching events on calendar
- Mobile responsive (auto-collapse sidebar, switch to day view)

### Mobile Interactions
- **Tap-to-create:** Tap issue in sidebar to select, then tap calendar to create entry
- **Single tap on event:** Select event (enables drag/resize handles)
- **Double tap on event:** Open edit dialog
- **Long press on event (~500ms):** Open edit dialog

### Sidebar
- **Recent Issues:** 5 most recently used issues for quick access (stored in cookie)
- **Active Issues:** Issues where user is assignee, reporter, or has logged time, organized hierarchically by project
  - Filtered to issues with recent activity (configurable via `ACTIVE_ISSUES_WEEKS`, default 4 weeks)
  - Done issues remain visible for configurable period (`DONE_ISSUES_WEEKS`, default 2 weeks)
  - Collapsible project headers
  - Issues displayed as `[PROJECT-123] Issue Title` with external link icon
  - Draggable onto calendar to create time entries (uses FullCalendar external dragging)
- **Search Results:** Search all Jira issues (not just assigned), returns up to 100 results
  - Appears when search query is 2+ characters
  - Uses remaining sidebar space with scrollable list

### Hours Tracking Widget
- Displays current week hours vs. target (e.g., "32/40 hours")
- Color coding: red when under target, green when met
- Configurable hours target via `HOURS_TARGET` config (default: 40)

### Drag-to-Create Behavior
- When dragging an issue onto the calendar:
  - If all required data is available: create entry immediately (30-min default)
  - If data is missing: open edit dialog to complete
- Resizing or moving updates the entry

### Custom Field Handling
- The following custom fields on Issues are used to track the total number of minutes in a given category
  - `customfield_11710` - Billable Time
  - `customfield_11712` - Smart Hands and Eyes
  - `customfield_12073` - Smart Hands and Eyes (After Hours)
- If any of these custom fields are present on an issue, the JiraTime time edit popup should include a checkbox per appropriate custom field
- If the checkbox is checked, the duration of the worklog should be added to the value of the appropriate custom field
- If the checkbox was checked and is unchecked, then the duration of the worklog should be removed from the value of the appropriate custom field
- If the checkbox was checked and the worklog is deleted, then the duration of the worklog should be removed from the value of the appropriate custom field
- If the checkbox is checked and the worklog duration changes, the change in duration should be reflected in the value of the custom field (example: If the duration changes from 90 to 60 minutes, then 30 minutes is removed from the custom field, etc)
- The "checked" status of the appropriate custom field checkbox and the duration value contributed to the custom field value should be tracked in the work log properties (API: `/rest/api/3/issue/{issueIdOrKey}/worklog/{worklogId}/properties/{propertyKey}`)

## Design Decisions

- **No billable/non-billable tracking** - not needed for this implementation
- **Single Jira instance** - no multi-tenant support required
- **Issues as primary entity** - users think in terms of issues, not tasks/subtasks
