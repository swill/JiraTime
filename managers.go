package main

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

const managersFile = "managers.json"

var (
	managers     = make(map[string]*Manager) // keyed by account_id
	managersLock sync.RWMutex
)

// initManagers loads the persisted manager list into memory at startup
func initManagers() {
	data, err := os.ReadFile(managersFile)
	if err != nil {
		if !os.IsNotExist(err) {
			logrus.Errorf("Failed to read %s: %v", managersFile, err)
		}
		return
	}

	var stored StoredManagers
	if err := json.Unmarshal(data, &stored); err != nil {
		logrus.Errorf("Failed to unmarshal %s: %v", managersFile, err)
		return
	}

	managersLock.Lock()
	for i := range stored.Managers {
		m := stored.Managers[i]
		// Entries saved before roles existed are view-only managers
		if m.Role == "" {
			m.Role = RoleManager
		}
		managers[m.AccountID] = &m
	}
	managersLock.Unlock()

	logrus.Infof("Loaded %d managers from %s", len(stored.Managers), managersFile)
}

// saveManagers persists the in-memory manager list to disk.
// Callers must hold managersLock (read or write).
func saveManagers() {
	stored := StoredManagers{Managers: make([]Manager, 0, len(managers))}
	for _, m := range managers {
		stored.Managers = append(stored.Managers, *m)
	}
	sort.Slice(stored.Managers, func(i, j int) bool {
		return stored.Managers[i].DisplayName < stored.Managers[j].DisplayName
	})

	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		logrus.Errorf("Failed to marshal managers: %v", err)
		return
	}

	if err := writeFileAtomic(managersFile, data, 0600); err != nil {
		logrus.Errorf("Failed to save managers: %v", err)
	}
}

// IsManager checks if the given account ID is an app-managed manager
func IsManager(accountID string) bool {
	managersLock.RLock()
	defer managersLock.RUnlock()
	_, ok := managers[accountID]
	return ok
}

// IsSDM checks if the given account ID is on the manager list with the SDM role
func IsSDM(accountID string) bool {
	managersLock.RLock()
	defer managersLock.RUnlock()
	m, ok := managers[accountID]
	return ok && m.Role == RoleSDM
}

// CanImpersonate checks if the given account ID may view other users'
// calendars: super users (site admins from config) and app-managed managers
func CanImpersonate(accountID string) bool {
	return IsSuperUser(accountID) || IsManager(accountID)
}

// CanEditOthers checks if the given account ID may change the time on a
// calendar it is viewing as someone else: super users and SDMs. Checked on
// every write, so a role change takes effect on the next request.
func CanEditOthers(accountID string) bool {
	return IsSuperUser(accountID) || IsSDM(accountID)
}

// ListManagers returns all managers sorted by display name
func ListManagers() []Manager {
	managersLock.RLock()
	defer managersLock.RUnlock()

	list := make([]Manager, 0, len(managers))
	for _, m := range managers {
		list = append(list, *m)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].DisplayName < list[j].DisplayName
	})
	return list
}

// handleManagersRoute handles GET (list) and POST (create/update) on /api/managers.
// Only super users can manage managers.
func handleManagersRoute(w http.ResponseWriter, r *http.Request) {
	session := getSessionFromRequest(r)
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if !IsSuperUser(session.AccountID) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, ListManagers())
	case http.MethodPost:
		handleAddManager(w, r, session)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleAddManager creates a manager, or updates the stored name/avatar/role if
// the account is already a manager. The caller is already verified as a super
// user.
func handleAddManager(w http.ResponseWriter, r *http.Request, session *UserSession) {
	var req ManagerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	req.AccountID = strings.TrimSpace(req.AccountID)
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if req.AccountID == "" || req.DisplayName == "" {
		http.Error(w, "account_id and display_name are required", http.StatusBadRequest)
		return
	}
	if req.Role != "" && req.Role != RoleManager && req.Role != RoleSDM {
		http.Error(w, "role must be \"manager\" or \"sdm\"", http.StatusBadRequest)
		return
	}

	manager := &Manager{
		AccountID:   req.AccountID,
		DisplayName: req.DisplayName,
		AvatarURL:   req.AvatarURL,
		Role:        req.Role,
		AddedBy:     session.AccountID,
		AddedAt:     time.Now().UTC(),
	}

	managersLock.Lock()
	previousRole := ""
	if existing, ok := managers[req.AccountID]; ok {
		// Preserve the original audit trail on update
		manager.AddedBy = existing.AddedBy
		manager.AddedAt = existing.AddedAt
		previousRole = existing.Role
	}
	// Re-adding someone from the search box sends no role: keep theirs
	if manager.Role == "" {
		manager.Role = previousRole
	}
	if manager.Role == "" {
		manager.Role = RoleManager
	}
	managers[req.AccountID] = manager
	saveManagers()
	managersLock.Unlock()

	if previousRole != "" && previousRole != manager.Role {
		logrus.Infof("Manager %s role changed from %s to %s by super user %s", req.AccountID, previousRole, manager.Role, session.AccountID)
	} else {
		logrus.Infof("Manager %s (%s) added/updated as %s by super user %s", req.DisplayName, req.AccountID, manager.Role, session.AccountID)
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, manager)
}

// handleManagerByIDRoute handles DELETE /api/managers/{account_id}.
// Only super users can manage managers.
func handleManagerByIDRoute(w http.ResponseWriter, r *http.Request) {
	session := getSessionFromRequest(r)
	if session == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if !IsSuperUser(session.AccountID) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	accountID := strings.TrimPrefix(r.URL.Path, "/api/managers/")
	if accountID == "" {
		http.Error(w, "Manager account ID required", http.StatusBadRequest)
		return
	}

	managersLock.Lock()
	_, ok := managers[accountID]
	if ok {
		delete(managers, accountID)
		saveManagers()
	}
	managersLock.Unlock()

	if !ok {
		http.Error(w, "Manager not found", http.StatusNotFound)
		return
	}

	logrus.Infof("Manager %s removed by super user %s", accountID, session.AccountID)

	w.WriteHeader(http.StatusNoContent)
}
