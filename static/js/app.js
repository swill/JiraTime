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

        calendar = new FullCalendar.Calendar(calendarEl, {
            initialView: window.innerWidth < 768 ? 'timeGridDay' : 'timeGridWeek',
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

            // Update hours widget when the visible date range changes
            datesSet: function(info) {
                updateHoursWidget(info.start);
            },

            // Event rendering
            eventContent: function(arg) {
                return {
                    html: `<div class="fc-event-title">${arg.event.title}</div>`
                };
            }
        });

        calendar.render();
    }

    // Fetch events from API
    async function fetchEvents(info, successCallback, failureCallback) {
        try {
            const start = info.startStr;
            const end = info.endStr;
            const response = await fetch(`/api/events?start=${encodeURIComponent(start)}&end=${encodeURIComponent(end)}`);

            if (!response.ok) {
                throw new Error('Failed to fetch events');
            }

            const events = await response.json();
            if (!events) {
                successCallback([]);
                return;
            }

            const calendarEvents = events.map(e => ({
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
                    subtaskTypeName: e.subtask_type_name
                }
            }));

            successCallback(calendarEvents);
        } catch (error) {
            console.error('Error fetching events:', error);
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
    // Projects with billable sub-tasks configured open the edit dialog so a
    // sub-task can be chosen; other projects keep the fast one-step logging.
    async function startCreateFlow(issueKey, issueSummary, start) {
        if (isViewOnlyMode()) {
            alert('Cannot create events while viewing another user\'s calendar');
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

        if (!options || !options.billable_types || options.billable_types.length === 0) {
            // No billable sub-tasks configured - create immediately
            addToRecentIssues(issueKey, issueSummary);
            await createEvent(issueKey, start, 30, '');
            return;
        }

        // A billable sub-task resolves to its parent; track the parent as recent
        addToRecentIssues(options.issue_key, options.issue_summary);
        openCreateDialog(options, start);
    }

    async function fetchSubtaskOptions(issueKey) {
        const response = await fetch(`/api/issues/${encodeURIComponent(issueKey)}/subtask-options`);
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

            const response = await fetch('/api/events', {
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
            const response = await fetch(`/api/events/${encodeURIComponent(eventId)}`, {
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
            const response = await fetch(`/api/events/${encodeURIComponent(eventId)}`, {
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
    let currentEditingEvent = null;
    let dialogSubtaskOptions = null; // Sub-task options for the open dialog (null = not loaded)
    let selectedSubtaskTypeId = null;
    let selectedSubtaskKey = null;

    function initDialog() {
        const dialog = document.getElementById('editDialog');
        const form = document.getElementById('editForm');
        const cancelBtn = document.getElementById('cancelBtn');
        const deleteBtn = document.getElementById('deleteBtn');

        // Handle dialog close (cancel, click outside, escape key)
        dialog.addEventListener('close', () => {
            // Remove focus/selection from calendar events
            if (currentEditingEvent && currentEditingEvent.el) {
                currentEditingEvent.el.blur();
                currentEditingEvent.el.classList.remove('fc-event-selected');
            }
            currentEditingEvent = null;

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
            if (confirm('Delete this time entry?')) {
                await deleteEvent(eventId);
                dialog.close();
            }
        });

        form.addEventListener('submit', async (e) => {
            e.preventDefault();

            const eventId = document.getElementById('eventId').value;
            const start = new Date(document.getElementById('eventStart').value);
            const duration = parseInt(document.getElementById('eventDuration').value, 10);
            const description = document.getElementById('eventDescription').value;
            const issueKey = document.getElementById('eventIssueKey').value;
            const subtaskTypeId = selectedSubtaskTypeId;
            const subtaskKey = selectedSubtaskKey;
            const subtaskOptions = dialogSubtaskOptions;

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

                    const response = await fetch(`/api/events/${encodeURIComponent(eventId)}`, {
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

        const eventId = `${event.extendedProps.issueKey}-${event.extendedProps.worklogId}`;
        document.getElementById('eventId').value = eventId;
        document.getElementById('eventIssue').value = event.title;
        document.getElementById('eventIssueKey').value = event.extendedProps.issueKey;
        document.getElementById('eventStart').value = formatDateTimeLocal(event.start);
        document.getElementById('eventDuration').value = getDurationMinutes(event);
        document.getElementById('eventDescription').value = event.extendedProps.description || '';

        // Load sub-task options; a worklog on a billable sub-task resolves to
        // its parent with the current association pre-checked (this also covers
        // time entered in Jira/JSM directly)
        dialogSubtaskOptions = null;
        showLoading();
        try {
            dialogSubtaskOptions = await fetchSubtaskOptions(event.extendedProps.issueKey);
        } catch (error) {
            // Dialog still works; the sub-task association just cannot be changed
            console.error('Error loading sub-task options:', error);
        } finally {
            hideLoading();
        }
        renderSubtaskSection(dialogSubtaskOptions);

        title.textContent = isViewOnlyMode() ? 'View Time Entry' : 'Edit Time Entry';
        deleteBtn.style.display = isViewOnlyMode() ? 'none' : 'inline-block';
        saveBtn.style.display = isViewOnlyMode() ? 'none' : 'inline-block';

        dialog.showModal();
    }

    // Open the dialog to create a new entry (used when the issue's project has
    // billable sub-tasks configured)
    function openCreateDialog(options, start) {
        const dialog = document.getElementById('editDialog');
        const title = document.getElementById('dialogTitle');
        const deleteBtn = document.getElementById('deleteBtn');
        const saveBtn = dialog.querySelector('button[type="submit"]');

        currentEditingEvent = null;
        dialogSubtaskOptions = options;

        document.getElementById('eventId').value = '';
        document.getElementById('eventIssue').value = options.issue_title;
        document.getElementById('eventIssueKey').value = options.issue_key;
        document.getElementById('eventStart').value = formatDateTimeLocal(start);
        document.getElementById('eventDuration').value = 30;
        document.getElementById('eventDescription').value = '';

        renderSubtaskSection(options);

        title.textContent = 'New Time Entry';
        deleteBtn.style.display = 'none';
        saveBtn.style.display = 'inline-block';

        dialog.showModal();
    }

    // Render the billable sub-task checkboxes. Type checkboxes act like
    // toggleable radios (one at a time); selecting a type reveals its existing
    // sub-tasks, where at most one can be picked.
    function renderSubtaskSection(options) {
        const section = document.getElementById('subtaskSection');
        const container = document.getElementById('subtaskTypes');
        container.innerHTML = '';
        selectedSubtaskTypeId = null;
        selectedSubtaskKey = null;

        if (!options || !options.billable_types || options.billable_types.length === 0) {
            section.classList.add('hidden');
            return;
        }

        const disabled = !!isViewOnlyMode();

        options.billable_types.forEach(type => {
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
            });
        });

        // Pre-check the current association for existing entries on a billable
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
            settingsDialog.showModal();
        });

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
            const timeRange = getCalendarTimeRange();
            calendar.setOption('slotMinTime', timeRange.slotMinTime);
            calendar.setOption('slotMaxTime', timeRange.slotMaxTime);
        });
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
                await fetch('/api/refresh', { method: 'POST' });
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
            const response = await fetch('/api/user');
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
        const container = document.querySelector('.app-container');

        // Check if currently impersonating
        if (currentUser.impersonating_id) {
            impersonationBar.classList.remove('hidden');
            impersonatingName.textContent = currentUser.impersonating_name;
            container.classList.add('view-only-mode');

            // Hide impersonate search when impersonating
            impersonateControls.classList.add('hidden');
        } else {
            impersonationBar.classList.add('hidden');
            container.classList.remove('view-only-mode');

            // Show impersonate controls for super users only
            if (currentUser.is_super_user) {
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
                    const response = await fetch(`/api/users/search?q=${encodeURIComponent(query)}`);
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
            item.innerHTML = `
                <img src="${user.avatar_url}" alt="">
                <span>${user.display_name}</span>
            `;
            item.addEventListener('click', () => startImpersonation(user));
            container.appendChild(item);
        });

        container.classList.remove('hidden');
    }

    async function startImpersonation(user) {
        showLoading();
        try {
            const response = await fetch('/api/impersonate', {
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
            const response = await fetch('/api/impersonate/stop', {
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

    function isViewOnlyMode() {
        return currentUser && currentUser.impersonating_id;
    }

    async function loadIssues() {
        try {
            const response = await fetch('/api/issues');
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
            const response = await fetch(`/api/issues/search?q=${encodeURIComponent(query)}`);
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
    async function updateHoursWidget(weekDate) {
        try {
            const url = weekDate
                ? `/api/hours?week=${weekDate.toISOString().slice(0, 10)}`
                : '/api/hours';
            const response = await fetch(url);
            if (!response.ok) {
                throw new Error('Failed to fetch hours');
            }

            const data = await response.json();
            const widget = document.getElementById('hoursWidget');
            const loggedEl = widget.querySelector('.hours-logged');
            const targetEl = widget.querySelector('.hours-target');

            loggedEl.textContent = data.hours_logged.toFixed(1);
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
    function checkMobileView() {
        const sidebar = document.getElementById('sidebar');
        const isMobile = window.innerWidth < 768;

        if (isMobile) {
            sidebar.classList.remove('open');
            if (calendar) {
                calendar.changeView('timeGridDay');
            }
        } else {
            sidebar.classList.add('open');
            if (calendar && calendar.view.type === 'timeGridDay') {
                calendar.changeView('timeGridWeek');
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
