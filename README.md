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
- **Custom Field Tracking**: Track time in custom numeric fields (e.g., Billable Time) with automatic totals
- **Source Tracking**: Visual indicator distinguishes JiraTime entries from those created in Jira/JSM
- **Manager Impersonation**: Super users can view team members' calendars in read-only mode
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
   DONE_ISSUES_WEEKS = 2     # How long Done issues stay visible
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
make dev
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
3. A 30-minute entry is created automatically

**By Selecting Time:**
1. Click and drag on the calendar to select a time range
2. (Note: You'll need to drag an issue to create the entry)

### Editing Time Entries

- **Move**: Drag an event to a new time
- **Resize**: Drag the bottom edge of an event to change duration
- **Copy**: Hold Shift while dragging to duplicate
- **Edit Details**: Click an event to open the edit dialog
- **Delete**: Click an event, then click "Delete" in the dialog

### Keyboard Shortcuts

- **Shift+Drag**: Copy an event instead of moving it

### Mobile Usage

On mobile devices (screen width < 768px):

**Creating entries:**
1. Tap the hamburger menu (☰) to open the sidebar
2. Tap an issue to select it (it will highlight blue)
3. The sidebar auto-closes and a toast message appears
4. Tap on the calendar at the desired time to create a 30-minute entry
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
- Recently completed issues (Done status within the configured period, default 2 weeks) - so you can still log time on recently finished work

These time periods are configurable via `ACTIVE_ISSUES_WEEKS` and `DONE_ISSUES_WEEKS` in your config.

**Search Results** may include some issues also shown in Active Issues.

### Custom Field Tracking

JiraTime can track worklog time in custom numeric fields on Jira issues. This is useful for tracking categories like billable time or specific work types.

**Supported Custom Fields:**
| Field ID | Label |
|----------|-------|
| `customfield_11710` | Billable Time |
| `customfield_11712` | Smart Hands and Eyes |
| `customfield_12073` | Smart Hands and Eyes (After Hours) |

**How it works:**
1. When editing a time entry, checkboxes appear for any custom fields available on that issue
2. Check a field to add the worklog's duration to that field's running total
3. The current total (in hours) is shown next to each checkbox
4. Unchecking a field removes the worklog's contribution from the total
5. Deleting a worklog automatically removes its contributions from all custom fields

**Notes:**
- Custom fields must already exist on the Jira issue to appear as options
- Contributions are tracked per-worklog using Jira's worklog properties API
- If a worklog's duration changes (including via drag-resize), the delta is automatically applied to checked custom fields

### Source Tracking

Calendar events show a visual indicator to distinguish their origin:
- **Solid left border**: Created via JiraTime
- **Dashed left border**: Created externally (Jira, JSM, or other tools)

This helps identify which entries were logged through JiraTime vs. native Jira interfaces.

### Manager Impersonation

Super users (managers) can view team members' calendars in read-only mode to review time entries without logging in as that user.

**Setup:**
1. Add account IDs to the `SUPER_USERS` list in `config.toml`:
   ```toml
   SUPER_USERS = ["5a1234567890abcdef123456", "5b1234567890abcdef123456"]
   ```
2. To find your account ID, check the `/api/user` endpoint while logged in

**Usage:**
1. Super users see a "Search users to impersonate..." field below their name
2. Search for a team member by name
3. Click their name to view their calendar
4. A yellow banner shows "Viewing as: [Name]" with a Stop button
5. All modifications (create, edit, delete) are blocked while impersonating
6. Click "Stop" to return to your own calendar

### Hours Widget

The hours widget shows your logged hours for the current week:
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
| `DONE_ISSUES_WEEKS` | No | 2 | Weeks that Done issues remain visible |
| `SUPER_USERS` | No | [] | List of account IDs that can impersonate other users |

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
| POST | `/api/events` | Create a worklog |
| PUT | `/api/events/{id}` | Update a worklog |
| DELETE | `/api/events/{id}` | Delete a worklog |
| GET | `/api/events/{id}/contributions` | Get worklog's custom field contributions |
| GET | `/api/issues` | Get issues assigned to current user |
| GET | `/api/issues/search?q=X` | Search all Jira issues by text |
| GET | `/api/issues/{key}/custom-fields` | Get available custom fields for an issue |
| GET | `/api/hours?week=X` | Get weekly hours summary |
| POST | `/api/refresh` | Force cache refresh |
| GET | `/api/user` | Get current user info |
| GET | `/api/users/search?q=X` | Search users (super users only) |
| POST | `/api/impersonate` | Start impersonating a user (super users only) |
| POST | `/api/impersonate/stop` | Stop impersonating |

## Troubleshooting

### "No accessible Jira sites found"
- Ensure your Atlassian account has access to at least one Jira Cloud site
- Check that you granted the app access during OAuth authorization

### "Authentication failed"
- Verify your `JIRA_CLIENT_ID` and `JIRA_CLIENT_SECRET` are correct
- Check that the callback URL in Atlassian matches your `BASE_URL`

### Events not loading
- Click the "Refresh" button to clear the cache
- Check browser console for API errors
- Ensure you have worklogs in the visible date range

### Session expires frequently
- The app automatically refreshes tokens and persists sessions across restarts
- Sessions are stored in `sessions.json` and tokens in `tokens.json`
- Logout/login will restore your session without requiring OAuth re-authorization
- If issues persist, delete `sessions.json` and `tokens.json` and re-authenticate

## License

MIT
