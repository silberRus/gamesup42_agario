package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"time"
)

type Admin struct {
	password string
	game     *Game
	store    *Store
	stats    *Stats
}

// token derives a cookie value from the admin password, so cookies survive
// restarts and stop working when the password changes.
func token(purpose, password string) string {
	sum := sha256.Sum256([]byte(purpose + ":" + password))
	return hex.EncodeToString(sum[:])
}

func hasCookie(r *http.Request, name, value string) bool {
	c, err := r.Cookie(name)
	return err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(value)) == 1
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// auth lets a request through with the right Basic Auth password or with the
// cookie set by the admin page. The cookie is there because not every browser
// repeats Basic credentials on the WebSocket handshake.
func (a *Admin) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, pass, ok := r.BasicAuth()
		ok = ok && subtle.ConstantTimeCompare([]byte(pass), []byte(a.password)) == 1
		if !ok && !hasCookie(r, "admin", token("admin", a.password)) {
			w.Header().Set("WWW-Authenticate", `Basic realm="admin", charset="UTF-8"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (a *Admin) page() http.HandlerFunc {
	serve := static("admin.html", "text/html; charset=utf-8")
	return func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name: "admin", Value: token("admin", a.password), Path: basePath + "/admin",
			HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteStrictMode,
		})
		serve(w, r)
	}
}

// markOwner tags this browser: game sessions it opens from now on are the owner's.
func (a *Admin) markOwner(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: "owner", Value: a.game.ownerToken, Path: basePath + "/", MaxAge: 365 * 24 * 3600,
		HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

// adminMsg is the state pushed to the panel every second.
type adminMsg struct {
	Now        int64       `json:"now"` // unix ms
	Stats      Sample      `json:"stats"`
	Chart      []Sample    `json:"chart,omitempty"` // last 5 minutes, first message only
	Players    []PlayerRow `json:"players"`
	Sessions   []Session   `json:"sessions"`
	Bots       int         `json:"bots"`
	Difficulty string      `json:"difficulty"`
	IsOwner    bool        `json:"isOwner"` // this browser is marked as the owner's
}

func (a *Admin) ws(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Commands from the panel; also notices when the panel goes away.
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			var cmd struct {
				Cmd   string `json:"cmd"`
				N     int    `json:"n"`
				Level string `json:"level"`
				ID    uint32 `json:"id"`
			}
			if conn.ReadJSON(&cmd) != nil {
				return
			}
			switch cmd.Cmd {
			case "bots":
				a.game.SetBots(cmd.N)
			case "difficulty":
				a.game.SetDifficulty(cmd.Level)
			case "kick":
				a.game.Kick(cmd.ID)
			case "reset":
				a.game.Reset()
			}
		}
	}()

	isOwner := hasCookie(r, "owner", a.game.ownerToken)
	chart := a.stats.History()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		msg := adminMsg{Now: time.Now().UnixMilli(), Stats: a.stats.Last(), Chart: chart,
			Players: a.game.Rows(), IsOwner: isOwner}
		msg.Sessions, _ = a.store.Recent(100)
		msg.Bots, msg.Difficulty = a.game.Settings()
		chart = nil
		if conn.WriteJSON(msg) != nil {
			return
		}
		select {
		case <-closed:
			return
		case <-ticker.C:
		}
	}
}
