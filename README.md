# JiraTime

A calendar-based time tracking application for Jira Cloud. Log work directly from a visual calendar interface with drag-and-drop support.

## Features

- **Calendar View**: Week and day views with 30-minute increments
- **Drag & Drop**: Drag issues from the sidebar onto the calendar to create time entries
- **Resize Events**: Drag event edges to adjust duration
- **Move Events**: Drag events to reschedule
- **Copy Events**: Shift+drag to duplicate an entry
- **Edit Dialog**: Click any event to edit details or delete
- **Hours Tracking**: Visual widget showing weekly hours vs. target
- **Search**: Filter sidebar issues and search all Jira issues
- **Source Tracking**: Visual indicator distinguishes JiraTime entries from those created in Jira/JSM
- **Billable / Non-billable Sub-tasks**: Reclassify logged time against configured sub-task types (via the JiraMetadata app's `billable_subtasks` / `non_billable_subtasks` project properties)
- **Manager Impersonation**: Managers (administered in-app by super users) can view team members' calendars in read-only mode
- **SDM Editing**: Service Delivery Managers can correct team members' time while viewing it (edit, move to another issue or sub-task, split, add), with the team member kept as the worklog author
- **Configurable Time Range**: Adjust visible hours for different shifts (day, night, custom)
- **Recent Issues**: Quick access to your 5 most recently used issues
- **External Links**: Click the link icon on any issue to open it in Jira
- **Mobile Responsive**: Collapsible sidebar, day view, and tap-to-create workflow

## Prerequisites

- Go 1.22 or later
- A Jira Cloud instance (works with both Jira and Jira Service Management)
- An Atlassian developer account to create OAuth credentials

## Setup

### 1. Create an Atlassian OAuth App

1. Go to the [Atlassian Developer Console](https://developer.atlassian.com/console/myapps/)
2. Click **Create** → **OAuth 2.0 integration**
3. Give your app a name (e.g., "JiraTime") and click **Create**
4. In the left sidebar, click **Permissions**
5. Add the following permissions:

   **User Identity API:**
   - `read:me`
   - `read:account`

   **Jira API:**
   - `read:jira-user`
   - `read:jira-work`
   - `write:jira-work`

   **Jira Service Management API** (if using JSM):
   - `read:servicedesk-request`
   - `write:servicedesk-request`

6. In the left sidebar, click **Authorization**
7. Next to "OAuth 2.0 (3LO)", click **Configure**
8. Set the **Callback URL** to: `http://localhost:8080/oauth/callback`
   - If deploying to a different URL, use that instead
9. Click **Save changes**
10. In the left sidebar, click **Settings**
11. Copy the **Client ID** and **Secret** (you'll need these for configuration)

### 2. Configure JiraTime

1. Copy the example configuration file:
   ```bash
   cp empty_config.toml config.toml
   ```

2. Edit `config.toml` and fill in your values:
   ```toml
   JIRA_CLIENT_ID = "your-client-id-here"
   JIRA_CLIENT_SECRET = "your-client-secret-here"
   BASE_URL = "http://localhost:8080"
   SESSION_SECRET = "generate-a-random-string-at-least-32-characters"
   PORT = 8080
   HOURS_TARGET = 40
   ACTIVE_ISSUES_WEEKS = 4   # How far back to look for activity
   DONE_ISSUES_DAYS = 90     # How long Done issues can still have time logged
   ```

3. Generate a session secret (at least 32 characters):
   ```bash
   openssl rand -base64 32
   ```

### 3. Build and Run

```bash
# Install dependencies and build
make build

# Or run directly in development mode
make run
```

The server will start on the configured port (default: 8080).

### 4. Production Deployment with HTTPS

For production, JiraTime supports automatic HTTPS via Let's Encrypt:

1. Set `PORT = 443` in your `config.toml`
2. Set `BASE_URL` to your domain (e.g., `https://jiratime.example.com`)
3. Ensure ports 80 and 443 are open (port 80 is needed for ACME challenges)
4. Run the server - certificates are automatically obtained and renewed

```toml
PORT = 443
BASE_URL = "https://jiratime.example.com"
```

Certificates are cached in the `certs/` directory.

### 5. Remote Server Deployment

JiraTime includes a `make deploy` command for deploying to a remote Linux server using Supervisor for process management.

**Setup:**

1. Copy the deployment config example:
   ```bash
   cp __config.sh.example __config.sh
   ```

2. Edit `__config.sh` with your server details:
   ```bash
   USER=youruser
   GROUP=yourgroup
   SERVER=yourserver.com
   ```

3. Create a production config:
   ```bash
   cp config.toml config.prod.toml
   # Edit config.prod.toml with production values (BASE_URL, PORT=443, etc.)
   ```

4. Cross-compile and deploy:
   ```bash
   make compile
   make deploy
   ```

**What `make deploy` does:**
- Copies the Linux binary to `/home/$USER/jiratime/`
- Copies and configures `supervisor.conf` for the target server
- Copies `config.prod.toml` as `config.toml`
- Installs the Supervisor config and restarts the service

**Server Requirements:**
- SSH access with key authentication
- Supervisor installed (`apt install supervisor`)
- Ports 80 and 443 open (if using HTTPS)

## Usage

### First Login

1. Open your browser to `http://localhost:8080`
2. You'll be redirected to Atlassian to authorize the app
3. Grant the requested permissions
4. You'll be redirected back to the calendar

### Creating Time Entries

**From the Sidebar:**
1. Find an issue in "Active Issues" or "Recent Issues"
2. Drag the issue onto the calendar at the desired time
3. The edit dialog opens for a 30-minute entry, with the cursor in **Description**
4. Describe the work, adjust the start or duration if needed, and click **Save**

Every entry needs a description. Jira can't make worklog comments mandatory, so JiraTime does: Save won't go ahead until there is one. Entries logged without one (older entries, or time logged in Jira) can still be dragged and resized, but saving them from the dialog needs a description.

The description is the entry's **whole** worklog comment, including comments written in Jira: every paragraph, plus mentions, links (shown as `text (address)`), lists, tables and code as plain text. Images and files show as `[attachment]`. Dragging and resizing never change the comment, and saving from the dialog without editing the description keeps it exactly as Jira stores it. If a comment has formatting from Jira (links, mentions, lists, images, ...), the dialog says so: changing the text there saves the comment as plain text.

If the issue's project has billable or non-billable sub-tasks configured (see [Billable & Non-billable Sub-tasks](#billable--non-billable-sub-tasks)), the dialog also lets you choose where the time goes.

**By Selecting Time:**
1. Click and drag on the calendar to select a time range
2. (Note: You'll need to drag an issue to create the entry)

### Editing Time Entries

- **Move**: Drag an event to a new time
- **Resize**: Drag the bottom edge of an event to change duration
- **Copy**: Hold Shift while dragging to duplicate (copying an entry that has no description opens the dialog so you can add one)
- **Edit Details**: Click an event to open the edit dialog
- **Delete**: Click an event, then click "Delete" in the dialog

Time logged on a project that has since been archived still shows on the calendar and counts toward the hours total. Jira doesn't allow changes to archived projects, so these entries are faded and striped, can't be moved, resized or copied, and open in a view-only dialog. Restore the project in Jira to change them.

### Keyboard Shortcuts

- **Shift+Drag**: Copy an event instead of moving it

### Mobile Usage

On mobile devices (screen width < 768px):

**Creating entries:**
1. Tap the hamburger menu (☰) to open the sidebar
2. Tap an issue to select it (it will highlight blue)
3. The sidebar auto-closes and a toast message appears
4. Tap on the calendar at the desired time, then add a description to the new 30-minute entry and tap Save
5. Tap a selected issue again to deselect it

**Editing entries:**
- **Single tap** on an event = select it (enables drag/resize handles)
- **Double tap** on an event = open the edit dialog
- After selecting, press and hold briefly (~100ms) to drag or resize
- Edit dialog allows precise control of start time and duration

### Search

Type in the search box to:
- Filter your active issues in the sidebar
- **Search all Jira issues** (2+ characters) - results appear in a "Search Results" section, allowing you to log time on any issue

Search supports partial matching - typing "test" will match "testing", "testify", etc. This also works for issue keys: typing "ABC" matches ABC-1, ABC-123, etc.

### Issue Filtering Criteria

**Active Issues** shows issues where you are:
- Assigned to the issue
- The reporter (created the issue)
- Have previously logged time on the issue

Additionally, Active Issues are filtered to only show issues with recent activity (updated within the last 4 weeks by default). This prevents old issues you created years ago from cluttering the list.

**Both sections** include:
- Open issues (any status except Done)
- Recently completed issues (Done within the configured period, default 90 days) - so you can still log time on recently finished work. In Active Issues, Done issues also need activity within the 4-week window, so they drop off the sidebar sooner but stay searchable for the full period.

**Both sections** exclude Epics (and anything above Epic in the issue hierarchy), since time is not logged against them. Time already logged on an Epic still shows on the calendar.

These time periods are configurable via `ACTIVE_ISSUES_WEEKS` and `DONE_ISSUES_DAYS` in your config.

**Search Results** may include some issues also shown in Active Issues.

### Source Tracking

Calendar events show a visual indicator to distinguish their origin:
- **Solid left border**: Created via JiraTime
- **Dashed left border**: Created externally (Jira, JSM, or other tools)

This helps identify which entries were logged through JiraTime vs. native Jira interfaces.

### Billable & Non-billable Sub-tasks

Projects can define sub-task types that **reclassify** logged time, using the companion [JiraMetadata](../jirametadata) Forge app, which stores them in the `jirametadata` project property. There are two mirror-image modes:

| Mode | Property field | Configured on | Effect of logging time to the sub-task |
|------|----------------|---------------|----------------------------------------|
| **Billable sub-tasks** | `billable_subtasks` | a **non-billable** project | time is reclassified as **billable** |
| **Non-billable sub-tasks** | `non_billable_subtasks` | a **billable** project | time is reclassified as **non-billable** |

```
GET /rest/api/3/project/{key}/properties/jirametadata
{"key":"jirametadata","value":{"billable":false,"billable_subtasks":["11457","11490"]}}
{"key":"jirametadata","value":{"billable":true,"non_billable_subtasks":["11492","11493"]}}
```

A project is billable *or* non-billable, so it uses one mode or the other — never both. The values are sub-task issue type IDs. When a project has either field configured, the behaviour is identical apart from the dialog label ("Billable via Sub-task" vs "Non-billable via Sub-task"):

- **Creating an entry** (drag or tap) shows a checkbox per configured sub-task type in the edit dialog. Only one type can be selected at a time, and a checkbox can be unticked again.
- Selecting a type reveals the parent issue's **existing sub-tasks of that type** (all statuses, shown with a status badge). Ticking one logs the time against that specific sub-task's worklog.
- If a type is selected but no specific sub-task is ticked, the time goes to a sub-task **named after the type** (e.g. a "Non-billable Work" sub-task) — reused if it already exists under the parent, created if not.
- If no type is selected, the time is logged against the parent issue as before.
- Calendar events whose worklog lives on a configured sub-task are titled with the **parent issue plus the type** (e.g. `[PROJ-12] Fix login • Non-billable Work`), and reopening them shows the association pre-checked — including time logged directly in Jira/JSM on such a sub-task.
- Changing the checkboxes on an existing entry **moves** the worklog (to the parent when unticked, or to the selected sub-task). On your own calendar this is done as create-then-delete (Jira's worklog move API isn't used, because reporting that syncs from Jira's worklog change feeds doesn't see its moves): the copy is created first and rolled back if the delete fails, so time is never lost or double-counted. A moved entry counts as JiraTime-created (solid border), even if it was originally logged in Jira. SDMs' moves work the same way, using two sign-ins; see [SDM Editing](#sdm-editing).
- Dragging a configured sub-task itself from the sidebar resolves it to its parent, with the sub-task pre-checked in the dialog.

Projects with neither field show no checkboxes in the dialog. The property and issue-type lookups are cached for 15 minutes; use the sidebar **Refresh** button to pick up configuration changes immediately.

#### Setting up a project's sub-task types (order matters)

The three steps must happen in this order — skipping or reordering the first is the most common reason the checkboxes never appear:

1. **Add the sub-task work item types to the project.** Every type you intend to use (e.g. Pre-sales, Shadowing, Warranty) must exist in the project's Work Item Type scheme. JiraTime only offers types that are actually **creatable in that project**, so a type that isn't in the scheme is silently ignored — even when it's configured in JiraMetadata.
   - **Company-managed projects:** Project settings → **Work items → Types** → add the sub-task types to the scheme.
   - **Team-managed projects:** Project settings → **Work types** → add the sub-task types.
2. **Configure the types in JiraMetadata.** In the project's **Project Metadata** panel, add those types to **Billable Sub-tasks** (on a non-billable project) or **Non-Billable Sub-tasks** (on a billable project). This writes them to the `jirametadata` project property.
   - The field is a **Sub-Task Types** field. JiraMetadata derives the property key from the field label (lowercased, spaces → underscores, other punctuation dropped), so label it **"Non Billable Subtasks"** to get the key `non_billable_subtasks`. A hyphen is *stripped*, so "Non-Billable Subtasks" becomes `nonbillable_subtasks` — JiraTime accepts that spelling as a fallback, but the canonical key is `non_billable_subtasks`. If both fields happen to be set on one project, the project's `billable` flag decides which one applies.
3. **Reload in JiraTime.** The type checkboxes then appear in the create/edit dialog. Site config (the project property and issue-type scheme) is cached for 15 minutes, so use the sidebar **Refresh** button to pick up steps 1–2 immediately.

> **Gotcha:** if a type you configured in JiraMetadata doesn't show up in the dialog, it's almost always because it was never added to the project's Work Item Type scheme (step 1). Configuring a type in JiraMetadata does **not** add it to the project.

### Manager Impersonation

Managers, SDMs and super users can view team members' calendars to review time entries without logging in as that user.

There are three roles:
- **Super users** are site administrators listed by sign-in email in `config.toml` (`SUPER_USERS`). They can impersonate other users, change their time like an SDM, and are the only ones who can manage the Manager list.
- **Managers** are administered inside the app by super users and can view other users' calendars read-only.
- **SDMs** (Service Delivery Managers) are on the same list with the SDM role and can also change the time they view (see [SDM Editing](#sdm-editing)).

The list is persisted in `managers.json` next to the binary; entries saved before roles existed are Managers.

**Setup:**
1. Add super users' Atlassian sign-in emails to the `SUPER_USERS` list in `config.toml`:
   ```toml
   SUPER_USERS = ["someone@aptum.com", "someone.else@aptum.com"]
   ```
   Matching ignores case. JiraTime reads each user's email from Atlassian's identity service when they sign in, and an email only counts once Atlassian reports it as verified. Entries that aren't email addresses (such as account IDs from older configs) are ignored, with a warning in the server log at startup.
2. As a super user, open Settings (gear icon ⚙ in the sidebar footer) and use the **Managers** section to add or remove people and choose each one's role (Manager or SDM). Changes take effect immediately — removing a manager also ends any impersonation they have in progress, and changing an SDM to Manager blocks their next change.

**Usage:**
1. Managers, SDMs and super users see a "Search users to impersonate..." field below their name
2. Search for a team member by name
3. Click their name to view their calendar
4. A banner shows "Viewing as: [Name]" (yellow, for Managers) or "Editing as: [Name]" (blue, for SDMs and super users) with a Stop button
5. For Managers, all modifications (create, edit, delete) are blocked

**What you see:** the calendar, hours total and Active Issues are loaded with the team member's own Jira access, so you see exactly what they see, including time on projects you can't open in Jira yourself. Issue search and everything else still uses your own access. This needs the team member to have signed in to JiraTime at least once (and within Atlassian's 90-day inactivity limit). If they haven't, JiraTime falls back to your own access and shows a warning under the "Viewing as" bar, because entries on projects you can't open will be missing. Viewing someone's calendar keeps their saved sign-in active. Starting and stopping impersonation is recorded in the server log.
6. Click "Stop" to return to your own calendar

### SDM Editing

SDMs (and super users) can correct a team member's time while viewing their calendar: drag, resize, edit and delete entries, move them to another issue or sub-task, split them, and add time the team member forgot to log. **The team member stays the author of every worklog**, so the time still counts as theirs everywhere.

**How changes are made in Jira:**
- **Edits and deletes** use the SDM's own Jira access. Jira keeps the worklog's author and records the SDM as the one who last updated it. SDMs therefore need the **Edit All Worklogs** and **Delete All Worklogs** project permissions in every project whose time they correct (check team-managed and JSM projects too, which have their own permissions).
- **New worklogs** (adding time, Shift+drag copies, the second half of a split) have to be created with the team member's own JiraTime sign-in, because Jira always makes the caller the author. JiraTime then re-saves the new worklog with the SDM's access so Jira also shows the SDM as its last updater. If the team member has never signed in to JiraTime, or their sign-in has expired, adding, splitting and moving time is blocked and says why (nothing is ever created under the SDM's name instead); editing and deleting still work. User search shows "Not using JiraTime" next to such people, and the banner warns while viewing them.
- **Moves** copy the worklog to the target with the team member's sign-in (re-saved with the SDM's, as above) and then delete the original with the SDM's access. Jira records an ordinary create and delete, so time tracking, issue history and reports that sync from Jira's worklog change feeds (such as JiraReports) all stay right; the worklog gets a new ID. Jira's worklog move API is deliberately not used: in testing it gave the worklog a new ID anyway and left JiraReports counting the time on the old issue until its reconcile pass, and Atlassian's runbook says it doesn't update time tracking or issue history.

**Safety checks:**
- An SDM can only change worklogs that belong to the person they are viewing.
- Before a move or split writes anything, JiraTime checks the Jira permissions it will need (the SDM's, and the team member's Work On Issues on the target), so it doesn't stop halfway.
- A split creates the new part first and then shortens the original; a move that also changes the time is undone if the move fails. If an undo also fails, the error says exactly what is logged twice and needs fixing in Jira.
- Changing an entry from a previous month asks for confirmation, since it may already have been invoiced.

**In the edit dialog** (SDM mode):
- **Move to** shows the entry's project as a tree: epics, their issues and each issue's sub-tasks (epics load as you expand them; issues without an epic are grouped separately). Search finds issues in any project, including Done issues of any age. Picking an issue shows its billable/non-billable sub-task options, and a line under the form spells out what Save will do (e.g. "Save moves this entry to [ABC-14] DNS cutover").
- **Split…** keeps the minutes you enter on the entry and logs the rest, starting where that part ends, on the issue chosen in the picker (which can be the same issue). The comment is copied to both parts.

**Audit trail:** every SDM change is appended to `audit.log` next to the binary (one JSON object per line: time, SDM, team member, IP address, action, issue and worklog, before and after values, and whether it was in a previous month), and echoed to the server log. Refused and half-finished changes are recorded too. Forward `audit.log` to your central logging so it is kept beyond this server. Each changed worklog also gets a `jiratime.sdm` property naming the SDM, and the team member's calendar marks those entries with an amber underline ("Changed by … (SDM)").

### Hours Widget

The hours widget shows your logged hours (to two decimal places, e.g. 0.25) for the Monday-to-Sunday week being viewed, including in Day view:
- **Red**: Under target hours
- **Green**: Target hours met or exceeded

Configure your weekly target in `config.toml` with `HOURS_TARGET`.

### Time Range Settings

Click the gear icon (⚙) in the sidebar footer to configure the visible time range on the calendar.

**Presets:**
- **Day Shift** (6am - 10pm) - default
- **Early Shift** (4am - 2pm)
- **Late Shift** (2pm - 12am)
- **Night Shift** (8pm - 8am) - overnight range
- **Full Day** (12am - 12am)
- **Custom** - specify your own start/end times

Overnight ranges (where end time is before start time) are fully supported. Settings are stored in your browser and persist across sessions.

If the week or day being viewed has time entries outside the configured range (e.g. time logged in Jira at 11pm), the calendar temporarily stretches its visible hours to include them, so no entry is ever hidden.

The Day/Week view you choose is also remembered in your browser and kept when the window is resized. Narrow (mobile) screens always use Day view and return to your chosen view when widened again.

## Configuration Reference

| Key | Required | Default | Description |
|-----|----------|---------|-------------|
| `JIRA_CLIENT_ID` | Yes | - | OAuth 2.0 Client ID from Atlassian |
| `JIRA_CLIENT_SECRET` | Yes | - | OAuth 2.0 Client Secret from Atlassian |
| `BASE_URL` | Yes | - | URL where JiraTime is hosted |
| `SESSION_SECRET` | Yes | - | Random string (32+ chars) for session signing |
| `PORT` | No | 8080 | HTTP server port (set to 443 for HTTPS with Let's Encrypt) |
| `HOURS_TARGET` | No | 40 | Weekly hours target for the widget |
| `ACTIVE_ISSUES_WEEKS` | No | 4 | Weeks of activity for Active Issues filter |
| `DONE_ISSUES_DAYS` | No | 90 | Days after completion that Done issues can still have time logged (replaces `DONE_ISSUES_WEEKS`) |
| `SUPER_USERS` | No | [] | Site administrators' Atlassian sign-in emails (case-insensitive, must be verified by Atlassian): can impersonate and edit other users' time, and manage Managers and SDMs in-app |

Environment variables can override config file values (e.g., `JIRA_CLIENT_ID=xxx ./jiratime`).

## Development

```bash
# Build binary
make build

# Cross-compile for Linux/macOS/Windows
make compile

# Run development server
make dev

# Clean build artifacts
make clean

# Install/update dependencies
make deps

# Deploy to remote server (see Deployment section)
make deploy
```

## Project Structure

```
jiratime/
├── main.go              # Entry point, routes, embedded static files
├── config.go            # Viper configuration loading
├── auth.go              # OAuth 2.0 flow and session management
├── jira.go              # Jira REST API client
├── handlers.go          # HTTP request handlers
├── sdm.go               # SDM editing: whose Jira access each change uses, checks, moves
├── audit.go             # Audit log of SDM changes
├── managers.go          # Manager/SDM list (managers.json)
├── types.go             # Data structures
├── cache.go             # In-memory caching
├── static/
│   ├── index.html       # Main page
│   ├── js/app.js        # Calendar and UI logic
│   └── css/style.css    # Styling
├── config.toml          # Your configuration (gitignored)
├── config.prod.toml     # Production configuration (gitignored)
├── empty_config.toml    # Configuration template
├── __config.sh          # Deployment variables (gitignored)
├── __config.sh.example  # Deployment variables template
├── supervisor.conf      # Supervisor process config
└── Makefile
```

## API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/` | Main calendar page |
| GET | `/login` | Initiate OAuth flow |
| GET | `/oauth/callback` | OAuth callback handler |
| GET | `/logout` | Clear session and logout |
| GET | `/api/events?start=X&end=Y` | Get worklogs for date range |
| POST | `/api/events` | Create a worklog (`description` is required) |
| PUT | `/api/events/{id}` | Update a worklog (moves it when the dialog picks another issue or sub-task). `description` is optional: left out, the comment is kept as stored; sent, it can't be blank |
| POST | `/api/events/{id}/split` | Split a worklog `{duration_min, parent_key, subtask_type_id?, subtask_key?}` |
| DELETE | `/api/events/{id}` | Delete a worklog |
| GET | `/api/issues` | Get issues assigned to current user |
| GET | `/api/issues/search?q=X` | Search all Jira issues by text |
| GET | `/api/hierarchy?project=KEY` | A project's epics for the Move-to tree (`&no_epic=1`: its issues without an epic) |
| GET | `/api/hierarchy?parent=KEY` | An epic's child issues with their sub-tasks |
| GET | `/api/hours?start=X&end=Y` | Get hours summary for a week (RFC 3339 bounds in the user's timezone) |
| POST | `/api/refresh` | Force cache refresh |
| GET | `/api/user` | Get current user info |
| GET | `/api/users/search?q=X` | Search users (super users and managers) |
| POST | `/api/impersonate` | Start impersonating a user (super users and managers) |
| POST | `/api/impersonate/stop` | Stop impersonating |
| GET | `/api/managers` | List managers (super users only) |
| POST | `/api/managers` | Add or update a manager `{account_id, display_name, avatar_url, role}` (role `manager` or `sdm`; super users only) |
| DELETE | `/api/managers/{account_id}` | Remove a manager (super users only) |

## Troubleshooting

### "No accessible Jira sites found"
- Ensure your Atlassian account has access to at least one Jira Cloud site
- Check that you granted the app access during OAuth authorization

### "Authentication failed"
- Verify your `JIRA_CLIENT_ID` and `JIRA_CLIENT_SECRET` are correct
- Check that the callback URL in Atlassian matches your `BASE_URL`

### Events not loading
- Click the "Refresh" button to clear the cache
- If loading fails, an error is shown rather than an incomplete calendar (Jira rate limits and brief outages are retried automatically first)
- Ensure you have worklogs in the visible date range

### Time on an archived project is missing
- Jira only returns archived projects you can still browse. If an admin removed access before archiving the project, its entries can't be shown; restoring access (or the project) brings them back.
- Issues archived one at a time (a Premium/Enterprise feature, separate from archiving a whole project) are not returned by Jira search, so their time can't be shown. Unarchive the issue to see it again.

### Billable/Non-billable sub-task types don't appear in the dialog
- The sub-task type must be added to the **project's** Work Item Type scheme first (Project settings → **Work items → Types**, or **Work types** for team-managed projects). Types configured in JiraMetadata but missing from the project's scheme aren't offered. See [Setting up a project's sub-task types](#setting-up-a-projects-sub-task-types-order-matters).
- **Check the server log.** JiraTime logs why a configured type isn't offered, once per project and type: `isn't a sub-task type in the project's work type scheme`, `isn't an issue type JiraTime can see`, a JiraMetadata field whose name it doesn't read (`looks like sub-task settings but … is ignored`), `is a "…" sub-task, which isn't one of project …'s configured sub-task types` (the open issue is itself a sub-task), or `Failed to resolve sub-task types for project …` (the property couldn't be read).
- After adding a type, click the sidebar **Refresh** button — the project property and issue-type scheme are cached for 15 minutes.
- Sub-tasks can't have sub-tasks of their own, so the section never appears when the open issue is itself a sub-task.

### "ERROR: You can not edit the issue as it is in a non-editable workflow state."
This error occurs when trying to log time against a Jira issue that is in a closed/terminal workflow state (e.g. Done, Closed, Resolved). There are two options:

**Option 1:** Ask a project lead to transition the issue back to an editable state, log the time, then close it again.

**Option 2:** Ask a Jira site admin to add the `jira.issue.editable = true` property to the relevant workflow status. Note that this makes the **entire issue** editable (not just worklogs) for anyone in that status — consider whether that trade-off is acceptable before making this change.

To add the property (requires Jira site admin):
- **Company-managed projects:** Settings gear → Work items → Workflows → Edit the workflow → click the status (e.g. "Done") → Add property → key: `jira.issue.editable`, value: `true` → Save and publish.
- **Team-managed projects:** Project Settings → Work types → select the work type → Edit workflow → click the status → Add property → key: `jira.issue.editable`, value: `true` → Save and publish.

### Session expires frequently
- The app automatically refreshes tokens and persists sessions across restarts
- Sessions are stored in `sessions.json` and tokens in `tokens.json` (one token per user, shared by all of their browsers and devices)
- Sign-in cookies are renewed on each use and last 90 days, matching Atlassian's refresh-token inactivity expiry
- Logout/login will restore your session without requiring OAuth re-authorization
- Users are only sent back through the Atlassian approval screen when Atlassian rejects the saved refresh token; network errors and Atlassian outages show a temporary error instead
- If issues persist, delete `sessions.json` and `tokens.json` and re-authenticate

## License

MIT
