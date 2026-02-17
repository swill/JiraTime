package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"golang.org/x/oauth2"
)

const (
	jiraAuthURL     = "https://auth.atlassian.com/authorize"
	jiraTokenURL    = "https://auth.atlassian.com/oauth/token"
	jiraResourceURL = "https://api.atlassian.com/oauth/token/accessible-resources"
	tokenFile       = "tokens.json"
	sessionCookie   = "jiratime_session"
)

var (
	sessions     = make(map[string]*UserSession)
	sessionsLock sync.RWMutex
)

func getOAuthConfig() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     viper.GetString("JIRA_CLIENT_ID"),
		ClientSecret: viper.GetString("JIRA_CLIENT_SECRET"),
		Endpoint: oauth2.Endpoint{
			AuthURL:  jiraAuthURL,
			TokenURL: jiraTokenURL,
		},
		RedirectURL: viper.GetString("BASE_URL") + "/oauth/callback",
		Scopes: []string{
			"read:me",
			"read:account",
			"read:jira-user",
			"read:jira-work",
			"write:jira-work",
			"read:servicedesk-request",
			"write:servicedesk-request",
		},
	}
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	config := getOAuthConfig()
	state := generateState()
	setStateCookie(w, state)

	url := config.AuthCodeURL(state, oauth2.SetAuthURLParam("audience", "api.atlassian.com"))
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

func handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	// Verify state
	state := r.URL.Query().Get("state")
	expectedState := getStateCookie(r)
	if state == "" || state != expectedState {
		http.Error(w, "Invalid state parameter", http.StatusBadRequest)
		return
	}
	clearStateCookie(w)

	// Exchange code for token
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "Missing code parameter", http.StatusBadRequest)
		return
	}

	config := getOAuthConfig()
	token, err := config.Exchange(r.Context(), code)
	if err != nil {
		logrus.Errorf("Token exchange failed: %v", err)
		http.Error(w, "Authentication failed", http.StatusInternalServerError)
		return
	}

	// Get accessible resources (cloud ID and site URL)
	cloudID, siteURL, err := getAccessibleResource(token.AccessToken)
	if err != nil {
		logrus.Errorf("Failed to get accessible resources: %v", err)
		http.Error(w, "Failed to get Jira site", http.StatusInternalServerError)
		return
	}

	// Get current user info
	client := NewJiraClient(token, cloudID)
	user, err := client.GetCurrentUser(r.Context())
	if err != nil {
		logrus.Errorf("Failed to get current user: %v", err)
		http.Error(w, "Failed to get user info", http.StatusInternalServerError)
		return
	}

	// Create session
	session := &UserSession{
		AccountID:   user.AccountID,
		CloudID:     cloudID,
		SiteURL:     siteURL,
		DisplayName: user.DisplayName,
		Email:       user.EmailAddress,
		AvatarURL:   user.AvatarURLs.Large,
		Token:       token,
	}

	sessionID := createSession(session)
	setSessionCookie(w, sessionID)

	// Save token for persistence
	saveToken(user.AccountID, token)

	http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	sessionID := getSessionCookie(r)
	if sessionID != "" {
		deleteSession(sessionID)
	}
	clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
}

func requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session := getSessionFromRequest(r)
		if session == nil {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
			} else {
				http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
			}
			return
		}

		// Refresh token if needed
		if session.Token.Expiry.Before(time.Now().Add(5 * time.Minute)) {
			newToken, err := refreshToken(session.Token)
			if err != nil {
				logrus.Errorf("Failed to refresh token: %v", err)
				deleteSession(getSessionCookie(r))
				clearSessionCookie(w)
				if strings.HasPrefix(r.URL.Path, "/api/") {
					http.Error(w, "Session expired", http.StatusUnauthorized)
				} else {
					http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
				}
				return
			}
			session.Token = newToken
			saveToken(session.AccountID, newToken)
		}

		next(w, r)
	}
}

func getSessionFromRequest(r *http.Request) *UserSession {
	sessionID := getSessionCookie(r)
	if sessionID == "" {
		return nil
	}

	sessionsLock.RLock()
	session := sessions[sessionID]
	sessionsLock.RUnlock()

	return session
}

func createSession(session *UserSession) string {
	sessionID := generateSessionID()
	sessionsLock.Lock()
	sessions[sessionID] = session
	sessionsLock.Unlock()
	return sessionID
}

func deleteSession(sessionID string) {
	sessionsLock.Lock()
	delete(sessions, sessionID)
	sessionsLock.Unlock()
}

func generateSessionID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.URLEncoding.EncodeToString(b)
}

func generateState() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.URLEncoding.EncodeToString(b)
}

func setSessionCookie(w http.ResponseWriter, sessionID string) {
	sig := signSession(sessionID)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    sessionID + "." + sig,
		Path:     "/",
		HttpOnly: true,
		Secure:   strings.HasPrefix(viper.GetString("BASE_URL"), "https"),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400 * 30, // 30 days
	})
}

func getSessionCookie(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}

	parts := strings.SplitN(cookie.Value, ".", 2)
	if len(parts) != 2 {
		return ""
	}

	sessionID, sig := parts[0], parts[1]
	if signSession(sessionID) != sig {
		return ""
	}

	return sessionID
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

func setStateCookie(w http.ResponseWriter, state string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "oauth_state",
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   strings.HasPrefix(viper.GetString("BASE_URL"), "https"),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600, // 10 minutes
	})
}

func getStateCookie(r *http.Request) string {
	cookie, err := r.Cookie("oauth_state")
	if err != nil {
		return ""
	}
	return cookie.Value
}

func clearStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "oauth_state",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

func signSession(sessionID string) string {
	h := hmac.New(sha256.New, []byte(viper.GetString("SESSION_SECRET")))
	h.Write([]byte(sessionID))
	return base64.URLEncoding.EncodeToString(h.Sum(nil))
}

func getAccessibleResource(accessToken string) (string, string, error) {
	req, err := http.NewRequest("GET", jiraResourceURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("accessible resources request failed: %s", body)
	}

	var resources []JiraAccessibleResource
	if err := json.NewDecoder(resp.Body).Decode(&resources); err != nil {
		return "", "", err
	}

	if len(resources) == 0 {
		return "", "", fmt.Errorf("no accessible Jira sites found")
	}

	// Use the first accessible resource - return both cloudId and site URL
	return resources[0].ID, resources[0].URL, nil
}

func refreshToken(token *oauth2.Token) (*oauth2.Token, error) {
	config := getOAuthConfig()
	src := config.TokenSource(nil, token)
	newToken, err := src.Token()
	if err != nil {
		return nil, err
	}
	return newToken, nil
}

func saveToken(accountID string, token *oauth2.Token) {
	tokens := loadTokens()
	tokens.Tokens[accountID] = token

	data, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		logrus.Errorf("Failed to marshal tokens: %v", err)
		return
	}

	if err := os.WriteFile(tokenFile, data, 0600); err != nil {
		logrus.Errorf("Failed to save tokens: %v", err)
	}
}

func loadTokens() *StoredTokens {
	tokens := &StoredTokens{
		Tokens: make(map[string]*oauth2.Token),
	}

	data, err := os.ReadFile(tokenFile)
	if err != nil {
		return tokens
	}

	if err := json.Unmarshal(data, tokens); err != nil {
		logrus.Errorf("Failed to unmarshal tokens: %v", err)
		return &StoredTokens{Tokens: make(map[string]*oauth2.Token)}
	}

	return tokens
}
