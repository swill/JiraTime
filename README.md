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
- **Search**: Filter issues, search all Jira issues, and highlight matching calendar events
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
- **Long press** (hold ~500ms) on an event = open the edit dialog
- After selecting, press and hold briefly (~100ms) to drag or resize
- Edit dialog allows precise control of start time and duration

### Search

Type in the search box to:
- Filter your active issues in the sidebar
- Dim non-matching events on the calendar
- **Search all Jira issues** (2+ characters) - results appear in a "Search Results" section, allowing you to log time on any issue

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

### Hours Widget

The hours widget shows your logged hours for the current week:
- **Red**: Under target hours
- **Green**: Target hours met or exceeded

Configure your weekly target in `config.toml` with `HOURS_TARGET`.

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

Environment variables can override config file values (e.g., `JIRA_CLIENT_ID=xxx ./jiratime`).

## Development

```bash
# Build binary
make build

# Run development server
make dev

# Run tests
make test

# Run static analysis
make vet

# Clean build artifacts
make clean

# Install/update dependencies
make deps
```

## Project Structure

```
jiratime/
├── main.go           # Entry point, routes, embedded static files
├── config.go         # Viper configuration loading
├── auth.go           # OAuth 2.0 flow and session management
├── jira.go           # Jira REST API client
├── handlers.go       # HTTP request handlers
├── types.go          # Data structures
├── cache.go          # In-memory caching
├── static/
│   ├── index.html    # Main page
│   ├── js/app.js     # Calendar and UI logic
│   └── css/style.css # Styling
├── config.toml       # Your configuration (gitignored)
├── empty_config.toml # Configuration template
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
| GET | `/api/issues` | Get issues assigned to current user |
| GET | `/api/issues/search?q=X` | Search all Jira issues by text |
| GET | `/api/hours?week=X` | Get weekly hours summary |
| POST | `/api/refresh` | Force cache refresh |
| GET | `/api/user` | Get current user info |

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
