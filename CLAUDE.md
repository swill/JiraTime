# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

JiraTime is a Go application for calendar-based Jira time tracking. It replaces a similar Teamwork-based application (`../betterwork`).

## Build Commands

```bash
make build    # Build Go binary
make compile  # Cross-compile for Linux/macOS/Windows (outputs to bin/)
make dev      # Run development server
make deploy   # Deploy to remote server (requires __config.sh)
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
  - `SUPER_USERS`: List of account IDs that can impersonate other users (default: [])

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

### Deployment
- Remote deployment via `make deploy` using Supervisor for process management
- Deployment config in `__config.sh` (gitignored): `USER`, `GROUP`, `SERVER` variables
- Production config in `config.prod.toml` (gitignored)
- `supervisor.conf` template is copied and paths are adjusted via sed on the remote server
- Binary is cross-compiled for Linux via `make compile`

### Reference Implementation
The `../betterwork` codebase is useful for understanding the existing functionality and user workflows, but should not dictate code structure. Follow Go best practices for project organization rather than mirroring betterwork's layout.

## Features

### Calendar Functionality
- Day/Week views with 30-minute increments
- Drag & drop event moving
- Event resize for duration adjustment
- Shift+drag to copy events
- Custom event edit dialog (native `<dialog>` element)
- Mobile responsive (auto-collapse sidebar, switch to day view)

### Mobile Interactions
- **Tap-to-create:** Tap issue in sidebar to select, then tap calendar to create entry
- **Single tap on event:** Select event (enables drag/resize handles)
- **Double tap on event:** Open edit dialog

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
  - Supports partial matching via JQL wildcards (e.g., "test" matches "testing", "ABC" matches "ABC-123")

### Hours Tracking Widget
- Displays current week hours vs. target (e.g., "32/40 hours")
- Color coding: red when under target, green when met
- Configurable hours target via `HOURS_TARGET` config (default: 40)

### Drag-to-Create Behavior
- When dragging an issue onto the calendar (same for mobile tap-to-create):
  - If the project has no billable sub-tasks configured: create entry immediately (30-min default)
  - If the project has billable sub-tasks: open the edit dialog so a sub-task can be chosen
- Resizing or moving updates the entry in place (never changes the sub-task association)

### Billable Sub-tasks
- Projects may define billable sub-task issue type IDs in the `jirametadata` project property (`value.billable_subtasks`), managed by the companion `../jirametadata` Forge app
- `GET /api/issues/{key}/subtask-options` returns the checkbox choices for the edit dialog; a billable sub-task resolves to its parent with `current_type_id`/`current_subtask` pre-set
- When configured, drag/tap-to-create opens the edit dialog instead of logging immediately; the dialog shows one checkbox per type (radio-behaving, deselectable) plus a nested list of the parent's existing sub-tasks of the selected type
- Type selected without a specific sub-task → get-or-create a sub-task named after the type (case-insensitive summary match); no type selected → worklog goes on the parent
- Worklog target resolution and validation happen server-side (`resolveWorklogTarget` in handlers.go)
- Changing the association on an existing entry moves the worklog: create on target first, then delete the original, with best-effort rollback on delete failure (Jira has no move API); moved worklogs are marked JiraTime-created
- Calendar events on billable sub-tasks are titled `[PARENT-KEY] Parent summary • Type Name` and carry `parent_key`/`subtask_type_id`/`subtask_type_name`
- Project property and issue-type lookups are cached site-wide (15 min TTL); the Refresh button also invalidates these via `cache.InvalidateSite`

### Worklog Source Tracking
- Worklogs created via JiraTime are marked with a `jiratime.source` property
- Calendar displays visual indicator: solid left border (JiraTime) vs dashed left border (external)
- Source is checked via worklog properties API when fetching events
- `CalendarEvent.FromJiraTime` boolean field indicates the source

### Manager Impersonation
- Super users (defined in `SUPER_USERS` config) can view other users' calendars
- View-only mode: all create/update/delete operations are blocked
- Impersonation state stored in session (`ImpersonatingID`, `ImpersonatingName`)
- JQL queries use the impersonated user's account ID instead of `currentUser()`
- API endpoints:
  - `GET /api/users/search?q=X` - Search users (super users only)
  - `POST /api/impersonate` - Start impersonating `{account_id, display_name}`
  - `POST /api/impersonate/stop` - Stop impersonating
- Frontend shows yellow banner with "Viewing as: [Name]" and Stop button
- `is_super_user` flag returned in `/api/user` response

### Time Range Settings
- Configurable visible time range for calendar (supports different shifts/timezones)
- Presets: Day (6am-10pm), Early (4am-2pm), Late (2pm-12am), Night (8pm-8am), Full Day
- Custom option allows arbitrary start/end times
- Overnight ranges supported (when end < start, end is extended past 24:00 for FullCalendar)
- Stored in localStorage (`timeRangeSettings` key)
- Applied dynamically via `calendar.setOption()` without page reload
- Gear icon (⚙) in sidebar footer opens settings dialog

## Design Decisions

- **Single Jira instance** - no multi-tenant support required
- **Issues as primary entity** - users think in terms of issues, not tasks/subtasks
