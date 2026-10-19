// JiraTime Application
(function() {
    'use strict';

    let calendar;
    let recentIssues = JSON.parse(localStorage.getItem('recentIssues') || '[]');
    const MAX_RECENT_ISSUES = 5;
    const initializedDraggables = new WeakSet();
    let selectedIssue = null; // For mobile tap-to-create
    let jiraSiteURL = ''; // Base URL for Jira site
    let currentUser = null; // Current user info including impersonation state

    // Time range presets (start, end in HH:MM format)
    const TIME_RANGE_PRESETS = {
        'default': { start: '06:00', end: '22:00', label: 'Day Shift (6am - 10pm)' },
        'early':   { start: '04:00', end: '14:00', label: 'Early Shift (4am - 2pm)' },
        'late':    { start: '14:00', end: '24:00', label: 'Late Shift (2pm - 12am)' },
        'night':   { start: '20:00', end: '08:00', label: 'Night Shift (8pm - 8am)' },
        'full':    { start: '00:00', end: '24:00', label: 'Full Day (12am - 12am)' }
    };

    // Get stored time range settings from localStorage
    function getTimeRangeSettings() {
        const stored = localStorage.getItem('timeRangeSettings');
        if (stored) {
            try {
                return JSON.parse(stored);
            } catch (e) {
                console.error('Error parsing time range settings:', e);
            }
        }
        return { preset: 'default', customStart: '06:00', customEnd: '22:00' };
    }

    // Save time range settings to localStorage
    function saveTimeRangeSettings(settings) {
        localStorage.setItem('timeRangeSettings', JSON.stringify(settings));
    }

    // Theme functions
    function getTheme() {
        return localStorage.getItem('theme') || 'light';
    }

    function saveTheme(theme) {
        localStorage.setItem('theme', theme);
    }

    function applyTheme(theme) {
        if (theme === 'dark') {
            document.documentElement.setAttribute('data-theme', 'dark');
        } else {
            document.documentElement.removeAttribute('data-theme');
        }
    }

    // Convert time range settings to FullCalendar format
    // Handles overnight ranges by extending end time past 24:00
    function getCalendarTimeRange() {
        const settings = getTimeRangeSettings();
        let start, end;

        if (settings.preset === 'custom') {
            start = settings.customStart;
            end = settings.customEnd;
        } else {
            const preset = TIME_RANGE_PRESETS[settings.preset] || TIME_RANGE_PRESETS['default'];
            start = preset.start;
            end = preset.end;
        }

        // Convert to FullCalendar format (HH:MM:SS)
        const startTime = start + ':00';
        let endTime = end + ':00';

        // Handle overnight ranges: if end time is less than or equal to start time,
        // it's an overnight range - add 24 hours to end time
        const startMinutes = timeToMinutes(start);
        const endMinutes = timeToMinutes(end);

        if (endMinutes <= startMinutes && end !== '24:00') {
            // Convert to extended time (e.g., 08:00 becomes 32:00 for 8am next day)
            const extendedHours = parseInt(end.split(':')[0], 10) + 24;
            const mins = end.split(':')[1];
            endTime = String(extendedHours).padStart(2, '0') + ':' + mins + ':00';
        }

        return { slotMinTime: startTime, slotMaxTime: endTime };
    }

    // Helper to convert HH:MM to minutes since midnight
    function timeToMinutes(time) {
        const [hours, mins] = time.split(':').map(Number);
        return hours * 60 + mins;
    }

    // Helper to convert minutes since midnight to FullCalendar's HH:MM:SS
    // (hours may exceed 24 for overnight ranges)
    function minutesToTime(minutes) {
        const hours = String(Math.floor(minutes / 60)).padStart(2, '0');
        const mins = String(minutes % 60).padStart(2, '0');
        return `${hours}:${mins}:00`;
    }

    // Time range currently applied to the calendar - can be wider than the
    // saved settings when entries fall outside them (see applyTimeRange)
    let appliedTimeRange = null;

    // Apply the saved time range, stretched to include any entries in the
    // current view that would otherwise fall entirely outside it. FullCalendar
    // does not draw those at all, so time logged at e.g. 11pm would be invisible.
    function applyTimeRange() {
        const configured = getCalendarTimeRange();
        const configuredMin = timeToMinutes(configured.slotMinTime);
        const configuredMax = timeToMinutes(configured.slotMaxTime);
        const overnight = configuredMax > 24 * 60;
        const view = calendar.view;
        let minM = configuredMin;
        let maxM = configuredMax;

        calendar.getEvents().forEach(event => {
            const start = event.start;
            const end = event.end || new Date(start.getTime() + 30 * 60000);
            if (end <= view.activeStart || start >= view.activeEnd) return;
            if (isInVisibleHours(start, end, view, configuredMin, configuredMax)) return;

            const startM = start.getHours() * 60 + start.getMinutes();
            minM = Math.min(minM, startM);
            if (!overnight) {
                // Entries running past midnight show up to the end of their first day
                const endM = startM + Math.round((end - start) / 60000);
                maxM = Math.max(maxM, Math.min(endM, 24 * 60));
            }
            // Overnight ranges only ever miss entries in their daytime gap;
            // starting the range earlier shows them in their own day's column
        });

        // Snap any expansion to the 30-minute slot grid
        if (minM < configuredMin) minM = Math.floor(minM / 30) * 30;
        if (maxM > configuredMax) maxM = Math.ceil(maxM / 30) * 30;

        const range = { slotMinTime: minutesToTime(minM), slotMaxTime: minutesToTime(maxM) };
        if (appliedTimeRange &&
            appliedTimeRange.slotMinTime === range.slotMinTime &&
            appliedTimeRange.slotMaxTime === range.slotMaxTime) {
            return;
        }

        appliedTimeRange = range;
        calendar.batchRendering(() => {
            calendar.setOption('slotMinTime', range.slotMinTime);
            calendar.setOption('slotMaxTime', range.slotMaxTime);
        });
    }

    // Whether an entry overlaps the visible hours of any day column in the view
    function isInVisibleHours(start, end, view, minM, maxM) {
        for (let day = new Date(view.currentStart); day < view.currentEnd; day.setDate(day.getDate() + 1)) {
            const columnStart = new Date(day.getFullYear(), day.getMonth(), day.getDate(), 0, minM);
            const columnEnd = new Date(day.getFullYear(), day.getMonth(), day.getDate(), 0, maxM);
            if (start < columnEnd && end > columnStart) return true;
        }
        return false;
    }

    // Calendar view (Day/Week) chosen on desktop, remembered across reloads.
    // Narrow (mobile) screens always use Day view without changing this.
    const CALENDAR_VIEWS = ['timeGridWeek', 'timeGridDay'];

    function getPreferredView() {
        const view = localStorage.getItem('calendarView');
        return CALENDAR_VIEWS.includes(view) ? view : 'timeGridWeek';
    }

    function savePreferredView(view) {
        if (CALENDAR_VIEWS.includes(view)) {
            localStorage.setItem('calendarView', view);
        }
    }

    // fetch wrapper for API calls. A 401 means the session is gone (e.g. the Jira
    // sign-in was revoked), so go back through login instead of leaving the page
    // showing stale data; the returned promise never settles as the page unloads.
    async function apiFetch(url, options) {
        const response = await fetch(url, options);
        if (response.status === 401) {
            window.location.href = '/login';
            return new Promise(() => {});
        }
        return response;
    }

    // Loading state management
    let apiLoadingCount = 0;
    let calendarLoading = false;

    function updateLoadingState() {
        const isLoading = apiLoadingCount > 0 || calendarLoading;
        const overlay = document.getElementById('loadingOverlay');
        if (isLoading) {
            overlay.classList.remove('hidden');
        } else {
            overlay.classList.add('hidden');
        }
    }

    function showLoading() {
        apiLoadingCount++;
        updateLoadingState();
    }

    function hideLoading() {
        apiLoadingCount--;
        if (apiLoadingCount < 0) apiLoadingCount = 0;
        updateLoadingState();
    }

    function setCalendarLoading(isLoading) {
        calendarLoading = isLoading;
        updateLoadingState();
    }

    // Initialize when DOM is ready
    document.addEventListener('DOMContentLoaded', init);

    async function init() {
        // Show loading immediately on page load
        showLoading();
        try {
            await loadUserInfo();
            await loadIssues();
            initCalendar();
            initSidebar();
            initSearch();
            initDialog();
            initSettings();
            checkMobileView();
            window.addEventListener('resize', checkMobileView);
        } finally {
            hideLoading();
        }
    }

    // Calendar Setup
    function initCalendar() {
        const calendarEl = document.getElementById('calendar');
        const timeRange = getCalendarTimeRange();
        appliedTimeRange = timeRange;

        calendar = new FullCalendar.Calendar(calendarEl, {
            initialView: isMobileView() ? 'timeGridDay' : getPreferredView(),
            headerToolbar: {
                left: 'prev,next today',
                center: 'title',
                right: 'timeGridWeek,timeGridDay'
            },
            slotDuration: '00:30:00',
            slotMinTime: timeRange.slotMinTime,
            slotMaxTime: timeRange.slotMaxTime,
            allDaySlot: false,
            editable: true,
            droppable: true,
            selectable: true,
            selectMirror: true,
            nowIndicator: true,
            // Mobile touch settings - shorter delay to start drag/resize
            eventLongPressDelay: 100,
            selectLongPressDelay: 100,
            weekends: true,
            firstDay: 1, // Monday
            height: '100%',
            expandRows: true, // Expand time slots to fill available height

            // Event sources
            events: fetchEvents,

            // Loading state callback - fires when calendar starts/stops loading events
            loading: function(isLoading) {
                setCalendarLoading(isLoading);
            },

            // Event handlers
            eventClick: handleEventClick,
            eventDrop: handleEventDrop,
            eventResize: handleEventResize,
            eventReceive: handleEventReceive, // Handle external drops
            select: handleSelect,
            dateClick: handleDateClick, // For mobile tap-to-create

            datesSet: function(info) {
                // Remember the view picked on desktop (mobile forces Day view)
                if (!isMobileView()) {
                    savePreferredView(info.view.type);
                }

                // Also fires when options like slotMinTime change, so only
                // reload the hours when the week actually changed
                if (formatLocalDate(getWeekRange(info.start).start) !== hoursWidgetWeek) {
                    updateHoursWidget(info.start);
                }

                applyTimeRange();
            },

            // Stretch the visible hours to fit entries once they are loaded
            eventsSet: function() {
                applyTimeRange();
            },

            // Event rendering
            eventContent: function(arg) {
                return {
                    html: `<div class="fc-event-title">${arg.event.title}</div>`
                };
            },

            eventDidMount: function(arg) {
                const notes = [];
                if (arg.event.extendedProps.sdmChangedBy) {
                    notes.push(sdmChangeNote(arg.event));
                }
                if (arg.event.extendedProps.projectArchived) {
                    notes.push(ARCHIVED_ENTRY_NOTE);
                }
                if (notes.length > 0) {
                    arg.el.title = notes.join('\n');
                }
            }
        });

        calendar.render();
    }

    // Fetch events from API
    async function fetchEvents(info, successCallback, failureCallback) {
        try {
            const start = info.startStr;
            const end = info.endStr;
            const response = await apiFetch(`/api/events?start=${encodeURIComponent(start)}&end=${encodeURIComponent(end)}`);

            if (!response.ok) {
                const text = await response.text();
                throw new Error(extractErrorMessage(text));
            }

            setViewLimited(response.headers.get('X-View-Limited') === '1');

            const events = await response.json();
            if (!events) {
                successCallback([]);
                return;
            }

            const calendarEvents = events.map(e => {
                const event = {
                    id: e.id,
                    title: e.title,
                    start: e.start,
                    end: e.end,
                    classNames: e.from_jiratime ? ['from-jiratime'] : ['from-external'],
                    extendedProps: {
                        issueKey: e.issue_key,
                        issueId: e.issue_id,
                        worklogId: e.worklog_id,
                        description: e.description,
                        fromJiraTime: e.from_jiratime,
                        parentKey: e.parent_key,
                        subtaskTypeId: e.subtask_type_id,
                        subtaskTypeName: e.subtask_type_name,
                        projectArchived: !!e.project_archived,
                        sdmChangedBy: e.sdm_changed_by || '',
                        sdmChangedAt: e.sdm_changed_at || ''
                    }
                };
                if (e.sdm_changed_by) {
                    // Visible to the user too, so time changed for them is never hidden
                    event.classNames.push('sdm-changed');
                }
                if (e.project_archived) {
                    // Jira makes archived projects read-only: no move, resize or copy
                    event.editable = false;
                    event.classNames.push('project-archived');
                }
                return event;
            });

            successCallback(calendarEvents);
        } catch (error) {
            console.error('Error fetching events:', error);
            // Make it obvious the calendar may be incomplete
            alert('Failed to load time entries: ' + error.message);
            failureCallback(error);
        }
    }

    // Event Handlers
    let lastTapTime = 0;
    let lastTapEventId = null;
    const DOUBLE_TAP_DELAY = 300; // ms

    function handleEventClick(info) {
        const event = info.event;

        // Only allow editing events that have a worklog ID (real events, not temp)
        if (!event.extendedProps || !event.extendedProps.worklogId) {
            return;
        }

        // On desktop, always open dialog immediately
        if (!isMobileView()) {
            openEditDialog(event);
            return;
        }

        // On mobile, check for double tap
        const now = Date.now();
        const eventId = event.id;

        if (lastTapEventId === eventId && (now - lastTapTime) < DOUBLE_TAP_DELAY) {
            // Double tap detected - open dialog
            lastTapTime = 0;
            lastTapEventId = null;
            openEditDialog(event);
        } else {
            // Single tap - let FullCalendar handle selection (show handles)
            lastTapTime = now;
            lastTapEventId = eventId;
        }
    }

    async function handleEventDrop(info) {
        const event = info.event;

        // Only handle events with worklog IDs (real events from server)
        if (!event.extendedProps || !event.extendedProps.worklogId) {
            info.revert();
            return;
        }

        // Check if Shift key was held (copy event)
        if (info.jsEvent && info.jsEvent.shiftKey) {
            // Revert the original event position
            info.revert();

            // Create a new event at the drop location
            await createEvent(
                event.extendedProps.issueKey,
                event.start,
                getDurationMinutes(event),
                event.extendedProps.description || ''
            );
            return;
        }

        if (!confirmPriorPeriod(info.oldEvent.start, event.start)) {
            info.revert();
            return;
        }

        // Update the event
        await updateEvent(event, info.revert);
    }

    async function handleEventResize(info) {
        const event = info.event;

        // Only handle events with worklog IDs
        if (!event.extendedProps || !event.extendedProps.worklogId) {
            info.revert();
            return;
        }

        if (!confirmPriorPeriod(event.start)) {
            info.revert();
            return;
        }

        await updateEvent(event, info.revert);
    }

    // Handle external drop from sidebar - this is called AFTER FullCalendar adds the temp event
    async function handleEventReceive(info) {
        const event = info.event;
        const issueKey = info.draggedEl.dataset.issueKey;
        const issueSummary = info.draggedEl.dataset.issueSummary;

        if (!issueKey) {
            event.remove();
            return;
        }

        // Remove the temporary event that FullCalendar added
        event.remove();

        await startCreateFlow(issueKey, issueSummary, event.start);
    }

    function handleSelect(info) {
        // If an issue is selected (tap-to-create), create the event
        if (selectedIssue) {
            const issueKey = selectedIssue.key;
            const issueSummary = selectedIssue.summary;

            // Clear selection
            clearSelectedIssue();

            startCreateFlow(issueKey, issueSummary, info.start);
        }

        calendar.unselect();
    }

    function handleDateClick(info) {
        // If an issue is selected (mobile tap-to-create), create the event
        if (selectedIssue) {
            const issueKey = selectedIssue.key;
            const issueSummary = selectedIssue.summary;

            // Clear selection
            clearSelectedIssue();

            startCreateFlow(issueKey, issueSummary, info.date);
        }
    }

    // Start the create flow for an issue dropped/tapped onto the calendar.
    // Projects with sub-tasks configured (billable or non-billable) open the
    // edit dialog so a sub-task can be chosen; other projects keep the fast
    // one-step logging.
    async function startCreateFlow(issueKey, issueSummary, start) {
        if (isViewOnlyMode()) {
            alert('Cannot create events while viewing another user\'s calendar');
            return;
        }
        if (!canAddTime()) {
            alert(noSignInMessage());
            return;
        }

        let options = null;
        showLoading();
        try {
            options = await fetchSubtaskOptions(issueKey);
        } catch (error) {
            console.error('Error loading sub-task options:', error);
        } finally {
            hideLoading();
        }

        if (!options || !options.subtask_types || options.subtask_types.length === 0) {
            // No sub-tasks configured - create immediately
            addToRecentIssues(issueKey, issueSummary);
            await createEvent(issueKey, start, 30, '');
            return;
        }

        // A configured sub-task resolves to its parent; track the parent as recent
        addToRecentIssues(options.issue_key, options.issue_summary);
        openCreateDialog(options, start);
    }

    async function fetchSubtaskOptions(issueKey) {
        const response = await apiFetch(`/api/issues/${encodeURIComponent(issueKey)}/subtask-options`);
        if (!response.ok) {
            throw new Error('Failed to fetch sub-task options');
        }
        return response.json();
    }

    // API Functions
    function extractErrorMessage(text) {
        const jsonMatch = text.match(/\{[\s\S]*\}/);
        if (jsonMatch) {
            try {
                const parsed = JSON.parse(jsonMatch[0]);
                if (parsed.errorMessages && parsed.errorMessages.length > 0) {
                    return parsed.errorMessages.join(' ');
                }
            } catch (e) { /* ignore parse errors */ }
        }
        return text.trim();
    }

    async function createEvent(issueKey, start, durationMin, description, subtaskTypeId, subtaskKey) {
        if (isViewOnlyMode()) {
            alert('Cannot create events while viewing another user\'s calendar');
            return;
        }
        if (!canAddTime()) {
            alert(noSignInMessage());
            return;
        }
        if (!confirmPriorPeriod(start)) {
            return;
        }
        showLoading();
        try {
            const body = {
                issue_key: issueKey,
                start: start.toISOString(),
                duration_min: durationMin,
                description: description
            };
            if (subtaskTypeId) body.subtask_type_id = subtaskTypeId;
            if (subtaskKey) body.subtask_key = subtaskKey;

            const response = await apiFetch('/api/events', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(body)
            });

            if (!response.ok) {
                const text = await response.text();
                console.error('Create event failed:', text);
                throw new Error(extractErrorMessage(text));
            }

            calendar.refetchEvents();
            updateHoursWidget(calendar ? calendar.view.activeStart : undefined);
        } catch (error) {
            console.error('Error creating event:', error);
            alert('Failed to create time entry: ' + error.message);
        } finally {
            hideLoading();
        }
    }

    async function updateEvent(event, revertFn) {
        if (isViewOnlyMode()) {
            if (revertFn) revertFn();
            alert('Cannot update events while viewing another user\'s calendar');
            return;
        }
        showLoading();
        try {
            const eventId = `${event.extendedProps.issueKey}-${event.extendedProps.worklogId}`;
            const response = await apiFetch(`/api/events/${encodeURIComponent(eventId)}`, {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    start: event.start.toISOString(),
                    duration_min: getDurationMinutes(event),
                    description: event.extendedProps.description || ''
                })
            });

            if (!response.ok) {
                const text = await response.text();
                console.error('Update event failed:', text);
                throw new Error(extractErrorMessage(text));
            }

            updateHoursWidget(calendar ? calendar.view.activeStart : undefined);
        } catch (error) {
            console.error('Error updating event:', error);
            alert('Failed to update time entry: ' + error.message);
            if (revertFn) revertFn();
            calendar.refetchEvents();
        } finally {
            hideLoading();
        }
    }

    async function deleteEvent(eventId) {
        if (isViewOnlyMode()) {
            alert('Cannot delete events while viewing another user\'s calendar');
            return;
        }
        showLoading();
        try {
            const response = await apiFetch(`/api/events/${encodeURIComponent(eventId)}`, {
                method: 'DELETE'
            });

            if (!response.ok) {
                const text = await response.text();
                console.error('Delete event failed:', text);
                throw new Error(extractErrorMessage(text));
            }

            calendar.refetchEvents();
            updateHoursWidget(calendar ? calendar.view.activeStart : undefined);
        } catch (error) {
            console.error('Error deleting event:', error);
            alert('Failed to delete time entry: ' + error.message);
        } finally {
            hideLoading();
        }
    }

    // Dialog Functions
    const ARCHIVED_ENTRY_NOTE = 'This project is archived in Jira, so this entry can\'t be changed.';

    // "Changed by <SDM> on <date>" for entries an SDM changed on the user's behalf
    function sdmChangeNote(event) {
        const props = event.extendedProps;
        const at = props.sdmChangedAt ? new Date(props.sdmChangedAt) : null;
        const when = at && !isNaN(at) ? ` on ${formatLocalDate(at)}` : '';
        return `Changed by ${props.sdmChangedBy} (SDM)${when}`;
    }
    let currentEditingEvent = null;
    let dialogReadOnly = false; // Open dialog only shows the entry (no save/delete)
    let dialogSubtaskOptions = null; // Sub-task options for the open dialog (null = not loaded)
    let selectedSubtaskTypeId = null;
    let selectedSubtaskKey = null;
    // Where the entry being edited is logged now, to tell when Save moves it:
    // { key, typeId, subtaskKey } in sub-task options terms
    let dialogOriginalTarget = null;
    let dialogEntry = null; // { start, durationMin, description } of the entry being edited
    let splitMode = false;

    function initDialog() {
        const dialog = document.getElementById('editDialog');
        const form = document.getElementById('editForm');
        const cancelBtn = document.getElementById('cancelBtn');
        const deleteBtn = document.getElementById('deleteBtn');

        initMovePicker();
        document.getElementById('splitBtn').addEventListener('click', () => setSplitMode(!splitMode));
        document.getElementById('splitDuration').addEventListener('input', updateActionSummary);

        // Handle dialog close (cancel, click outside, escape key)
        dialog.addEventListener('close', () => {
            // Remove focus/selection from calendar events
            if (currentEditingEvent && currentEditingEvent.el) {
                currentEditingEvent.el.blur();
                currentEditingEvent.el.classList.remove('fc-event-selected');
            }
            currentEditingEvent = null;
            splitMode = false;

            // Also blur any focused event elements
            document.querySelectorAll('.fc-event').forEach(el => {
                el.blur();
                el.classList.remove('fc-event-selected');
            });

            calendar.unselect();
        });

        cancelBtn.addEventListener('click', () => dialog.close());

        deleteBtn.addEventListener('click', async () => {
            const eventId = document.getElementById('eventId').value;
            if (confirm('Delete this time entry?') && confirmPriorPeriod(dialogEntry && dialogEntry.start)) {
                await deleteEvent(eventId);
                dialog.close();
            }
        });

        form.addEventListener('submit', async (e) => {
            e.preventDefault();

            // Enter in a field can still submit when Save is hidden
            if (dialogReadOnly) {
                dialog.close();
                return;
            }

            const eventId = document.getElementById('eventId').value;
            const start = new Date(document.getElementById('eventStart').value);
            const duration = parseInt(document.getElementById('eventDuration').value, 10);
            const description = document.getElementById('eventDescription').value;
            const issueKey = document.getElementById('eventIssueKey').value;
            const subtaskTypeId = selectedSubtaskTypeId;
            const subtaskKey = selectedSubtaskKey;
            const subtaskOptions = dialogSubtaskOptions;

            if (eventId && splitMode) {
                const keep = parseInt(document.getElementById('splitDuration').value, 10);
                if (!(keep > 0 && keep < dialogEntry.durationMin)) {
                    alert(`Keep fewer minutes than the entry's ${dialogEntry.durationMin} on it, so there is time left to split off.`);
                    return;
                }
                if (!confirmPriorPeriod(dialogEntry.start)) return;
                dialog.close();
                await splitEvent(eventId, keep, subtaskOptions ? subtaskOptions.issue_key : issueKey, subtaskTypeId, subtaskKey);
                return;
            }

            // Edits are confirmed here; new entries in createEvent
            if (eventId && !confirmPriorPeriod(dialogEntry && dialogEntry.start, start)) return;

            dialog.close();
            showLoading();

            try {
                if (eventId) {
                    // Update existing event
                    const body = {
                        start: start.toISOString(),
                        duration_min: duration,
                        description: description
                    };

                    // Send the parent key so the backend can move the worklog
                    // when the sub-task association changed
                    if (subtaskOptions) {
                        body.parent_key = subtaskOptions.issue_key;
                        if (subtaskTypeId) body.subtask_type_id = subtaskTypeId;
                        if (subtaskKey) body.subtask_key = subtaskKey;
                    }

                    const response = await apiFetch(`/api/events/${encodeURIComponent(eventId)}`, {
                        method: 'PUT',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify(body)
                    });

                    if (!response.ok) {
                        const text = await response.text();
                        console.error('Update event failed:', text);
                        throw new Error(extractErrorMessage(text));
                    }

                    calendar.refetchEvents();
                    updateHoursWidget(calendar ? calendar.view.activeStart : undefined);
                } else {
                    // Create new event
                    await createEvent(issueKey, start, duration, description, subtaskTypeId, subtaskKey);
                }
            } catch (error) {
                console.error('Error saving event:', error);
                alert('Failed to save time entry: ' + error.message);
                calendar.refetchEvents();
            } finally {
                hideLoading();
            }
        });
    }

    async function openEditDialog(event) {
        const dialog = document.getElementById('editDialog');
        const title = document.getElementById('dialogTitle');
        const deleteBtn = document.getElementById('deleteBtn');
        const saveBtn = dialog.querySelector('button[type="submit"]');

        // Track the event being edited so we can unselect it on close
        currentEditingEvent = event;

        // Entries on archived projects are read-only, like another user's calendar
        const archived = !!event.extendedProps.projectArchived;
        const readOnly = !!isViewOnlyMode() || archived;

        const eventId = `${event.extendedProps.issueKey}-${event.extendedProps.worklogId}`;
        document.getElementById('eventId').value = eventId;
        document.getElementById('eventIssue').value = event.title;
        document.getElementById('eventIssueKey').value = event.extendedProps.issueKey;
        document.getElementById('eventStart').value = formatDateTimeLocal(event.start);
        document.getElementById('eventDuration').value = getDurationMinutes(event);
        document.getElementById('eventDescription').value = event.extendedProps.description || '';

        dialogEntry = {
            start: event.start,
            durationMin: getDurationMinutes(event),
            description: event.extendedProps.description || ''
        };

        // Load sub-task options; a worklog on a configured sub-task resolves to
        // its parent with the current association pre-checked (this also covers
        // time entered in Jira/JSM directly)
        dialogSubtaskOptions = null;
        dialogOriginalTarget = null;
        showLoading();
        try {
            dialogSubtaskOptions = await fetchSubtaskOptions(event.extendedProps.issueKey);
            dialogOriginalTarget = {
                key: dialogSubtaskOptions.issue_key,
                typeId: dialogSubtaskOptions.current_type_id || null,
                subtaskKey: dialogSubtaskOptions.current_subtask || null
            };
        } catch (error) {
            // Dialog still works; the sub-task association just cannot be changed
            console.error('Error loading sub-task options:', error);
        } finally {
            hideLoading();
        }
        // Changing the sub-task moves the worklog, which needs their sign-in
        const canMove = canAddTime();
        renderSubtaskSection(dialogSubtaskOptions, readOnly || !canMove);

        let note = archived ? ARCHIVED_ENTRY_NOTE : '';
        if (!readOnly && isSDMMode()) {
            note = `You're changing ${currentUser.impersonating_name}'s time. Jira will show you as the one who changed it.`;
            if (!canMove) {
                note += ` It can't be moved or split until they sign in to JiraTime.`;
            }
        } else if (event.extendedProps.sdmChangedBy) {
            note = [sdmChangeNote(event), note].filter(Boolean).join(' ');
        }

        title.textContent = readOnly ? 'View Time Entry' : 'Edit Time Entry';
        deleteBtn.style.display = readOnly ? 'none' : 'inline-block';
        saveBtn.style.display = readOnly ? 'none' : 'inline-block';
        setDialogReadOnly(readOnly, note);
        setSplitMode(false);
        // SDMs can move the entry anywhere (or split it) with the picker
        setupMoveSection(!readOnly && isSDMMode() && canMove && !!dialogSubtaskOptions);

        dialog.showModal();
    }

    // Lock the dialog fields and show why (an empty note hides it)
    function setDialogReadOnly(readOnly, note) {
        dialogReadOnly = readOnly;
        ['eventStart', 'eventDuration', 'eventDescription'].forEach(id => {
            document.getElementById(id).readOnly = readOnly;
        });

        const noteEl = document.getElementById('dialogNote');
        noteEl.textContent = note;
        noteEl.classList.toggle('hidden', !note);
    }

    // Open the dialog to create a new entry (used when the issue's project has
    // billable or non-billable sub-tasks configured)
    function openCreateDialog(options, start) {
        const dialog = document.getElementById('editDialog');
        const title = document.getElementById('dialogTitle');
        const deleteBtn = document.getElementById('deleteBtn');
        const saveBtn = dialog.querySelector('button[type="submit"]');

        currentEditingEvent = null;
        dialogSubtaskOptions = options;
        dialogOriginalTarget = null;
        dialogEntry = null;

        document.getElementById('eventId').value = '';
        document.getElementById('eventIssue').value = options.issue_title;
        document.getElementById('eventIssueKey').value = options.issue_key;
        document.getElementById('eventStart').value = formatDateTimeLocal(start);
        document.getElementById('eventDuration').value = 30;
        document.getElementById('eventDescription').value = '';

        renderSubtaskSection(options, false);

        title.textContent = 'New Time Entry';
        deleteBtn.style.display = 'none';
        saveBtn.style.display = 'inline-block';
        setDialogReadOnly(false, isSDMMode()
            ? `This will be logged as ${currentUser.impersonating_name}'s time, entered by you.`
            : '');
        setSplitMode(false);
        setupMoveSection(false);

        dialog.showModal();
    }

    async function splitEvent(eventId, keepMin, parentKey, subtaskTypeId, subtaskKey) {
        showLoading();
        try {
            const body = { duration_min: keepMin, parent_key: parentKey };
            if (subtaskTypeId) body.subtask_type_id = subtaskTypeId;
            if (subtaskKey) body.subtask_key = subtaskKey;

            const response = await apiFetch(`/api/events/${encodeURIComponent(eventId)}/split`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(body)
            });

            if (!response.ok) {
                const text = await response.text();
                console.error('Split event failed:', text);
                throw new Error(extractErrorMessage(text));
            }
        } catch (error) {
            console.error('Error splitting event:', error);
            alert('Failed to split time entry: ' + error.message);
        } finally {
            calendar.refetchEvents();
            updateHoursWidget(calendar ? calendar.view.activeStart : undefined);
            hideLoading();
        }
    }

    // Split mode: the original keeps the minutes entered and the rest goes to
    // the issue chosen in the picker; the entry's own fields are not edited
    function setSplitMode(on) {
        const wasOn = splitMode;
        splitMode = on;
        const dialog = document.getElementById('editDialog');
        const splitBtn = document.getElementById('splitBtn');
        const splitDuration = document.getElementById('splitDuration');

        document.getElementById('splitSection').classList.toggle('hidden', !on);
        splitBtn.textContent = on ? 'Cancel split' : 'Split…';
        document.getElementById('moveSectionLabel').textContent = on ? 'Rest goes to' : 'Move to';
        dialog.querySelector('button[type="submit"]').textContent = on ? 'Split' : 'Save';
        // Delete's visibility is set inline when the dialog opens
        if (on) {
            document.getElementById('deleteBtn').style.display = 'none';
        } else if (wasOn) {
            document.getElementById('deleteBtn').style.display = 'inline-block';
        }

        if (on && dialogEntry) {
            // Split what is saved, not unsaved edits
            document.getElementById('eventStart').value = formatDateTimeLocal(dialogEntry.start);
            document.getElementById('eventDuration').value = dialogEntry.durationMin;
            document.getElementById('eventDescription').value = dialogEntry.description;
            const half = Math.round(dialogEntry.durationMin / 2 / 15) * 15;
            splitDuration.value = Math.min(Math.max(half, 1), dialogEntry.durationMin - 1);
            splitDuration.max = dialogEntry.durationMin - 1;
            document.getElementById('splitHint').textContent = `Of the entry's ${dialogEntry.durationMin} minutes. The rest is logged by the same person, starting where this part ends.`;
        }
        ['eventStart', 'eventDuration', 'eventDescription'].forEach(id => {
            document.getElementById(id).readOnly = on || dialogReadOnly;
        });

        updateActionSummary();
    }

    // Where Save (or Split) will log the time, in words
    function describeTarget() {
        const options = dialogSubtaskOptions;
        if (!options) return '';
        const type = (options.subtask_types || []).find(t => t.id === selectedSubtaskTypeId);
        if (selectedSubtaskKey) {
            return `[${selectedSubtaskKey}]${type ? ` (${type.name} sub-task of ${options.issue_key})` : ''}`;
        }
        if (type) {
            return `a "${type.name}" sub-task of [${options.issue_key}]`;
        }
        return options.issue_title;
    }

    function targetChanged() {
        const options = dialogSubtaskOptions;
        const original = dialogOriginalTarget;
        if (!options || !original) return false;
        return options.issue_key !== original.key ||
            (selectedSubtaskTypeId || null) !== original.typeId ||
            (selectedSubtaskKey || null) !== original.subtaskKey;
    }

    // Spell out what saving will do when it does more than update the entry
    function updateActionSummary() {
        const el = document.getElementById('actionSummary');
        const editing = !!document.getElementById('eventId').value;
        let text = '';

        if (editing && splitMode && dialogEntry) {
            const keep = parseInt(document.getElementById('splitDuration').value, 10);
            const owner = isSDMMode() ? currentUser.impersonating_name : 'you';
            text = keep > 0 && keep < dialogEntry.durationMin
                ? `Split keeps ${keep} min on this entry and logs the other ${dialogEntry.durationMin - keep} min on ${describeTarget()}, as ${owner}.`
                : `Keep fewer than the entry's ${dialogEntry.durationMin} minutes on it.`;
        } else if (editing && !dialogReadOnly && targetChanged()) {
            text = `Save moves this entry to ${describeTarget()}.`;
        }

        el.textContent = text;
        el.classList.toggle('hidden', !text);
    }

    // SDM Move-to picker: a searchable Epic > Issue > Sub-task tree. Picking an
    // issue makes it the target for Save (or the rest of a split), with its
    // own sub-task options.
    let moveTreeProject = null;
    let moveSearchTimeout = null;

    function initMovePicker() {
        const searchInput = document.getElementById('moveSearch');
        const results = document.getElementById('moveSearchResults');

        searchInput.addEventListener('input', () => {
            const query = searchInput.value.trim();
            clearTimeout(moveSearchTimeout);
            if (query.length < 2) {
                results.classList.add('hidden');
                return;
            }
            moveSearchTimeout = setTimeout(async () => {
                try {
                    const response = await apiFetch(`/api/issues/search?q=${encodeURIComponent(query)}`);
                    if (!response.ok) throw new Error('Search failed');
                    renderMoveSearchResults(await response.json() || []);
                } catch (error) {
                    console.error('Error searching issues:', error);
                }
            }, 300);
        });

        // Enter picks the first result rather than saving the dialog
        searchInput.addEventListener('keydown', (e) => {
            if (e.key !== 'Enter') return;
            e.preventDefault();
            const first = results.querySelector('.move-result');
            if (first && !results.classList.contains('hidden')) first.click();
        });

        document.addEventListener('click', (e) => {
            if (!searchInput.contains(e.target) && !results.contains(e.target)) {
                results.classList.add('hidden');
            }
        });
    }

    function setupMoveSection(show) {
        document.getElementById('moveSection').classList.toggle('hidden', !show);
        document.getElementById('splitBtn').classList.toggle('hidden', !show || !dialogEntry || dialogEntry.durationMin < 2);
        if (!show) return;

        document.getElementById('moveSearch').value = '';
        document.getElementById('moveSearchResults').classList.add('hidden');
        renderMoveTarget();
        moveTreeProject = null;
        loadMoveTree(dialogSubtaskOptions.project_key);
    }

    function renderMoveSearchResults(issues) {
        const container = document.getElementById('moveSearchResults');
        container.innerHTML = '';
        if (issues.length === 0) {
            container.classList.add('hidden');
            return;
        }

        issues.forEach(issue => {
            const row = document.createElement('button');
            row.type = 'button';
            row.className = 'move-result';
            row.appendChild(issueLabel(issue.key, issue.summary));
            if (issue.parent_key) {
                const parent = document.createElement('span');
                parent.className = 'move-result-parent';
                parent.textContent = `in ${issue.parent_key} ${issue.parent_summary || ''}`;
                row.appendChild(parent);
            }
            row.addEventListener('click', () => {
                container.classList.add('hidden');
                document.getElementById('moveSearch').value = '';
                selectMoveTarget(issue.key);
                if (issue.project_key && issue.project_key !== moveTreeProject) {
                    loadMoveTree(issue.project_key);
                }
            });
            container.appendChild(row);
        });
        container.classList.remove('hidden');
    }

    // Make an issue the target; its sub-task options replace the current ones
    async function selectMoveTarget(key) {
        const saveBtn = document.getElementById('editDialog').querySelector('button[type="submit"]');
        saveBtn.disabled = true;
        showLoading();
        try {
            dialogSubtaskOptions = await fetchSubtaskOptions(key);
            renderSubtaskSection(dialogSubtaskOptions, false);
            renderMoveTarget();
        } catch (error) {
            console.error('Error loading issue:', error);
            alert(`Failed to load ${key}: ${error.message}`);
        } finally {
            saveBtn.disabled = false;
            hideLoading();
        }
    }

    function renderMoveTarget() {
        const el = document.getElementById('moveTarget');
        el.innerHTML = '';
        if (!dialogSubtaskOptions) return;

        const label = document.createElement('span');
        label.textContent = dialogSubtaskOptions.issue_title;
        el.appendChild(label);

        if (dialogOriginalTarget && dialogSubtaskOptions.issue_key !== dialogOriginalTarget.key) {
            const reset = document.createElement('button');
            reset.type = 'button';
            reset.className = 'link-button';
            reset.textContent = 'Keep current issue';
            reset.addEventListener('click', () => selectMoveTarget(document.getElementById('eventIssueKey').value));
            el.appendChild(reset);
        }

        highlightMoveTarget();
    }

    // Mark the target in the tree: the chosen sub-task, else the issue
    function highlightMoveTarget() {
        const key = selectedSubtaskKey || (dialogSubtaskOptions && dialogSubtaskOptions.issue_key);
        document.querySelectorAll('#moveTree .move-tree-issue').forEach(row => {
            row.classList.toggle('selected', row.dataset.key === key);
        });
    }

    async function fetchHierarchy(params) {
        const response = await apiFetch('/api/hierarchy?' + new URLSearchParams(params));
        if (!response.ok) {
            throw new Error(extractErrorMessage(await response.text()));
        }
        return response.json();
    }

    // Load a project's epics (and its "No epic" group); epics load their
    // issues when expanded
    async function loadMoveTree(projectKey) {
        const tree = document.getElementById('moveTree');
        if (!projectKey || projectKey === moveTreeProject) return;
        moveTreeProject = projectKey;
        tree.innerHTML = '';
        tree.appendChild(treeNote('Loading issues…'));

        try {
            const res = await fetchHierarchy({ project: projectKey });
            if (moveTreeProject !== projectKey) return; // another project was picked meanwhile

            tree.innerHTML = '';
            const header = document.createElement('div');
            header.className = 'move-tree-project';
            header.textContent = `${res.project.name} (${res.project.key})`;
            tree.appendChild(header);

            // Empty lists are left out of the response (e.g. JSM projects have no epics)
            const epics = res.epics || [];
            epics.forEach(epic => {
                tree.appendChild(treeGroup(issueLabel(epic.key, epic.summary, epic.done ? epic.status : '', 'Epic'),
                    () => fetchHierarchy({ parent: epic.key })));
            });

            // Without epics this group is every request in the project, so it
            // stays closed until asked for: search is quicker on a big project
            if (epics.length === 0) {
                tree.appendChild(treeNote('This project has no epics. Search above, or open the list of its most recently updated issues.'));
            }
            const noEpic = document.createElement('span');
            noEpic.textContent = epics.length > 0 ? 'No epic' : 'Issues';
            tree.appendChild(treeGroup(noEpic, () => fetchHierarchy({ project: projectKey, no_epic: '1' })));

            if (res.truncated) {
                tree.appendChild(treeNote('Showing the most recently updated epics. Search to find others.'));
            }
        } catch (error) {
            console.error('Error loading issue tree:', error);
            tree.innerHTML = '';
            tree.appendChild(treeNote(`Couldn't load issues: ${error.message}`));
            moveTreeProject = null;
        }
    }

    // A collapsible tree group whose issues load the first time it opens
    function treeGroup(label, load, open) {
        const group = document.createElement('div');
        group.className = 'move-tree-group';

        const toggle = document.createElement('button');
        toggle.type = 'button';
        toggle.className = 'move-tree-toggle';
        const caret = document.createElement('span');
        caret.className = 'move-tree-caret';
        toggle.appendChild(caret);
        toggle.appendChild(label);

        const children = document.createElement('div');
        children.className = 'move-tree-children hidden';
        let loaded = false;

        const setOpen = (isOpen) => {
            children.classList.toggle('hidden', !isOpen);
            caret.textContent = isOpen ? '▾' : '▸';
            toggle.setAttribute('aria-expanded', String(isOpen));
            if (isOpen && !loaded) {
                loaded = true;
                children.appendChild(treeNote('Loading…'));
                load().then(res => renderTreeIssues(children, res)).catch(error => {
                    loaded = false;
                    children.innerHTML = '';
                    children.appendChild(treeNote(`Couldn't load issues: ${error.message}`));
                });
            }
        };
        toggle.addEventListener('click', () => setOpen(children.classList.contains('hidden')));
        setOpen(!!open);

        group.appendChild(toggle);
        group.appendChild(children);
        return group;
    }

    function renderTreeIssues(container, res) {
        container.innerHTML = '';
        const issues = res.issues || [];
        if (issues.length === 0) {
            container.appendChild(treeNote('No issues'));
        }
        issues.forEach(issue => {
            container.appendChild(treeIssue(issue, false));
            (issue.subtasks || []).forEach(st => container.appendChild(treeIssue(st, true)));
        });
        if (res.truncated) {
            container.appendChild(treeNote('Showing the most recently updated issues. Search to find others.'));
        }
        highlightMoveTarget();
    }

    function treeIssue(issue, isSubtask) {
        const row = document.createElement('button');
        row.type = 'button';
        row.className = 'move-tree-issue' + (isSubtask ? ' subtask' : '') + (issue.done ? ' done' : '');
        row.dataset.key = issue.key;
        row.appendChild(issueLabel(issue.key, issue.summary, issue.status));
        row.addEventListener('click', () => selectMoveTarget(issue.key));
        return row;
    }

    // "KEY summary [status] [kind]" for picker rows
    function issueLabel(key, summary, status, kind) {
        const label = document.createElement('span');
        label.className = 'move-label';
        const keyEl = document.createElement('span');
        keyEl.className = 'subtask-key';
        keyEl.textContent = key;
        label.appendChild(keyEl);
        const summaryEl = document.createElement('span');
        summaryEl.className = 'move-summary';
        summaryEl.textContent = summary;
        label.appendChild(summaryEl);
        [kind, status].filter(Boolean).forEach(text => {
            const badge = document.createElement('span');
            badge.className = 'subtask-status';
            badge.textContent = text;
            label.appendChild(badge);
        });
        return label;
    }

    function treeNote(text) {
        const note = document.createElement('p');
        note.className = 'subtask-hint';
        note.textContent = text;
        return note;
    }

    // Render the configured sub-task checkboxes (billable or non-billable, per
    // the project's mode). Type checkboxes act like toggleable radios (one at a
    // time); selecting a type reveals its existing sub-tasks, where at most one
    // can be picked. Read-only dialogs show the choices disabled.
    function renderSubtaskSection(options, readOnly) {
        const section = document.getElementById('subtaskSection');
        const label = document.getElementById('subtaskSectionLabel');
        const container = document.getElementById('subtaskTypes');
        container.innerHTML = '';
        selectedSubtaskTypeId = null;
        selectedSubtaskKey = null;

        if (!options || !options.subtask_types || options.subtask_types.length === 0) {
            section.classList.add('hidden');
            updateActionSummary();
            return;
        }

        // Label the section by mode: billable sub-tasks reclassify time as
        // billable, non-billable sub-tasks reclassify it as non-billable
        if (label) {
            label.textContent = options.subtask_mode === 'non_billable'
                ? 'Non-billable via Sub-task'
                : 'Billable via Sub-task';
        }

        const disabled = !!readOnly;

        options.subtask_types.forEach(type => {
            const typeEl = document.createElement('div');
            typeEl.className = 'subtask-type';

            const typeLabel = document.createElement('label');
            typeLabel.className = 'subtask-option';
            const typeCheckbox = document.createElement('input');
            typeCheckbox.type = 'checkbox';
            typeCheckbox.className = 'subtask-type-checkbox';
            typeCheckbox.dataset.typeId = type.id;
            typeCheckbox.disabled = disabled;
            const typeName = document.createElement('span');
            typeName.textContent = type.name;
            typeLabel.appendChild(typeCheckbox);
            typeLabel.appendChild(typeName);
            typeEl.appendChild(typeLabel);

            const listEl = document.createElement('div');
            listEl.className = 'subtask-list hidden';

            const existing = (options.subtasks || []).filter(st => st.type_id === type.id);

            // Show at a glance that this type already has sub-tasks
            if (existing.length > 0) {
                const count = document.createElement('span');
                count.className = 'subtask-count';
                count.textContent = existing.length === 1 ? '1 existing' : `${existing.length} existing`;
                typeLabel.appendChild(count);
            }
            existing.forEach(st => {
                const itemLabel = document.createElement('label');
                itemLabel.className = 'subtask-option subtask-item';
                const itemCheckbox = document.createElement('input');
                itemCheckbox.type = 'checkbox';
                itemCheckbox.className = 'subtask-item-checkbox';
                itemCheckbox.dataset.subtaskKey = st.key;
                itemCheckbox.disabled = disabled;
                const itemText = document.createElement('span');
                itemText.className = 'subtask-item-text';
                const itemKey = document.createElement('span');
                itemKey.className = 'subtask-key';
                itemKey.textContent = st.key;
                itemText.appendChild(itemKey);
                itemText.appendChild(document.createTextNode(st.summary));
                const statusBadge = document.createElement('span');
                statusBadge.className = 'subtask-status';
                statusBadge.textContent = st.status;
                itemLabel.appendChild(itemCheckbox);
                itemLabel.appendChild(itemText);
                itemLabel.appendChild(statusBadge);
                listEl.appendChild(itemLabel);

                itemCheckbox.addEventListener('change', () => {
                    if (itemCheckbox.checked) {
                        // Selecting a sub-task implies its type: check it and
                        // make it the only selected type/list
                        typeCheckbox.checked = true;
                        container.querySelectorAll('.subtask-type-checkbox').forEach(cb => {
                            if (cb !== typeCheckbox) cb.checked = false;
                        });
                        container.querySelectorAll('.subtask-list').forEach(el => {
                            if (el !== listEl) el.classList.add('hidden');
                        });
                        listEl.classList.remove('hidden');

                        // Only one specific sub-task can be selected
                        container.querySelectorAll('.subtask-item-checkbox').forEach(cb => {
                            if (cb !== itemCheckbox) cb.checked = false;
                        });
                        selectedSubtaskTypeId = type.id;
                        selectedSubtaskKey = st.key;
                    } else {
                        selectedSubtaskKey = null;
                    }
                    updateSubtaskHint(listEl, type.name);
                    updateActionSummary();
                    highlightMoveTarget();
                });
            });

            const hint = document.createElement('p');
            hint.className = 'subtask-hint hidden';
            listEl.appendChild(hint);

            typeEl.appendChild(listEl);
            container.appendChild(typeEl);

            typeCheckbox.addEventListener('change', () => {
                if (typeCheckbox.checked) {
                    // Only one sub-task type can be selected at a time
                    container.querySelectorAll('.subtask-type-checkbox').forEach(cb => {
                        if (cb !== typeCheckbox) cb.checked = false;
                    });
                    container.querySelectorAll('.subtask-list').forEach(el => {
                        if (el !== listEl) el.classList.add('hidden');
                    });
                    container.querySelectorAll('.subtask-item-checkbox').forEach(cb => {
                        cb.checked = false;
                    });
                    selectedSubtaskTypeId = type.id;
                    selectedSubtaskKey = null;
                    listEl.classList.remove('hidden');
                } else {
                    selectedSubtaskTypeId = null;
                    selectedSubtaskKey = null;
                    listEl.classList.add('hidden');
                }
                updateSubtaskHint(listEl, type.name);
                updateActionSummary();
                highlightMoveTarget();
            });
        });

        // Pre-check the current association for existing entries on a configured
        // sub-task (regardless of whether the time was logged here or in Jira)
        if (options.current_type_id) {
            const typeCb = container.querySelector(`.subtask-type-checkbox[data-type-id="${CSS.escape(options.current_type_id)}"]`);
            if (typeCb) {
                typeCb.checked = true;
                typeCb.dispatchEvent(new Event('change'));

                if (options.current_subtask) {
                    const itemCb = container.querySelector(`.subtask-item-checkbox[data-subtask-key="${CSS.escape(options.current_subtask)}"]`);
                    if (itemCb) {
                        itemCb.checked = true;
                        itemCb.dispatchEvent(new Event('change'));
                    }
                }
            }
        }

        section.classList.remove('hidden');
        updateActionSummary();
    }

    function updateSubtaskHint(listEl, typeName) {
        const hint = listEl.querySelector('.subtask-hint');
        if (!hint) return;

        const anyChecked = !!listEl.querySelector('.subtask-item-checkbox:checked');
        if (listEl.classList.contains('hidden') || anyChecked) {
            hint.classList.add('hidden');
        } else {
            hint.textContent = `Time will be logged to a "${typeName}" sub-task (created if needed)`;
            hint.classList.remove('hidden');
        }
    }

    // Settings Functions
    function initSettings() {
        const settingsBtn = document.getElementById('settingsBtn');
        const settingsDialog = document.getElementById('settingsDialog');
        const settingsForm = document.getElementById('settingsForm');
        const settingsCancelBtn = document.getElementById('settingsCancelBtn');
        const presetSelect = document.getElementById('timeRangePreset');
        const customTimeRange = document.getElementById('customTimeRange');
        const customStartTime = document.getElementById('customStartTime');
        const customEndTime = document.getElementById('customEndTime');
        const themeSelect = document.getElementById('themeSelect');

        // Load current settings into the form
        function loadSettingsIntoForm() {
            const settings = getTimeRangeSettings();
            presetSelect.value = settings.preset;
            customStartTime.value = settings.customStart;
            customEndTime.value = settings.customEnd;
            themeSelect.value = getTheme();

            // Show/hide custom time range inputs
            if (settings.preset === 'custom') {
                customTimeRange.classList.remove('hidden');
            } else {
                customTimeRange.classList.add('hidden');
            }
        }

        // Open settings dialog
        settingsBtn.addEventListener('click', () => {
            loadSettingsIntoForm();

            // Manager administration is only shown to super users
            const managersSection = document.getElementById('managersSection');
            if (currentUser && currentUser.is_super_user) {
                managersSection.classList.remove('hidden');
                loadManagerList();
            } else {
                managersSection.classList.add('hidden');
            }

            settingsDialog.showModal();
        });

        initManagersUI();

        // Handle preset change
        presetSelect.addEventListener('change', () => {
            if (presetSelect.value === 'custom') {
                customTimeRange.classList.remove('hidden');
            } else {
                customTimeRange.classList.add('hidden');
            }
        });

        // Cancel button
        settingsCancelBtn.addEventListener('click', () => {
            settingsDialog.close();
        });

        // Save settings
        settingsForm.addEventListener('submit', (e) => {
            e.preventDefault();

            const settings = {
                preset: presetSelect.value,
                customStart: customStartTime.value,
                customEnd: customEndTime.value
            };

            saveTimeRangeSettings(settings);

            const theme = themeSelect.value;
            saveTheme(theme);
            applyTheme(theme);

            settingsDialog.close();

            // Apply new time range to calendar
            applyTimeRange();
        });
    }

    // Manager Administration (settings dialog, super users only)
    let managerSearchTimeout = null;

    function initManagersUI() {
        const searchInput = document.getElementById('managerSearch');
        const resultsContainer = document.getElementById('managerSearchResults');

        searchInput.addEventListener('input', (e) => {
            const query = e.target.value.trim();

            if (managerSearchTimeout) {
                clearTimeout(managerSearchTimeout);
            }

            if (query.length < 2) {
                resultsContainer.classList.add('hidden');
                return;
            }

            managerSearchTimeout = setTimeout(async () => {
                try {
                    const response = await apiFetch(`/api/users/search?q=${encodeURIComponent(query)}`);
                    if (response.ok) {
                        renderManagerSearchResults(await response.json());
                    }
                } catch (error) {
                    console.error('Error searching users:', error);
                }
            }, 300);
        });

        // Close results when clicking outside
        document.addEventListener('click', (e) => {
            if (!searchInput.contains(e.target) && !resultsContainer.contains(e.target)) {
                resultsContainer.classList.add('hidden');
            }
        });
    }

    async function loadManagerList() {
        try {
            const response = await apiFetch('/api/managers');
            if (!response.ok) {
                throw new Error('Failed to fetch managers');
            }
            renderManagerList(await response.json() || []);
        } catch (error) {
            console.error('Error loading managers:', error);
        }
    }

    function renderManagerList(managers) {
        const container = document.getElementById('managerList');
        container.innerHTML = '';

        if (managers.length === 0) {
            const empty = document.createElement('p');
            empty.className = 'help-text';
            empty.textContent = 'No managers yet. Search above to add one.';
            container.appendChild(empty);
            return;
        }

        managers.forEach(manager => {
            const item = document.createElement('div');
            item.className = 'manager-item';

            if (manager.avatar_url) {
                const avatar = document.createElement('img');
                avatar.src = manager.avatar_url;
                avatar.alt = '';
                item.appendChild(avatar);
            }

            const name = document.createElement('span');
            name.className = 'manager-name';
            name.textContent = manager.display_name;
            item.appendChild(name);

            // Manager: view-only; SDM: can also change the time they view
            const role = document.createElement('select');
            role.className = 'manager-role';
            role.setAttribute('aria-label', `Role for ${manager.display_name}`);
            [['manager', 'Manager (view)'], ['sdm', 'SDM (edit)']].forEach(([value, label]) => {
                const option = document.createElement('option');
                option.value = value;
                option.textContent = label;
                role.appendChild(option);
            });
            role.value = manager.role || 'manager';
            role.addEventListener('change', () => setManagerRole(manager, role));
            item.appendChild(role);

            const removeBtn = document.createElement('button');
            removeBtn.type = 'button';
            removeBtn.className = 'btn btn-small btn-danger';
            removeBtn.textContent = 'Remove';
            removeBtn.addEventListener('click', () => removeManager(manager));
            item.appendChild(removeBtn);

            container.appendChild(item);
        });
    }

    function renderManagerSearchResults(users) {
        const container = document.getElementById('managerSearchResults');
        container.innerHTML = '';

        if (!users || users.length === 0) {
            container.classList.add('hidden');
            return;
        }

        users.forEach(user => {
            const item = document.createElement('div');
            item.className = 'manager-user-item';

            const avatar = document.createElement('img');
            avatar.src = user.avatar_url;
            avatar.alt = '';
            item.appendChild(avatar);

            const name = document.createElement('span');
            name.textContent = user.display_name;
            item.appendChild(name);

            item.addEventListener('click', () => addManager(user));
            container.appendChild(item);
        });

        container.classList.remove('hidden');
    }

    async function addManager(user) {
        document.getElementById('managerSearch').value = '';
        document.getElementById('managerSearchResults').classList.add('hidden');

        try {
            const response = await apiFetch('/api/managers', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    account_id: user.account_id,
                    display_name: user.display_name,
                    avatar_url: user.avatar_url
                })
            });

            if (!response.ok) {
                throw new Error('Failed to add manager');
            }

            await loadManagerList();
        } catch (error) {
            console.error('Error adding manager:', error);
            alert('Failed to add manager');
        }
    }

    async function setManagerRole(manager, select) {
        const role = select.value;
        if (role === 'sdm' && !confirm(`Let ${manager.display_name} change other people's time? Their changes are made with their own Jira access, so they also need the Edit All Worklogs and Delete All Worklogs permissions in Jira.`)) {
            select.value = manager.role || 'manager';
            return;
        }

        try {
            const response = await apiFetch('/api/managers', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    account_id: manager.account_id,
                    display_name: manager.display_name,
                    avatar_url: manager.avatar_url,
                    role: role
                })
            });

            if (!response.ok) {
                throw new Error('Failed to change role');
            }
            manager.role = role;
        } catch (error) {
            console.error('Error changing manager role:', error);
            alert('Failed to change role');
            select.value = manager.role || 'manager';
        }
    }

    async function removeManager(manager) {
        if (!confirm(`Remove ${manager.display_name} as a manager?`)) {
            return;
        }

        try {
            const response = await apiFetch(`/api/managers/${encodeURIComponent(manager.account_id)}`, {
                method: 'DELETE'
            });

            // 404 means it was already removed - refreshing the list is enough
            if (!response.ok && response.status !== 404) {
                throw new Error('Failed to remove manager');
            }

            await loadManagerList();
        } catch (error) {
            console.error('Error removing manager:', error);
            alert('Failed to remove manager');
        }
    }

    // Sidebar Functions
    function initSidebar() {
        const toggle = document.getElementById('sidebarToggle');
        const sidebar = document.getElementById('sidebar');
        const refreshBtn = document.getElementById('refreshBtn');

        toggle.addEventListener('click', () => {
            sidebar.classList.toggle('open');
        });

        refreshBtn.addEventListener('click', async () => {
            showLoading();
            try {
                await apiFetch('/api/refresh', { method: 'POST' });
                await loadIssues();
                calendar.refetchEvents();
                updateHoursWidget(calendar ? calendar.view.activeStart : undefined);
            } finally {
                hideLoading();
            }
        });
    }

    async function loadUserInfo() {
        try {
            const response = await apiFetch('/api/user');
            if (response.ok) {
                currentUser = await response.json();
                jiraSiteURL = currentUser.site_url || '';
                const userInfo = document.getElementById('userInfo');
                userInfo.innerHTML = `
                    <img src="${currentUser.avatar_url}" alt="" class="user-avatar">
                    <span class="user-name">${currentUser.display_name}</span>
                `;

                // Setup impersonation UI based on user permissions
                setupImpersonationUI();
            }
        } catch (error) {
            console.error('Error loading user info:', error);
        }
    }

    function setupImpersonationUI() {
        const impersonateControls = document.getElementById('impersonateControls');
        const impersonationBar = document.getElementById('impersonationBar');
        const impersonatingName = document.getElementById('impersonatingName');
        const noSignIn = document.getElementById('impersonationNoSignIn');
        const container = document.querySelector('.app-container');

        // Shown up front so SDMs know before trying to add time
        noSignIn.textContent = isSDMMode() && !canAddTime()
            ? `${noSignInMessage()} Until then, you only see the entries you can open in Jira.`
            : '';
        noSignIn.classList.toggle('hidden', !noSignIn.textContent);

        // Check if currently impersonating
        if (currentUser.impersonating_id) {
            impersonationBar.classList.remove('hidden');
            impersonatingName.textContent = currentUser.impersonating_name;
            document.getElementById('viewingAsLabel').textContent = isSDMMode() ? 'Editing as:' : 'Viewing as:';
            container.classList.toggle('view-only-mode', isViewOnlyMode());
            container.classList.toggle('sdm-mode', isSDMMode());

            // Hide impersonate search when impersonating
            impersonateControls.classList.add('hidden');
        } else {
            impersonationBar.classList.add('hidden');
            container.classList.remove('view-only-mode', 'sdm-mode');

            // Show impersonate controls for super users and managers
            if (currentUser.can_impersonate) {
                impersonateControls.classList.remove('hidden');
                initImpersonationSearch();
            } else {
                impersonateControls.classList.add('hidden');
            }
        }

        // Setup stop impersonation button
        const stopBtn = document.getElementById('stopImpersonateBtn');
        stopBtn.addEventListener('click', stopImpersonation);
    }

    let impersonateSearchTimeout = null;

    function initImpersonationSearch() {
        const searchInput = document.getElementById('impersonateSearch');
        const resultsContainer = document.getElementById('impersonateResults');

        searchInput.addEventListener('input', (e) => {
            const query = e.target.value.trim();

            if (impersonateSearchTimeout) {
                clearTimeout(impersonateSearchTimeout);
            }

            if (query.length < 2) {
                resultsContainer.classList.add('hidden');
                return;
            }

            impersonateSearchTimeout = setTimeout(async () => {
                try {
                    const response = await apiFetch(`/api/users/search?q=${encodeURIComponent(query)}`);
                    if (response.ok) {
                        const users = await response.json();
                        renderImpersonateResults(users);
                    }
                } catch (error) {
                    console.error('Error searching users:', error);
                }
            }, 300);
        });

        // Close results when clicking outside
        document.addEventListener('click', (e) => {
            if (!searchInput.contains(e.target) && !resultsContainer.contains(e.target)) {
                resultsContainer.classList.add('hidden');
            }
        });
    }

    function renderImpersonateResults(users) {
        const container = document.getElementById('impersonateResults');
        container.innerHTML = '';

        if (users.length === 0) {
            container.classList.add('hidden');
            return;
        }

        users.forEach(user => {
            const item = document.createElement('div');
            item.className = 'impersonate-user-item';

            const avatar = document.createElement('img');
            avatar.src = user.avatar_url;
            avatar.alt = '';
            item.appendChild(avatar);

            const name = document.createElement('span');
            name.textContent = user.display_name;
            item.appendChild(name);

            // SDMs can only add time for people who use JiraTime
            if (currentUser.can_edit_others && !user.has_jiratime) {
                const badge = document.createElement('span');
                badge.className = 'no-jiratime-badge';
                badge.textContent = 'Not using JiraTime';
                badge.title = 'Their time can be edited, moved and deleted, but not added or split, until they sign in to JiraTime';
                item.appendChild(badge);
            }

            item.addEventListener('click', () => startImpersonation(user));
            container.appendChild(item);
        });

        container.classList.remove('hidden');
    }

    async function startImpersonation(user) {
        showLoading();
        try {
            const response = await apiFetch('/api/impersonate', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    account_id: user.account_id,
                    display_name: user.display_name
                })
            });

            if (!response.ok) {
                throw new Error('Failed to start impersonation');
            }

            // Clear search and results
            document.getElementById('impersonateSearch').value = '';
            document.getElementById('impersonateResults').classList.add('hidden');

            // Reload user info and data
            await loadUserInfo();
            await loadIssues();
            calendar.refetchEvents();
            updateHoursWidget(calendar ? calendar.view.activeStart : undefined);
        } catch (error) {
            console.error('Error starting impersonation:', error);
            alert('Failed to start impersonation');
        } finally {
            hideLoading();
        }
    }

    async function stopImpersonation() {
        showLoading();
        try {
            const response = await apiFetch('/api/impersonate/stop', {
                method: 'POST'
            });

            if (!response.ok) {
                throw new Error('Failed to stop impersonation');
            }

            // Reload user info and data
            await loadUserInfo();
            await loadIssues();
            calendar.refetchEvents();
            updateHoursWidget(calendar ? calendar.view.activeStart : undefined);
        } catch (error) {
            console.error('Error stopping impersonation:', error);
            alert('Failed to stop impersonation');
        } finally {
            hideLoading();
        }
    }

    // Shown when the server could not use the viewed user's own Jira access,
    // so entries on projects the viewer can't open are missing
    function setViewLimited(limited) {
        const el = document.getElementById('impersonationLimited');
        const name = (currentUser && currentUser.impersonating_name) || 'This user';
        el.textContent = `Showing only entries you can open in Jira. ${name} needs to sign in to JiraTime for their full calendar to show here.`;
        // An SDM's sign-in warning already says this
        const covered = isSDMMode() && !canAddTime();
        el.classList.toggle('hidden', !limited || covered);
    }

    // Viewing someone else's calendar without being allowed to change it
    function isViewOnlyMode() {
        return !!(currentUser && currentUser.impersonating_id && !currentUser.can_edit_others);
    }

    // An SDM (or super user) changing someone else's calendar
    function isSDMMode() {
        return !!(currentUser && currentUser.impersonating_id && currentUser.can_edit_others);
    }

    // New time for someone else is created with their own JiraTime sign-in
    // (so they are its author); without one it can only be edited
    function canAddTime() {
        return !isSDMMode() || currentUser.impersonating_signed_in !== false;
    }

    // Moving recreates the worklog as its author, so it needs their sign-in
    // like adding, copying and splitting
    function noSignInMessage() {
        return `${currentUser.impersonating_name} hasn't signed in to JiraTime (or their sign-in has expired), so time can't be added, copied, split or moved for them. You can still edit and delete their entries.`;
    }

    // SDM changes to a previous month may alter time that was already
    // invoiced, so they are confirmed first
    function confirmPriorPeriod(...dates) {
        if (!isSDMMode()) return true;
        const monthStart = new Date();
        monthStart.setDate(1);
        monthStart.setHours(0, 0, 0, 0);
        if (!dates.some(d => d && d < monthStart)) return true;
        return confirm('This entry is in a previous month, which may already have been invoiced. Change it anyway?');
    }

    async function loadIssues() {
        try {
            const response = await apiFetch('/api/issues');
            if (!response.ok) {
                throw new Error('Failed to fetch issues');
            }

            const issuesByProject = await response.json();
            renderIssues(issuesByProject || []);
            renderRecentIssues();
        } catch (error) {
            console.error('Error loading issues:', error);
        }
    }

    function renderIssues(issuesByProject) {
        const container = document.getElementById('myIssues');
        container.innerHTML = '';

        issuesByProject.forEach(group => {
            const projectEl = document.createElement('div');
            projectEl.className = 'project-group';

            const headerEl = document.createElement('div');
            headerEl.className = 'project-header';
            headerEl.innerHTML = `<span class="collapse-icon">▼</span> ${group.project.name}`;
            headerEl.addEventListener('click', () => {
                projectEl.classList.toggle('collapsed');
            });

            const issuesEl = document.createElement('div');
            issuesEl.className = 'project-issues';

            group.issues.forEach(issue => {
                const issueEl = createIssueElement(issue);
                issuesEl.appendChild(issueEl);
            });

            projectEl.appendChild(headerEl);
            projectEl.appendChild(issuesEl);
            container.appendChild(projectEl);
        });

        initDraggableIssues();
    }

    function renderRecentIssues() {
        const container = document.getElementById('recentIssues');
        container.innerHTML = '';

        recentIssues.forEach(issue => {
            const issueEl = createIssueElement(issue);
            container.appendChild(issueEl);
        });

        initDraggableIssues();
    }

    function createIssueElement(issue) {
        const el = document.createElement('div');
        el.className = 'issue-item';
        el.dataset.issueKey = issue.key;
        el.dataset.issueSummary = issue.summary;
        el.title = `[${issue.key}] ${issue.summary}`;

        // Create issue text content
        const textSpan = document.createElement('span');
        textSpan.className = 'issue-text';
        textSpan.innerHTML = `<span class="issue-key">${issue.key}</span> ${issue.summary}`;

        // Create external link icon
        const linkIcon = document.createElement('a');
        linkIcon.className = 'issue-link';
        linkIcon.href = `${jiraSiteURL}/browse/${issue.key}`;
        linkIcon.target = '_blank';
        linkIcon.rel = 'noopener noreferrer';
        linkIcon.title = `Open ${issue.key} in Jira`;
        linkIcon.innerHTML = '↗';
        linkIcon.addEventListener('click', (e) => {
            e.stopPropagation(); // Prevent triggering issue selection
        });

        el.appendChild(textSpan);
        el.appendChild(linkIcon);

        // Add click handler for mobile tap-to-create
        el.addEventListener('click', (e) => {
            // Only use tap-to-create on mobile/touch devices
            if (!isMobileView()) return;

            e.preventDefault();
            e.stopPropagation();

            const issueKey = el.dataset.issueKey;
            const issueSummary = el.dataset.issueSummary;

            // Toggle selection
            if (selectedIssue && selectedIssue.key === issueKey) {
                clearSelectedIssue();
            } else {
                selectIssue(issueKey, issueSummary, el);
            }
        });

        return el;
    }

    function selectIssue(key, summary, element) {
        // Clear any previous selection
        clearSelectedIssue();

        // Set new selection
        selectedIssue = { key, summary };
        element.classList.add('selected');

        // Show instruction toast
        showToast(`Tap on calendar to add time for ${key}`);

        // Auto-close sidebar on mobile
        if (isMobileView()) {
            const sidebar = document.getElementById('sidebar');
            sidebar.classList.remove('open');
        }
    }

    function clearSelectedIssue() {
        selectedIssue = null;
        // Remove selected class from all issues
        document.querySelectorAll('.issue-item.selected').forEach(el => {
            el.classList.remove('selected');
        });
        hideToast();
    }

    function isMobileView() {
        return window.innerWidth < 768;
    }

    function showToast(message) {
        let toast = document.getElementById('toast');
        if (!toast) {
            toast = document.createElement('div');
            toast.id = 'toast';
            toast.className = 'toast';
            document.body.appendChild(toast);
        }
        toast.textContent = message;
        toast.classList.add('visible');
    }

    function hideToast() {
        const toast = document.getElementById('toast');
        if (toast) {
            toast.classList.remove('visible');
        }
    }

    function initDraggableIssues() {
        const issues = document.querySelectorAll('.issue-item');
        issues.forEach(issue => {
            // Skip if already initialized
            if (initializedDraggables.has(issue)) {
                return;
            }
            initializedDraggables.add(issue);

            new FullCalendar.Draggable(issue, {
                eventData: function() {
                    return {
                        title: `[${issue.dataset.issueKey}] ${issue.dataset.issueSummary}`,
                        duration: '00:30'
                    };
                }
            });
        });
    }

    function addToRecentIssues(key, summary) {
        // Remove if already exists
        recentIssues = recentIssues.filter(i => i.key !== key);

        // Add to front
        recentIssues.unshift({ key, summary });

        // Limit to MAX_RECENT_ISSUES
        recentIssues = recentIssues.slice(0, MAX_RECENT_ISSUES);

        // Save to localStorage
        localStorage.setItem('recentIssues', JSON.stringify(recentIssues));

        // Re-render
        renderRecentIssues();
    }

    // Search Functions
    let searchTimeout = null;

    function initSearch() {
        const searchInput = document.getElementById('searchInput');

        searchInput.addEventListener('input', (e) => {
            const query = e.target.value.trim();
            const queryLower = query.toLowerCase();

            // Filter local issues in sidebar
            filterSidebarIssues(queryLower);

            // Debounce the API search
            if (searchTimeout) {
                clearTimeout(searchTimeout);
            }

            if (query.length >= 2) {
                searchTimeout = setTimeout(() => {
                    searchJiraIssues(query);
                }, 300);
            } else {
                // Hide search results if query too short
                hideSearchResults();
            }
        });
    }

    async function searchJiraIssues(query) {
        try {
            const response = await apiFetch(`/api/issues/search?q=${encodeURIComponent(query)}`);
            if (!response.ok) {
                throw new Error('Search failed');
            }

            const issues = await response.json();
            renderSearchResults(issues);
        } catch (error) {
            console.error('Error searching issues:', error);
        }
    }

    function renderSearchResults(issues) {
        const section = document.querySelector('.search-results-section');
        const container = document.getElementById('searchResults');
        container.innerHTML = '';

        if (!issues || issues.length === 0) {
            section.classList.add('hidden');
            return;
        }

        issues.forEach(issue => {
            const issueEl = createIssueElement(issue);
            container.appendChild(issueEl);
        });

        initDraggableIssues();
        section.classList.remove('hidden');
    }

    function hideSearchResults() {
        const section = document.querySelector('.search-results-section');
        section.classList.add('hidden');
    }

    function filterSidebarIssues(query) {
        const issues = document.querySelectorAll('.issue-item');
        issues.forEach(issue => {
            const text = issue.textContent.toLowerCase();
            issue.style.display = (!query || text.includes(query)) ? '' : 'none';
        });

        // Also show/hide project headers based on visible issues
        const projectGroups = document.querySelectorAll('.project-group');
        projectGroups.forEach(group => {
            const visibleIssues = group.querySelectorAll('.issue-item[style=""]').length +
                                 group.querySelectorAll('.issue-item:not([style])').length;
            group.style.display = (!query || visibleIssues > 0) ? '' : 'none';
        });
    }

    // Hours Widget
    let hoursWidgetWeek = null; // Local date (YYYY-MM-DD) of the week shown

    // Monday-to-Monday week (local time) containing the given date, matching
    // the calendar's firstDay
    function getWeekRange(date) {
        const start = new Date(date.getFullYear(), date.getMonth(), date.getDate());
        start.setDate(start.getDate() - (start.getDay() + 6) % 7);
        const end = new Date(start);
        end.setDate(end.getDate() + 7);
        return { start, end };
    }

    function formatLocalDate(date) {
        return formatDateTimeLocal(date).slice(0, 10);
    }

    // Hours to at most 2 decimal places without trailing zeros, so 15-minute
    // entries add up exactly (0.25, 7.5, 32)
    function formatHours(hours) {
        return String(Math.round(hours * 100) / 100);
    }

    async function updateHoursWidget(viewDate) {
        // Totals cover the whole week, even when viewing a single day
        const week = getWeekRange(viewDate || new Date());
        hoursWidgetWeek = formatLocalDate(week.start);

        try {
            const response = await apiFetch(`/api/hours?start=${encodeURIComponent(calendar.formatIso(week.start))}&end=${encodeURIComponent(calendar.formatIso(week.end))}`);
            if (!response.ok) {
                throw new Error('Failed to fetch hours');
            }

            const data = await response.json();
            const widget = document.getElementById('hoursWidget');
            const loggedEl = widget.querySelector('.hours-logged');
            const targetEl = widget.querySelector('.hours-target');

            loggedEl.textContent = formatHours(data.hours_logged);
            targetEl.textContent = data.hours_target;

            widget.classList.remove('under', 'met');
            if (data.hours_logged >= data.hours_target) {
                widget.classList.add('met');
            } else {
                widget.classList.add('under');
            }
        } catch (error) {
            console.error('Error updating hours widget:', error);
        }
    }

    // Mobile Support
    let wasMobileView = null;

    // Runs on every resize event, but only acts when crossing the mobile
    // breakpoint - otherwise any resize would undo the user's view choice
    function checkMobileView() {
        const isMobile = isMobileView();
        if (isMobile === wasMobileView) {
            return;
        }
        wasMobileView = isMobile;

        const sidebar = document.getElementById('sidebar');

        if (isMobile) {
            sidebar.classList.remove('open');
            if (calendar && calendar.view.type !== 'timeGridDay') {
                calendar.changeView('timeGridDay');
            }
        } else {
            sidebar.classList.add('open');
            // Back on a wide screen: restore the view the user picked
            const preferredView = getPreferredView();
            if (calendar && calendar.view.type !== preferredView) {
                calendar.changeView(preferredView);
            }
        }
    }

    // Utility Functions
    function getDurationMinutes(event) {
        const start = event.start;
        const end = event.end || new Date(start.getTime() + 30 * 60000);
        return Math.round((end - start) / 60000);
    }

    function formatDateTimeLocal(date) {
        const year = date.getFullYear();
        const month = String(date.getMonth() + 1).padStart(2, '0');
        const day = String(date.getDate()).padStart(2, '0');
        const hours = String(date.getHours()).padStart(2, '0');
        const minutes = String(date.getMinutes()).padStart(2, '0');
        return `${year}-${month}-${day}T${hours}:${minutes}`;
    }
})();
