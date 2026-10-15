package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	accountIDCookie = "jiratime_account"

	// Auth cookies are renewed on every authenticated request, so only users
	// idle this long have to sign in again. Matches Atlassian's 90-day refresh
	// token inactivity expiry - past that the saved token is dead anyway.
	authCookieMaxAge = 90 * 24 * 60 * 60

	// Access tokens are refreshed when this close to expiry
	tokenRefreshMargin = 5 * time.Minute
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
			"offline_access",
		},
	}
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	// Check if user already has a valid session
	session := getSessionFromRequest(r)
	if session != nil {
		http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
		return
	}

	// Try to restore session from saved tokens (e.g., after logout)
	accountID := getAccountIDCookie(r)
	if accountID != "" {
		sessionID := generateSessionID()
		session := tryRestoreSession(accountID, sessionID)
		if session != nil {
			setSessionCookie(w, sessionID)
			http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
			return
		}
	}

	config := getOAuthConfig()
	state := generateState()
	setStateCookie(w, state)

	url := config.AuthCodeURL(state, oauth2.SetAuthURLParam("audience", "api.atlassian.com"))
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

func handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	// Check for error response
	if errCode := r.URL.Query().Get("error"); errCode != "" {
		errDesc := r.URL.Query().Get("error_description")
		logrus.Errorf("OAuth error: %s - %s", errCode, errDesc)
		http.Error(w, "Authentication failed: "+errDesc, http.StatusBadRequest)
		return
	}

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
	}

	// Save token and session data for persistence. The new token replaces the
	// account's previous one for all of its sessions.
	accountTokens.set(user.AccountID, token)
	saveSessionData(user.AccountID, session)

	sessionID := createSession(session)
	setSessionCookie(w, sessionID)
	setAccountIDCookie(w, user.AccountID)

	http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	sessionID := getSessionCookie(r)
	if sessionID != "" {
		deleteSession(sessionID)
	}
	clearSessionCookie(w)
	// Keep accountID cookie so we can restore session on next login without OAuth
	// The saved tokens remain valid and can be used to restore the session
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

		// Revoke in-flight impersonation if the user's manager/super-user
		// status was removed since it started (fail closed)
		if session.ImpersonatingID != "" && !CanImpersonate(session.AccountID) {
			logrus.Infof("Clearing impersonation for %s: no longer a manager or super user", session.AccountID)
			session.ImpersonatingID = ""
			session.ImpersonatingName = ""
			saveSession(session)
		}

		// Make sure the account's token is current, refreshing it if needed
		if _, err := accountTokens.validToken(session.AccountID); err != nil {
			if !isTokenRejected(err) {
				// Network error or Atlassian outage: keep the session so the user
				// is not sent back through the OAuth consent screen
				logrus.Warnf("Token refresh for %s failed, keeping session: %v", session.AccountID, err)
				http.Error(w, "Unable to reach Jira, please try again shortly", http.StatusServiceUnavailable)
				return
			}

			logrus.Errorf("Refresh token for %s was rejected, sign-in required: %v", session.AccountID, err)
			deleteSession(getSessionCookie(r))
			clearSessionCookie(w)
			clearAccountIDCookie(w)
			if strings.HasPrefix(r.URL.Path, "/api/") {
				http.Error(w, "Session expired", http.StatusUnauthorized)
			} else {
				http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
			}
			return
		}

		// Sliding expiry: keep active users signed in
		setSessionCookie(w, getSessionCookie(r))
		setAccountIDCookie(w, session.AccountID)

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

	if session != nil {
		return session
	}

	// Session not in memory (server restart?) - try to restore from saved data
	accountID := getAccountIDCookie(r)
	if accountID == "" {
		return nil
	}

	session = tryRestoreSession(accountID, sessionID)
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
		MaxAge:   authCookieMaxAge,
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

// refreshToken exchanges a refresh token for a new token pair. Only the refresh
// token is passed to the oauth2 library, which would otherwise hand back the
// old access token until it had fully expired instead of refreshing.
func refreshToken(ctx context.Context, token *oauth2.Token) (*oauth2.Token, error) {
	config := getOAuthConfig()
	return config.TokenSource(ctx, &oauth2.Token{RefreshToken: token.RefreshToken}).Token()
}

// tokenStore holds one OAuth token per account, shared by every session
// (browser/device) of that account and persisted to tokens.json. Atlassian
// rotates refresh tokens - each refresh invalidates the previous refresh token
// once a short reuse window passes - so per-session copies go stale and a
// refresh with one fails, forcing the user back through the OAuth consent
// screen. Refreshes are serialized per account for the same reason.
type tokenStore struct {
	path    string                                                      // persistence file
	refresh func(context.Context, *oauth2.Token) (*oauth2.Token, error) // token exchange

	mu           sync.Mutex
	tokens       map[string]*oauth2.Token // keyed by account_id
	refreshLocks map[string]*sync.Mutex   // keyed by account_id
}

func newTokenStore(path string) *tokenStore {
	return &tokenStore{
		path:         path,
		refresh:      refreshToken,
		tokens:       make(map[string]*oauth2.Token),
		refreshLocks: make(map[string]*sync.Mutex),
	}
}

var accountTokens = newTokenStore(tokenFile)

// errNoToken means there is no usable saved token, so the user must sign in
var errNoToken = errors.New("no saved refresh token for account")

// initTokens loads the persisted tokens into memory at startup
func initTokens() {
	accountTokens.load()
}

func (s *tokenStore) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			logrus.Errorf("Failed to read %s: %v", s.path, err)
		}
		return
	}

	var stored StoredTokens
	if err := json.Unmarshal(data, &stored); err != nil {
		logrus.Errorf("Failed to unmarshal %s: %v", s.path, err)
		return
	}

	s.mu.Lock()
	for accountID, token := range stored.Tokens {
		s.tokens[accountID] = token
	}
	s.mu.Unlock()
}

func (s *tokenStore) get(accountID string) *oauth2.Token {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens[accountID]
}

// set stores an account's token and persists all tokens to disk
func (s *tokenStore) set(accountID string, token *oauth2.Token) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.tokens[accountID] = token

	data, err := json.MarshalIndent(StoredTokens{Tokens: s.tokens}, "", "  ")
	if err != nil {
		logrus.Errorf("Failed to marshal tokens: %v", err)
		return
	}

	if err := writeFileAtomic(s.path, data, 0600); err != nil {
		logrus.Errorf("Failed to save tokens: %v", err)
	}
}

func (s *tokenStore) refreshLock(accountID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()

	lock := s.refreshLocks[accountID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.refreshLocks[accountID] = lock
	}
	return lock
}

// validToken returns the account's token, refreshing it first when it is close
// to expiry. Only one refresh runs per account at a time; callers that waited
// on it pick up the token it stored.
func (s *tokenStore) validToken(accountID string) (*oauth2.Token, error) {
	lock := s.refreshLock(accountID)
	lock.Lock()
	defer lock.Unlock()

	token := s.get(accountID)
	if token == nil {
		return nil, errNoToken
	}
	if !token.Expiry.Before(time.Now().Add(tokenRefreshMargin)) {
		return token, nil
	}
	if token.RefreshToken == "" {
		return nil, errNoToken
	}

	// Detached from the request context: if the browser gives up mid-refresh,
	// Atlassian may already have rotated the refresh token, and dropping the
	// response would strand the account on the invalidated one
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	newToken, err := s.refresh(ctx, token)
	if err != nil {
		return nil, err
	}
	s.set(accountID, newToken)

	return newToken, nil
}

// isTokenRejected reports whether a refresh failed because Atlassian rejected
// the refresh token (expired, revoked or already rotated) - the only case that
// needs a new OAuth sign-in. Network errors and Atlassian outages are
// transient and must not send the user back to the consent screen.
func isTokenRejected(err error) bool {
	if errors.Is(err, errNoToken) {
		return true
	}

	var retrieveErr *oauth2.RetrieveError
	if errors.As(err, &retrieveErr) && retrieveErr.Response != nil {
		status := retrieveErr.Response.StatusCode
		return status >= 400 && status < 500 && status != http.StatusTooManyRequests
	}

	return false
}

// jiraClientFor returns a Jira client using the session account's current token
func jiraClientFor(session *UserSession) *JiraClient {
	return NewJiraClient(accountTokens.get(session.AccountID), session.CloudID)
}

// viewClientFor returns the Jira client for reading the calendar a session is
// looking at. Jira filters results by the caller's own permissions, so while
// impersonating it uses the viewed user's saved token: managers then see the
// same entries that user sees, including on projects the manager cannot open.
// Only use it for read-only queries scoped to the viewed user's own work
// (their worklogs and Active Issues) - never for search, arbitrary issue
// lookups or writes. limited is true when the viewed user has no usable token
// and the viewer's own access was used instead, which can leave entries out.
func viewClientFor(session *UserSession) (client *JiraClient, limited bool) {
	if !isImpersonating(session) {
		return jiraClientFor(session), false
	}

	token, err := accountTokens.validToken(session.ImpersonatingID)
	if err != nil {
		if !errors.Is(err, errNoToken) {
			logrus.Warnf("Could not use %s's Jira access for %s's view, falling back to the viewer's: %v", session.ImpersonatingID, session.AccountID, err)
		}
		return jiraClientFor(session), true
	}
	return NewJiraClient(token, session.CloudID), false
}

// writeFileAtomic replaces a file in one step, so a crash mid-write cannot
// leave a truncated file behind (a corrupt tokens.json would sign everyone out)
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

const sessionDataFile = "sessions.json"

type StoredSessions struct {
	Sessions map[string]*UserSession `json:"sessions"`
}

func setAccountIDCookie(w http.ResponseWriter, accountID string) {
	sig := signSession(accountID)
	http.SetCookie(w, &http.Cookie{
		Name:     accountIDCookie,
		Value:    accountID + "." + sig,
		Path:     "/",
		HttpOnly: true,
		Secure:   strings.HasPrefix(viper.GetString("BASE_URL"), "https"),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   authCookieMaxAge,
	})
}

func getAccountIDCookie(r *http.Request) string {
	cookie, err := r.Cookie(accountIDCookie)
	if err != nil {
		return ""
	}

	parts := strings.SplitN(cookie.Value, ".", 2)
	if len(parts) != 2 {
		return ""
	}

	accountID, sig := parts[0], parts[1]
	if signSession(accountID) != sig {
		return ""
	}

	return accountID
}

func clearAccountIDCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     accountIDCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

func saveSessionData(accountID string, session *UserSession) {
	stored := loadSessionData()
	stored.Sessions[accountID] = session

	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		logrus.Errorf("Failed to marshal session data: %v", err)
		return
	}

	if err := writeFileAtomic(sessionDataFile, data, 0600); err != nil {
		logrus.Errorf("Failed to save session data: %v", err)
	}
}

func loadSessionData() *StoredSessions {
	stored := &StoredSessions{
		Sessions: make(map[string]*UserSession),
	}

	data, err := os.ReadFile(sessionDataFile)
	if err != nil {
		return stored
	}

	if err := json.Unmarshal(data, stored); err != nil {
		logrus.Errorf("Failed to unmarshal session data: %v", err)
		return &StoredSessions{Sessions: make(map[string]*UserSession)}
	}

	return stored
}

func tryRestoreSession(accountID, sessionID string) *UserSession {
	// Load saved session data
	stored := loadSessionData()
	session, ok := stored.Sessions[accountID]
	if !ok {
		return nil
	}

	// A saved token is needed to act for the user (it is refreshed on use)
	if accountTokens.get(accountID) == nil {
		return nil
	}

	// Re-register in memory
	sessionsLock.Lock()
	sessions[sessionID] = session
	sessionsLock.Unlock()

	logrus.Infof("Restored session for account %s", accountID)
	return session
}

// saveSession persists the session data to disk (the in-memory map is already updated via pointer)
func saveSession(session *UserSession) {
	saveSessionData(session.AccountID, session)
}
