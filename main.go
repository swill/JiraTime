package main

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

//go:embed static
var staticFiles embed.FS

func main() {
	initConfig()

	mux := http.NewServeMux()

	// Auth routes
	mux.HandleFunc("/login", handleLogin)
	mux.HandleFunc("/oauth/callback", handleOAuthCallback)
	mux.HandleFunc("/logout", handleLogout)

	// API routes (require auth)
	mux.HandleFunc("/api/events", requireAuth(handleEvents))
	mux.HandleFunc("/api/events/", requireAuth(handleEventByID))
	mux.HandleFunc("/api/issues", requireAuth(handleGetIssues))
	mux.HandleFunc("/api/issues/search", requireAuth(handleSearchIssues))
	mux.HandleFunc("/api/hours", requireAuth(handleGetHours))
	mux.HandleFunc("/api/refresh", requireAuth(handleRefresh))
	mux.HandleFunc("/api/user", requireAuth(handleGetUser))

	// Static files and main page
	staticFS, _ := fs.Sub(staticFiles, "static")
	fileServer := http.FileServer(http.FS(staticFS))

	mux.HandleFunc("/", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			// Serve index.html for root path
			http.ServeFileFS(w, r, staticFS, "index.html")
			return
		}
		// Strip leading slash and serve static files
		fileServer.ServeHTTP(w, r)
	}))

	port := viper.GetInt("PORT")
	addr := fmt.Sprintf(":%d", port)

	logrus.Infof("Starting JiraTime server on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		logrus.Fatalf("Server failed: %v", err)
	}
}

func handleEvents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		handleGetEvents(w, r)
	case http.MethodPost:
		handleCreateEvent(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleEventByID(w http.ResponseWriter, r *http.Request) {
	// Check if this is an actual event ID request (not just /api/events/)
	eventID := strings.TrimPrefix(r.URL.Path, "/api/events/")
	if eventID == "" {
		http.Error(w, "Event ID required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodPut:
		handleUpdateEvent(w, r)
	case http.MethodDelete:
		handleDeleteEvent(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
