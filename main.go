package main

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// basePath is the URL prefix the app is served under (BASE_PATH, e.g. "/game"), "" for the root.
var basePath = strings.TrimSuffix(os.Getenv("BASE_PATH"), "/")

//go:embed web
var webFS embed.FS

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// static serves one embedded file from web/.
func static(name, contentType string) http.HandlerFunc {
	data, err := webFS.ReadFile("web/" + name)
	if err != nil {
		log.Fatal(err)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Write(data)
	}
}

// index renders the login footer once: its optional parts come from env.
func index() http.HandlerFunc {
	var page bytes.Buffer
	tmpl := template.Must(template.ParseFS(webFS, "web/index.html"))
	err := tmpl.Execute(&page, map[string]string{
		"Tokens":  os.Getenv("TOKENS"),
		"RepoURL": os.Getenv("REPO_URL"),
	})
	if err != nil {
		log.Fatal(err)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(page.Bytes())
	}
}

func main() {
	password := os.Getenv("ADMIN_PASSWORD")
	if password == "" {
		buf := make([]byte, 8)
		rand.Read(buf)
		password = hex.EncodeToString(buf)
		log.Printf("ADMIN_PASSWORD is not set, using a temporary one: %s", password)
	}

	store, err := OpenStore(env("DB_PATH", "sessions.db"))
	if err != nil {
		log.Fatal(err)
	}
	game := NewGame(store, token("owner", password))
	go game.Run()
	stats := &Stats{}
	go stats.Run(game)
	admin := &Admin{password: password, game: game, store: store, stats: stats}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", index())
	mux.HandleFunc("GET /game.js", static("game.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /ws", game.ServeWS)
	mux.HandleFunc("GET /admin", admin.auth(admin.page()))
	mux.HandleFunc("GET /admin/admin.js", admin.auth(static("admin.js", "text/javascript; charset=utf-8")))
	mux.HandleFunc("GET /admin/ws", admin.auth(admin.ws))
	mux.HandleFunc("POST /admin/owner", admin.auth(admin.markOwner))

	// Sessions are written when a player leaves, so flush the live ones on shutdown.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		game.SaveSessions()
		os.Exit(0)
	}()

	// Behind a reverse proxy the app lives under BASE_PATH: the proxy passes the
	// full path and the prefix is stripped here.
	var handler http.Handler = mux
	if basePath != "" {
		outer := http.NewServeMux()
		outer.Handle(basePath+"/", http.StripPrefix(basePath, mux))
		outer.Handle(basePath, http.RedirectHandler(basePath+"/", http.StatusMovedPermanently))
		handler = outer
	}

	addr := env("HOST", "") + ":" + env("PORT", "8080")
	log.Printf("listening on %s%s/", addr, basePath)
	log.Fatal(http.ListenAndServe(addr, handler))
}
