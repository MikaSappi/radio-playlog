package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
)

func main() {
	res, err := loadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx := context.Background()
	store, err := NewStore(ctx, res)
	if err != nil {
		log.Fatalf("bq store: %v", err)
	}
	defer store.Close()

	app := &App{
		res:       res,
		store:     store,
		providers: buildProviders(res),
		mailer:    newMailer(res),
	}

	// Start background workers for silence alerts and scheduled reports.
	// They run forever; their own tickers handle pacing.
	go runSilenceAlerter(ctx, app)
	go runReportWorker(ctx, app)

	mux := http.NewServeMux()

	// Auth endpoints — one pair per configured provider.
	for name := range app.providers {
		mux.HandleFunc("/auth/"+name+"/start", app.handleAuthStart(name))
		// Match both with and without the trailing slash — OAuth consoles
		// differ on whether they let you keep or strip it.
		mux.HandleFunc("/auth/"+name+"/callback", app.handleAuthCallback(name))
		mux.HandleFunc("/auth/"+name+"/callback/", app.handleAuthCallback(name))
	}

	mux.HandleFunc("/api/logout", app.handleLogout)
	mux.HandleFunc("/api/me", app.requireUser(app.handleMe))

	mux.HandleFunc("/api/keys", app.requireUser(func(w http.ResponseWriter, r *http.Request, uid string) {
		switch r.Method {
		case http.MethodGet:
			app.handleKeysGet(w, r, uid)
		case http.MethodPost:
			app.handleKeysPost(w, r, uid)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	mux.HandleFunc("/api/keys/", app.requireUser(func(w http.ResponseWriter, r *http.Request, uid string) {
		switch r.Method {
		case http.MethodDelete:
			app.handleKeyDisable(w, r, uid)
		case http.MethodPatch:
			app.handleKeyRename(w, r, uid)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))

	mux.HandleFunc("/api/logs", app.requireUser(func(w http.ResponseWriter, r *http.Request, uid string) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		app.handleLogsGet(w, r, uid)
	}))

	mux.HandleFunc("/api/export", app.requireUser(func(w http.ResponseWriter, r *http.Request, uid string) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		app.handleExport(w, r, uid)
	}))

	mux.HandleFunc("/api/settings", app.requireUser(func(w http.ResponseWriter, r *http.Request, uid string) {
		switch r.Method {
		case http.MethodGet:
			app.handleSettingsGet(w, r, uid)
		case http.MethodPut:
			app.handleSettingsPut(w, r, uid)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	handler := withCORS(mux, res.Cfg.UIOrigin)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("ui-api listening on :%s (ui=%s)", port, res.Cfg.UIOrigin)
	if err := http.ListenAndServe(":"+port, handler); err != nil {
		log.Fatal(err)
	}
}

// withCORS allows the configured UI origin to call the API with credentials.
// Cross-origin is the default deployment shape — static site on one host,
// API on Cloud Run — so we don't bother with a permissive wildcard mode.
func withCORS(next http.Handler, allowOrigin string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && strings.EqualFold(origin, allowOrigin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
