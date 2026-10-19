# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

JiraTime is a Go application for calendar-based Jira time tracking. It replaces a similar Teamwork-based application (`../betterwork`).

## Build Commands

```bash
make build    # Build Go binary
make test     # Run Go unit tests
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
  - `DONE_ISSUES_DAYS`: How long Done issues stay available to log time against (default: 90)
  - `SUPER_USERS`: Site administrators' Atlassian sign-in emails (case-insensitive); can impersonate and edit other users' time (like SDMs) and manage Managers/SDMs in-app (default: []). Entries without "@" are warned about at startup and ignored

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
- One OAuth token per account (`accountTokens` in auth.go), shared by all of a user's sessions/devices; refreshes are serialized per account because Atlassian rotates refresh tokens. Only a rejected refresh token (4xx from the token endpoint) signs the user out; transient failures return 503. Auth cookies slide (renewed on every request, 90 days)
- JQL searches follow `nextPageToken` (`searchIssues`); read-only calls retry 429/502/503/504 (`doReadRequest`), writes are never retried, except the undo steps of a half-finished change (`RollbackWorklog`, `RestoreWorklog`), which are safe to repeat (`doRetryingRequest`)
- Issue keys and worklog IDs from the browser are validated (`validIssueKeys`, `parseEventID`) before they are put into Jira API paths
- Worklogs are fetched with `expand=properties` (source marker inline) and `startedAfter`/`startedBefore`; a failed worklog fetch fails the request rather than silently dropping the issue
- Jira leaves archived projects out of search, so the calendar/hours worklog search sets `includeArchivedProjects` (Active Issues and Search do not: archived issues can't take new time). Entries on archived projects come back with `project_archived` and are read-only in the UI (no move/resize/copy, view-only dialog). Archived status comes from `/project/{key}` (`GetProjectArchivedCached`, site cache); a 404 counts as archived, other lookup errors leave the entry editable. Individually archived issues (Premium) are not returned by search at all
- FullCalendar is pinned to 6.1.21 (6.1.17+ refreshes the now indicator when a hidden tab becomes visible)

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
- Day/Week choice stored in localStorage (`calendarView`) and kept through window resizes; narrow (mobile) widths force Day view without changing it (`checkMobileView` only acts when crossing the 768px breakpoint)
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
  - Done issues remain visible for `DONE_ISSUES_DAYS` (default 90), but also need activity within `ACTIVE_ISSUES_WEEKS`, so they leave the sidebar sooner than search
  - Epic-level issues (`issuetype.hierarchyLevel >= 1`) are excluded (`isEpicLevel` in jira.go)
  - Collapsible project headers
  - Issues displayed as `[PROJECT-123] Issue Title` with external link icon
  - Draggable onto calendar to create time entries (uses FullCalendar external dragging)
- **Search Results:** Search all Jira issues (not just assigned), returns up to 100 results
  - Same Done window (`DONE_ISSUES_DAYS`) and Epic exclusion as Active Issues; SDMs editing someone's calendar search all Done issues regardless of age
  - Search terms are escaped for JQL (`jqlString`)
  - Appears when search query is 2+ characters
  - Uses remaining sidebar space with scrollable list
  - Supports partial matching via JQL wildcards (e.g., "test" matches "testing", "ABC" matches "ABC-123")

### Hours Tracking Widget
- Displays current week hours vs. target (e.g., "32/40 hours"), to 2 decimals without trailing zeros ("0.25")
- Always covers the Monday-Sunday week being viewed, even in Day view; the client sends its local week bounds (`/api/hours?start=&end=`, RFC 3339)
- Color coding: red when under target, green when met
- Configurable hours target via `HOURS_TARGET` config (default: 40)

### Drag-to-Create Behavior
- When dragging an issue onto the calendar (same for mobile tap-to-create):
  - If the project has no configured sub-tasks: create entry immediately (30-min default)
  - If the project has billable or non-billable sub-tasks: open the edit dialog so a sub-task can be chosen
- Resizing or moving updates the entry in place (never changes the sub-task association)

### Billable & Non-billable Sub-tasks
- Two mirror-image modes reclassify logged time by routing the worklog to a sub-task. Both are stored in the `jirametadata` project property and managed by the companion `../jirametadata` Forge app:
  - **Billable sub-tasks** (`value.billable_subtasks`): configured on a **non-billable** project; time becomes billable
  - **Non-billable sub-tasks** (`value.non_billable_subtasks`, or the hyphen-stripped `nonbillable_subtasks` alias): configured on a **billable** project; time becomes non-billable
- A project uses one mode or the other, never both; if both fields are set, the project's `value.billable` flag decides (billable → non-billable sub-tasks, non-billable → billable sub-tasks). Resolved in `GetProjectSubtaskConfig` (jira.go), which returns the type IDs plus a `Mode` (`SubtaskModeBillable`/`SubtaskModeNonBillable`). The ID lists decode tolerantly (`flexStrings`: list, single string, "", null or numbers), like `billable` (`flexBool`)
- Why a configured type isn't offered is logged once per project/type (`warnOnce`): not a sub-task type in the project's scheme, not an issue type the user can see, or a jirametadata field with "subtask" in its key that isn't one JiraTime reads; opening a non-configured sub-task in a configured project logs at info
- The two modes share one code path; the only user-facing difference is the dialog label (`subtask_mode` in the API response drives "Billable via Sub-task" vs "Non-billable via Sub-task")
- `GET /api/issues/{key}/subtask-options` returns the checkbox choices (`subtask_types`) and `subtask_mode` for the edit dialog; a configured sub-task resolves to its parent with `current_type_id`/`current_subtask` pre-set
- When configured, drag/tap-to-create opens the edit dialog instead of logging immediately; the dialog shows one checkbox per type (radio-behaving, deselectable) plus a nested list of the parent's existing sub-tasks of the selected type
- Type selected without a specific sub-task → get-or-create a sub-task named after the type (case-insensitive summary match); no type selected → worklog goes on the parent
- Worklog target resolution and validation happen server-side (`findWorklogTarget` in handlers.go); finding the target and creating a missing sub-task (`createIfNeeded`) are separate so SDM changes can be checked before anything is created
- Changing the association on an existing entry moves the worklog. On your own calendar: create on target first, then delete the original, rolling back the copy (with retries) if the delete fails (`moveWorklog`); moved worklogs are marked JiraTime-created. Jira's worklog move API is not used anywhere (see SDM Editing)
- Calendar events on configured sub-tasks are titled `[PARENT-KEY] Parent summary • Type Name` and carry `parent_key`/`subtask_type_id`/`subtask_type_name`
- Project property and issue-type lookups are cached site-wide (15 min TTL); the Refresh button also invalidates these via `cache.InvalidateSite`

### Worklog Source Tracking
- Worklogs created via JiraTime are marked with a `jiratime.source` property
- Calendar displays visual indicator: solid left border (JiraTime) vs dashed left border (external)
- Source is checked via worklog properties API when fetching events
- `CalendarEvent.FromJiraTime` boolean field indicates the source

### Manager Impersonation
- Three roles can view other users' calendars:
  - **Super users** (`SUPER_USERS` config, by email) are site administrators; they can impersonate, edit like SDMs, and are the only ones who can manage Managers/SDMs. `IsSuperUser(session)` matches the session's `Email` only when `EmailVerified`; both come from Atlassian's identity endpoint (`GET https://api.atlassian.com/me`, `read:me` scope, `getAtlassianIdentity`) at sign-in, and sessions saved without a verified email look it up when restored (`refreshIdentity`). `CanImpersonate`/`CanEditOthers` take the session; Manager/SDM roles stay keyed by account ID
  - **Managers** are administered in-app by super users (persisted in `managers.json` with `role: "manager"`, loaded into memory at startup) and view other calendars read-only
  - **SDMs** (Service Delivery Managers) are on the same list with `role: "sdm"` and can also change the time they view (see SDM Editing); entries without a role load as Managers, and re-adding someone without a role keeps theirs
- `CanImpersonate` (super users, Managers, SDMs) gates viewing; `CanEditOthers` (super users, SDMs) is checked on every write, so a role change applies on the next request
- View-only mode (Managers): all create/update/delete operations are blocked
- Impersonation state stored in session (`ImpersonatingID`, `ImpersonatingName`)
- Removing a manager revokes any in-progress impersonation on their next request (checked in `requireAuth`)
- JQL queries use the impersonated user's account ID instead of `currentUser()`
- Jira filters results by the caller's permissions, so reads scoped to the viewed user's own work (calendar, hours, Active Issues) use the viewed user's saved token (`viewClientFor` in auth.go): managers see exactly what that user sees, including projects the manager can't open in Jira (a deliberate choice). Search, sub-task option lookups and writes keep the viewer's own token
- If the viewed user has no usable token (never signed in to JiraTime, or their sign-in expired), the viewer's token is used instead and `GET /api/events` sets `X-View-Limited: 1`, which shows a warning under the "Viewing as" bar; limited Active Issues results are not cached
- Impersonation start/stop is logged (viewer and viewed account IDs)
- API endpoints:
  - `GET /api/users/search?q=X` - Search users (super users and managers)
  - `POST /api/impersonate` - Start impersonating `{account_id, display_name}` (super users and managers)
  - `POST /api/impersonate/stop` - Stop impersonating
  - `GET /api/managers` - List managers (super users only)
  - `POST /api/managers` - Add/update a manager `{account_id, display_name, avatar_url, role}` (super users only)
  - `DELETE /api/managers/{account_id}` - Remove a manager (super users only)
- Frontend shows yellow banner with "Viewing as: [Name]" and Stop button
- Manager administration lives in the settings dialog (gear icon), visible only to super users
- `is_super_user`, `is_manager`, `is_sdm`, `can_impersonate` and `can_edit_others` flags returned in `/api/user` response; while impersonating it also returns `impersonating_signed_in` (whether time can be created for them)

### SDM Editing
- Writes go through `worklogWriter` (sdm.go): on your own calendar everything uses your own token; for an SDM changing someone else's calendar:
  - Edits and deletes use the SDM's own token (`jiraClientFor`), so Jira keeps the author and records the SDM as `updateAuthor`; SDMs need Edit All Worklogs and Delete All Worklogs. Moves create the copy with the viewed user's token and delete the original with the SDM's
  - New worklogs (create, Shift+drag copy, split remainder) use the viewed user's token (`authorClient`), because Jira always makes the caller the author; they are then re-saved unchanged with the SDM's token (`stampCreated`) so Jira shows the SDM as updater. No usable token for the viewed user → 409 with a "needs to sign in" message and nothing written (never fall back to creating as the SDM)
- Every SDM change first fetches the worklog (`loadWorklog`) and refuses worklogs not authored by the viewed user
- Multi-step changes check Jira permissions first (`requirePermissions`, `GET /mypermissions`): moves need the SDM's Delete All Worklogs (+ Edit All Worklogs if the time changes) and Work On Issues on the target; splits need the SDM's Edit All Worklogs and the viewed user's Work On Issues on the target
- Moves (`worklogWriter.move`): `moveWorklog` with the viewed user's token for the copy (then `stampCreated`) and the SDM's for the delete, rolling the copy back if the delete fails. Jira's `/worklog/updated` and `/worklog/deleted` feeds (which JiraReports syncs from), time tracking and history all see an ordinary create and delete. Moves therefore need the viewed user's sign-in, like creates; without it the dialog hides Move/Split and disables the sub-task boxes
- Do not use Jira's worklog move API (`POST /issue/{key}/worklog/move`): tested 2026-10-19, it answered 204 but gave the worklog a new ID (241135 → 241143), and JiraReports kept counting the old ID on the source issue (it apparently never reached `/worklog/deleted`) until its reconcile pass; Atlassian's runbook also says it skips time tracking and issue history
- Split (`POST /api/events/{id}/split`, `handleSplitEvent`; also works on your own calendar): create the remainder (viewed user's token, comment copied as stored ADF) at original start + kept duration, then shorten the original (SDM token, start and comment written back unchanged), deleting the remainder if that fails
- A change that is partly applied and can't be undone returns a `partialError` describing what to fix in Jira
- Audit: `writeAudit` (audit.go) appends one JSON line per SDM change to `audit.log` (actor, on-behalf-of, IP, action, outcome ok/partial/denied, issue, worklog, target, before/after, prior_period) and echoes it to logrus; own-calendar changes are not audited
- Each SDM-changed worklog gets a `jiratime.sdm` property (`SDMChange`); calendar events carry `sdm_changed_by`/`sdm_changed_at` and are marked (amber underline, tooltip)
- Move-to picker (edit dialog, SDM mode only): `GET /api/hierarchy?project=KEY` (epics, found by issue type `hierarchyLevel == 1` from `/project/{key}`), `?project=KEY&no_epic=1` (issues with no parent), `?parent=KEY` (an epic's children via `parent = KEY`, any project); issues carry their sub-tasks, each level is capped at 500 most recently updated (open first) and cached per viewer (`cache.GetHierarchy`, cleared by Refresh and when an SDM change creates a sub-task). Picking an issue reloads its sub-task options and the existing dialog save (`parent_key`/`subtask_*`) does the move
- The UI asks SDMs to confirm changes to entries before the current month (may already be invoiced), and shows a line describing what Save/Split will do

### Time Range Settings
- Configurable visible time range for calendar (supports different shifts/timezones)
- Presets: Day (6am-10pm), Early (4am-2pm), Late (2pm-12am), Night (8pm-8am), Full Day
- Custom option allows arbitrary start/end times
- Overnight ranges supported (when end < start, end is extended past 24:00 for FullCalendar)
- Stored in localStorage (`timeRangeSettings` key)
- Applied dynamically via `calendar.setOption()` without page reload (`applyTimeRange` in app.js)
- Auto-expands to include entries in the current view that fall outside the configured hours (FullCalendar does not render events entirely outside `slotMinTime`/`slotMaxTime`); resets when they are no longer in view
- Gear icon (⚙) in sidebar footer opens settings dialog

## Design Decisions

- **Single Jira instance** - no multi-tenant support required
- **Issues as primary entity** - users think in terms of issues, not tasks/subtasks
