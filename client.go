package main

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	pingEvery    = 2 * time.Second
	readTimeout  = 15 * time.Second // a client that stops answering pings is dropped
	writeTimeout = 5 * time.Second
	kickedCode   = 4000 // WebSocket close code the game client recognises
)

var upgrader = websocket.Upgrader{ReadBufferSize: 1024, WriteBufferSize: 4096}

// Traffic counters for the admin stats.
var msgsIn, msgsOut, bytesOut atomic.Int64

// Client is one human connection.
type Client struct {
	conn  *websocket.Conn
	send  chan []byte
	ip    string
	ua    string
	owner bool
	ping  atomic.Int64 // round trip, ms

	// Guarded by Game.mu.
	name    string
	joined  time.Time
	maxMass float64
	player  *Player // current cell, possibly dead; nil before the first join
}

func (g *Game) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	ua := r.UserAgent()
	if len(ua) > 300 {
		ua = ua[:300]
	}
	c := &Client{
		conn:  conn,
		send:  make(chan []byte, 32),
		ip:    clientIP(r),
		ua:    ua,
		owner: hasCookie(r, "owner", g.ownerToken),
	}
	go c.writeLoop()
	c.readLoop(g)
	g.Leave(c)
}

// clientIP prefers the address reported by the reverse proxy.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (c *Client) readLoop(g *Game) {
	c.conn.SetReadLimit(512)
	c.conn.SetReadDeadline(time.Now().Add(readTimeout))
	// Pings carry their send time, so the pong tells us the round trip.
	c.conn.SetPongHandler(func(sent string) error {
		if ns, err := strconv.ParseInt(sent, 10, 64); err == nil {
			c.ping.Store((time.Now().UnixNano() - ns) / int64(time.Millisecond))
		}
		return c.conn.SetReadDeadline(time.Now().Add(readTimeout))
	})
	for {
		var m struct {
			T    string  `json:"t"`
			Name string  `json:"name"`
			X    float64 `json:"x"`
			Y    float64 `json:"y"`
		}
		if err := c.conn.ReadJSON(&m); err != nil {
			return
		}
		msgsIn.Add(1)
		switch m.T {
		case "join":
			g.Join(c, m.Name)
		case "m":
			g.SetTarget(c, m.X, m.Y)
		}
	}
}

// writeLoop is the connection's only writer of data frames; it ends when
// Game.Leave closes c.send or a write fails.
func (c *Client) writeLoop() {
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	defer c.conn.Close()
	for {
		select {
		case msg, ok := <-c.send:
			if !ok {
				return
			}
			c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if c.conn.WriteMessage(websocket.TextMessage, msg) != nil {
				return
			}
			msgsOut.Add(1)
			bytesOut.Add(int64(len(msg)))
		case <-ping.C:
			stamp := strconv.FormatInt(time.Now().UnixNano(), 10)
			if c.conn.WriteControl(websocket.PingMessage, []byte(stamp), time.Now().Add(writeTimeout)) != nil {
				return
			}
		}
	}
}

// kick sends a close frame with a reason. After that the next data write
// fails, writeLoop closes the connection and readLoop ends the session.
func (c *Client) kick() {
	msg := websocket.FormatCloseMessage(kickedCode, "kicked")
	c.conn.WriteControl(websocket.CloseMessage, msg, time.Now().Add(time.Second))
}

// session must be called with Game.mu held.
func (c *Client) session() Session {
	return Session{
		Name:    c.name,
		IP:      c.ip,
		UA:      c.ua,
		Start:   c.joined.UnixMilli(),
		End:     time.Now().UnixMilli(),
		MaxMass: round1(c.maxMass),
		Owner:   c.owner,
	}
}
