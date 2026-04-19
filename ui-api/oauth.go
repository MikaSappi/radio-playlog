package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/endpoints"
)

// Firebase Hosting strips all incoming cookies except the one named
// "__session" (documented quirk of their CDN caching model). Share the name
// with the session cookie — the two are never live simultaneously: the state
// cookie is overwritten by the session cookie on successful callback, and a
// re-auth attempt overwrites any existing session with a fresh state value.
const stateCookieName = "__session"

type providerCfg struct {
	name       string
	oauth      *oauth2.Config
	userInfoFn func(ctx context.Context, client *http.Client) (sub, email string, err error)
}

func buildProviders(r *Resolved) map[string]*providerCfg {
	out := map[string]*providerCfg{}

	if r.GoogleID != "" {
		out["google"] = &providerCfg{
			name: "google",
			oauth: &oauth2.Config{
				ClientID:     r.GoogleID,
				ClientSecret: r.GoogleSecret,
				RedirectURL:  r.Cfg.APIBaseURL + "/auth/google/callback/",
				Scopes:       []string{"openid", "email"},
				Endpoint:     endpoints.Google,
			},
			userInfoFn: googleUserInfo,
		}
	}

	if r.MicrosoftID != "" {
		tenant := r.Cfg.MicrosoftTenant
		if tenant == "" {
			tenant = "common"
		}
		out["microsoft"] = &providerCfg{
			name: "microsoft",
			oauth: &oauth2.Config{
				ClientID:     r.MicrosoftID,
				ClientSecret: r.MicrosoftSecret,
				RedirectURL:  r.Cfg.APIBaseURL + "/auth/microsoft/callback/",
				Scopes:       []string{"openid", "email", "profile", "User.Read"},
				Endpoint: oauth2.Endpoint{
					AuthURL:  fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/authorize", tenant),
					TokenURL: fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenant),
				},
			},
			userInfoFn: microsoftUserInfo,
		}
	}

	return out
}

func googleUserInfo(ctx context.Context, client *http.Client) (string, string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://openidconnect.googleapis.com/v1/userinfo", nil)
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("google userinfo: %s", string(body))
	}
	var v struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "", "", err
	}
	if v.Sub == "" {
		return "", "", errors.New("google userinfo missing sub")
	}
	return v.Sub, v.Email, nil
}

func microsoftUserInfo(ctx context.Context, client *http.Client) (string, string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://graph.microsoft.com/v1.0/me", nil)
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("microsoft graph: %s", string(body))
	}
	var v struct {
		ID                string `json:"id"`
		Mail              string `json:"mail"`
		UserPrincipalName string `json:"userPrincipalName"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "", "", err
	}
	if v.ID == "" {
		return "", "", errors.New("microsoft graph missing id")
	}
	email := v.Mail
	if email == "" {
		email = v.UserPrincipalName
	}
	return v.ID, email, nil
}

// signState/verifyState: the oauth `state` parameter is derived from a random
// nonce, MAC'd with the session secret. We stash the same value in a short
// cookie so the callback can verify nothing was spoofed in the redirect URL.
func signState(secret []byte, nonce string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(nonce))
	return nonce + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func verifyState(secret []byte, token string) (string, bool) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(parts[0]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(parts[1])) {
		return "", false
	}
	return parts[0], true
}

func newNonce() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// handleAuthStart begins the OAuth dance for the named provider.
func (app *App) handleAuthStart(provider string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := app.providers[provider]
		if !ok {
			http.Error(w, "unknown provider", http.StatusNotFound)
			return
		}
		nonce := newNonce()
		state := signState(app.res.SessionSecret, nonce)

		http.SetCookie(w, &http.Cookie{
			Name:     stateCookieName,
			Value:    state,
			Path:     "/",
			Expires:  time.Now().Add(10 * time.Minute),
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
		})

		url := p.oauth.AuthCodeURL(state, oauth2.AccessTypeOnline)
		http.Redirect(w, r, url, http.StatusFound)
	}
}

// handleAuthCallback completes the OAuth dance and mints a session.
func (app *App) handleAuthCallback(provider string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := app.providers[provider]
		if !ok {
			http.Error(w, "unknown provider", http.StatusNotFound)
			return
		}

		q := r.URL.Query()
		if errMsg := q.Get("error"); errMsg != "" {
			http.Error(w, "oauth error: "+errMsg, http.StatusBadRequest)
			return
		}
		code := q.Get("code")
		state := q.Get("state")
		if code == "" || state == "" {
			http.Error(w, "missing code/state", http.StatusBadRequest)
			return
		}
		c, err := r.Cookie(stateCookieName)
		if err != nil || c.Value != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		if _, ok := verifyState(app.res.SessionSecret, state); !ok {
			http.Error(w, "bad state", http.StatusBadRequest)
			return
		}
		// Clear the state cookie.
		http.SetCookie(w, &http.Cookie{Name: stateCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})

		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()

		tok, err := p.oauth.Exchange(ctx, code)
		if err != nil {
			log.Printf("oauth exchange failed: %v", err)
			http.Error(w, "token exchange failed", http.StatusBadGateway)
			return
		}
		client := p.oauth.Client(ctx, tok)
		sub, email, err := p.userInfoFn(ctx, client)
		if err != nil {
			log.Printf("userinfo failed: %v", err)
			http.Error(w, "userinfo failed", http.StatusBadGateway)
			return
		}

		user, err := app.store.FindOrCreateUser(ctx, p.name, sub, email)
		if err != nil {
			log.Printf("FindOrCreateUser failed: %v", err)
			http.Error(w, "user store error", http.StatusInternalServerError)
			return
		}

		ttl := time.Duration(app.res.Cfg.SessionTTLHours) * time.Hour
		token := signSession(app.res.SessionSecret, user.UserID, ttl)
		setSessionCookie(w, token, ttl)

		http.Redirect(w, r, app.res.Cfg.UIOrigin+"/", http.StatusFound)
	}
}
