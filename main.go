package main

import (
	"crypto/tls"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"golang.org/x/crypto/acme/autocert"
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
	mux.HandleFunc("/api/issues/", requireAuth(handleIssueByKey)) // For /api/issues/{key}/custom-fields
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

	if port == 443 {
		// HTTPS mode with Let's Encrypt
		domain := viper.GetString("BASE_URL")
		// Strip protocol prefix if present
		domain = strings.TrimPrefix(domain, "https://")
		domain = strings.TrimPrefix(domain, "http://")

		certManager := autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(domain),
			Cache:      autocert.DirCache("certs"),
		}

		server := &http.Server{
			Addr:    ":https",
			Handler: mux,
			TLSConfig: &tls.Config{
				GetCertificate: certManager.GetCertificate,
			},
		}

		// HTTP server for ACME challenges and redirect to HTTPS
		go func() {
			logrus.Info("Starting HTTP server on :80 for ACME challenges")
			if err := http.ListenAndServe(":http", certManager.HTTPHandler(nil)); err != nil {
				logrus.Errorf("HTTP server failed: %v", err)
			}
		}()

		logrus.Infof("Starting JiraTime server on :443 with Let's Encrypt for %s", domain)
		if err := server.ListenAndServeTLS("", ""); err != nil {
			logrus.Fatalf("Server failed: %v", err)
		}
	} else {
		// HTTP mode
		addr := fmt.Sprintf(":%d", port)
		logrus.Infof("Starting JiraTime server on %s", addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			logrus.Fatalf("Server failed: %v", err)
		}
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
	path := strings.TrimPrefix(r.URL.Path, "/api/events/")
	if path == "" {
		http.Error(w, "Event ID required", http.StatusBadRequest)
		return
	}

	// Check if this is a contributions request: /api/events/{id}/contributions
	if strings.HasSuffix(path, "/contributions") {
		if r.Method == http.MethodGet {
			handleGetEventContributions(w, r)
			return
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
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

func handleIssueByKey(w http.ResponseWriter, r *http.Request) {
	// Check for /api/issues/{key}/custom-fields
	path := strings.TrimPrefix(r.URL.Path, "/api/issues/")
	if path == "" {
		http.Error(w, "Issue key required", http.StatusBadRequest)
		return
	}

	if strings.HasSuffix(path, "/custom-fields") {
		if r.Method == http.MethodGet {
			handleGetIssueCustomFields(w, r)
			return
		}
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	http.Error(w, "Not found", http.StatusNotFound)
}
