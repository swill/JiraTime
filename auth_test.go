package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/viper"
	"golang.org/x/oauth2"
)

func testTokenStore(t *testing.T, refresh func(context.Context, *oauth2.Token) (*oauth2.Token, error)) *tokenStore {
	t.Helper()
	store := newTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	store.refresh = refresh
	return store
}

func rejectedRefresh(status int) error {
	return &oauth2.RetrieveError{Response: &http.Response{StatusCode: status}, ErrorCode: "invalid_grant"}
}

func TestValidTokenRefreshesOnceForConcurrentRequests(t *testing.T) {
	var refreshes atomic.Int32
	store := testTokenStore(t, func(ctx context.Context, old *oauth2.Token) (*oauth2.Token, error) {
		refreshes.Add(1)
		time.Sleep(50 * time.Millisecond) // widen the race window
		return &oauth2.Token{AccessToken: "new", RefreshToken: "rotated", Expiry: time.Now().Add(time.Hour)}, nil
	})
	store.set("acct", &oauth2.Token{AccessToken: "old", RefreshToken: "original", Expiry: time.Now().Add(time.Minute)})

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := store.validToken("acct")
			if err != nil || token.AccessToken != "new" {
				t.Errorf("validToken = %v, %v; want the refreshed token", token, err)
			}
		}()
	}
	wg.Wait()

	if n := refreshes.Load(); n != 1 {
		t.Errorf("refreshed %d times, want 1 (a second refresh would reuse the rotated-out refresh token)", n)
	}

	// The rotated refresh token is persisted for restarts
	reloaded := newTokenStore(store.path)
	reloaded.load()
	if got := reloaded.get("acct"); got == nil || got.RefreshToken != "rotated" {
		t.Errorf("persisted token = %+v, want refresh token \"rotated\"", got)
	}
}

func TestValidTokenSkipsRefreshWhenFresh(t *testing.T) {
	store := testTokenStore(t, func(ctx context.Context, old *oauth2.Token) (*oauth2.Token, error) {
		t.Error("refresh called for a token that is not near expiry")
		return nil, errors.New("unexpected")
	})
	store.set("acct", &oauth2.Token{AccessToken: "current", RefreshToken: "r", Expiry: time.Now().Add(time.Hour)})

	token, err := store.validToken("acct")
	if err != nil || token.AccessToken != "current" {
		t.Errorf("validToken = %v, %v; want the current token", token, err)
	}
}

func TestValidTokenWithoutUsableToken(t *testing.T) {
	store := testTokenStore(t, func(ctx context.Context, old *oauth2.Token) (*oauth2.Token, error) {
		t.Error("refresh called without a refresh token")
		return nil, errors.New("unexpected")
	})

	if _, err := store.validToken("missing"); !errors.Is(err, errNoToken) {
		t.Errorf("missing account: err = %v, want errNoToken", err)
	}

	store.set("expired", &oauth2.Token{AccessToken: "a", Expiry: time.Now().Add(-time.Minute)})
	if _, err := store.validToken("expired"); !errors.Is(err, errNoToken) {
		t.Errorf("expired without refresh token: err = %v, want errNoToken", err)
	}
}

func TestIsTokenRejected(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"invalid grant (403)", rejectedRefresh(http.StatusForbidden), true},
		{"bad request (400)", rejectedRefresh(http.StatusBadRequest), true},
		{"wrapped rejection", fmt.Errorf("refresh: %w", rejectedRefresh(http.StatusUnauthorized)), true},
		{"no saved token", errNoToken, true},
		{"rate limited (429)", rejectedRefresh(http.StatusTooManyRequests), false},
		{"Atlassian outage (503)", rejectedRefresh(http.StatusServiceUnavailable), false},
		{"network error", errors.New("dial tcp: connection refused"), false},
		{"request timeout", context.DeadlineExceeded, false},
	}
	for _, c := range cases {
		if got := isTokenRejected(c.err); got != c.want {
			t.Errorf("%s: isTokenRejected = %v, want %v", c.name, got, c.want)
		}
	}
}

// authRequest runs requireAuth for a signed-in session whose token refresh
// behaves as given, returning the response and whether the handler ran
func authRequest(t *testing.T, path string, token *oauth2.Token, refresh func(context.Context, *oauth2.Token) (*oauth2.Token, error)) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	viper.Set("SESSION_SECRET", "test-secret-that-is-at-least-32-characters")
	viper.Set("BASE_URL", "https://jiratime.example")

	store := testTokenStore(t, refresh)
	store.set("acct", token)
	original := accountTokens
	accountTokens = store
	t.Cleanup(func() { accountTokens = original })

	sessionID := createSession(&UserSession{AccountID: "acct", CloudID: "cloud"})
	t.Cleanup(func() { deleteSession(sessionID) })

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: sessionID + "." + signSession(sessionID)})
	req.AddCookie(&http.Cookie{Name: accountIDCookie, Value: "acct." + signSession("acct")})

	called := false
	rec := httptest.NewRecorder()
	requireAuth(func(w http.ResponseWriter, r *http.Request) { called = true })(rec, req)

	if called || rec.Code != http.StatusOK {
		return rec, called
	}
	// Session must still exist after a successful request
	if getSessionFromRequest(req) == nil {
		t.Error("session missing after successful request")
	}
	return rec, called
}

func cookieMaxAges(rec *httptest.ResponseRecorder) map[string]int {
	ages := map[string]int{}
	for _, c := range rec.Result().Cookies() {
		ages[c.Name] = c.MaxAge
	}
	return ages
}

func TestRequireAuthRenewsCookies(t *testing.T) {
	rec, called := authRequest(t, "/api/events", &oauth2.Token{AccessToken: "a", RefreshToken: "r", Expiry: time.Now().Add(time.Hour)}, nil)
	if !called {
		t.Fatalf("handler not called, status %d", rec.Code)
	}
	ages := cookieMaxAges(rec)
	if ages[sessionCookie] != authCookieMaxAge || ages[accountIDCookie] != authCookieMaxAge {
		t.Errorf("cookie max ages = %v, want both renewed to %d", ages, authCookieMaxAge)
	}
}

func TestRequireAuthKeepsSessionOnTransientRefreshFailure(t *testing.T) {
	expiring := &oauth2.Token{AccessToken: "a", RefreshToken: "r", Expiry: time.Now().Add(time.Minute)}
	rec, called := authRequest(t, "/api/events", expiring, func(ctx context.Context, old *oauth2.Token) (*oauth2.Token, error) {
		return nil, errors.New("dial tcp: i/o timeout")
	})

	if called {
		t.Error("handler ran without a valid token")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	for name, age := range cookieMaxAges(rec) {
		if age < 0 {
			t.Errorf("cookie %s cleared on a transient failure - the user would be sent back through OAuth consent", name)
		}
	}
}

func TestRequireAuthSignsOutWhenRefreshTokenRejected(t *testing.T) {
	expiring := &oauth2.Token{AccessToken: "a", RefreshToken: "r", Expiry: time.Now().Add(time.Minute)}
	rec, called := authRequest(t, "/api/events", expiring, func(ctx context.Context, old *oauth2.Token) (*oauth2.Token, error) {
		return nil, rejectedRefresh(http.StatusForbidden)
	})

	if called {
		t.Error("handler ran with a rejected token")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	ages := cookieMaxAges(rec)
	if ages[sessionCookie] >= 0 || ages[accountIDCookie] >= 0 {
		t.Errorf("cookie max ages = %v, want both cleared", ages)
	}
}
