package main

import (
	"encoding/json"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	tickRate    = 25
	worldSize   = 4000.0
	foodCount   = 600
	cellSize    = 100.0 // side of one collision grid cell
	gridN       = int(worldSize / cellSize)
	startMass   = 20.0
	eatRatio    = 1.25  // how much heavier a cell must be to eat another
	baseSpeed   = 240.0 // px/s at start mass
	decayPerSec = 0.002 // share of mass lost every second
	maxBots     = 50

	// Every client sees the same part of the world whatever its screen size:
	// a viewW x viewH window divided by a zoom that depends on mass.
	// The client mirrors zoom() and radius() in game.js.
	viewW, viewH = 1920.0, 1080.0
)

var botNames = []string{"HR", "Тимлид", "Дедлайн", "Легаси-код", "Баг в проде", "Code Review", "Джун", "Сеньор"}

// Bot difficulty: how often a bot rethinks its target and how far it sees.
type difficulty struct {
	think  int // ticks between decisions
	vision float64
}

var difficulties = map[string]difficulty{
	"easy":   {think: 25, vision: 200},
	"normal": {think: 8, vision: 400},
	"hard":   {think: 2, vision: 700},
}

type Food struct{ X, Y, Hue int }

type Player struct {
	ID     uint32
	Name   string
	X, Y   float64
	dx, dy float64 // where the cell is heading, as an offset from its centre
	Mass   float64
	Eaten  int // food pellets eaten
	Hue    int
	Alive  bool
	Bot    bool
	slot   int // index in Game.bots
	Joined time.Time
}

type Kill struct {
	Killer   string `json:"k"`
	Victim   string `json:"v"`
	VictimID uint32 `json:"vi"`
}

// Snapshot is what one client receives every tick.
type Snapshot struct {
	T       string       `json:"t"`
	TS      int64        `json:"ts"` // server time, ms
	ID      uint32       `json:"id"` // the client's own cell
	Mass    float64      `json:"m"`  // 0 while dead
	Eaten   int          `json:"e"`
	Players []PlayerView `json:"p"`
	Food    [][3]int     `json:"f"` // x, y, hue
	Leaders []Leader     `json:"lb"`
	Kills   []Kill       `json:"k,omitempty"`
}

type PlayerView struct {
	ID   uint32  `json:"i"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	R    float64 `json:"r"`
	Hue  int     `json:"h"`
	Name string  `json:"n"`
}

type Leader struct {
	ID   uint32 `json:"i"`
	Name string `json:"n"`
	Mass int    `json:"m"`
}

type Game struct {
	mu         sync.Mutex
	store      *Store
	ownerToken string
	started    time.Time
	nextID     uint32
	tickN      int
	players    []*Player // every living cell, humans and bots
	bots       []*Player // by slot
	clients    map[*Client]struct{}
	food       [gridN * gridN][]Food
	cells      [gridN * gridN][]*Player // players by grid cell, rebuilt every tick
	kills      []Kill                   // this tick's kill feed
	botCount   int
	difficulty string
	tickTimes  [10 * tickRate]time.Duration // ring buffer for the admin stats
}

func NewGame(store *Store, ownerToken string) *Game {
	g := &Game{
		store:      store,
		ownerToken: ownerToken,
		started:    time.Now(),
		clients:    map[*Client]struct{}{},
		botCount:   8,
		difficulty: "normal",
	}
	for i := 0; i < foodCount; i++ {
		g.addFood()
	}
	return g
}

func radius(mass float64) float64 { return 4 * math.Sqrt(mass) }

func zoom(mass float64) float64 { return max(0.35, math.Pow(45/radius(mass), 0.4)) }

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func gridCoord(v float64) int { return min(max(int(v/cellSize), 0), gridN-1) }

// forCells calls fn with the index of every grid cell touching the given box.
func forCells(x, y, halfW, halfH float64, fn func(cell int)) {
	x0, x1 := gridCoord(x-halfW), gridCoord(x+halfW)
	y0, y1 := gridCoord(y-halfH), gridCoord(y+halfH)
	for cy := y0; cy <= y1; cy++ {
		for cx := x0; cx <= x1; cx++ {
			fn(cy*gridN + cx)
		}
	}
}

func (g *Game) Run() {
	for range time.Tick(time.Second / tickRate) {
		start := time.Now()
		g.mu.Lock()
		g.tick(start)
		g.tickTimes[(g.tickN-1)%len(g.tickTimes)] = time.Since(start)
		g.mu.Unlock()
	}
}

func (g *Game) tick(now time.Time) {
	g.tickN++
	g.manageBots()

	for i := range g.cells {
		g.cells[i] = g.cells[i][:0]
	}
	for _, p := range g.players {
		p.move(1.0 / tickRate)
		cell := gridCoord(p.Y)*gridN + gridCoord(p.X)
		g.cells[cell] = append(g.cells[cell], p)
	}
	for _, p := range g.players {
		if p.Alive {
			g.eat(p)
		}
	}

	alive := g.players[:0]
	for _, p := range g.players {
		if p.Alive {
			alive = append(alive, p)
		}
	}
	g.players = alive

	for _, p := range g.players {
		if p.Bot {
			g.think(p)
		}
	}
	g.broadcast(now)
}

func (p *Player) move(dt float64) {
	if dist := math.Hypot(p.dx, p.dy); dist > 1 {
		// Heavier cells are slower; a target inside the cell slows it down further.
		speed := baseSpeed * math.Pow(p.Mass/startMass, -0.22) * min(1, dist/radius(p.Mass))
		p.X = min(max(p.X+p.dx/dist*speed*dt, 0), worldSize)
		p.Y = min(max(p.Y+p.dy/dist*speed*dt, 0), worldSize)
	}
	p.Mass = max(startMass, p.Mass*(1-decayPerSec*dt))
}

// eat lets p swallow the food and the smaller players whose centres it covers.
func (g *Game) eat(p *Player) {
	r := radius(p.Mass)
	forCells(p.X, p.Y, r, r, func(cell int) {
		for i := 0; i < len(g.food[cell]); {
			f := g.food[cell][i]
			if math.Hypot(float64(f.X)-p.X, float64(f.Y)-p.Y) >= r {
				i++
				continue
			}
			last := len(g.food[cell]) - 1
			g.food[cell][i] = g.food[cell][last]
			g.food[cell] = g.food[cell][:last]
			g.addFood() // respawn elsewhere, keeping the pellet count constant
			p.Mass++
			p.Eaten++
		}
		for _, o := range g.cells[cell] {
			if o != p && o.Alive && p.Mass >= eatRatio*o.Mass && math.Hypot(o.X-p.X, o.Y-p.Y) < r {
				o.Alive = false
				p.Mass += o.Mass
				g.kills = append(g.kills, Kill{p.Name, o.Name, o.ID})
			}
		}
	})
}

func (g *Game) addFood() {
	f := Food{X: 20 + rand.Intn(worldSize-40), Y: 20 + rand.Intn(worldSize-40), Hue: rand.Intn(360)}
	cell := gridCoord(float64(f.Y))*gridN + gridCoord(float64(f.X))
	g.food[cell] = append(g.food[cell], f)
}

// spawn adds a new cell at the most secluded of a few random spots, so that
// newcomers are not eaten the moment they appear.
func (g *Game) spawn(name string, bot bool) *Player {
	g.nextID++
	p := &Player{ID: g.nextID, Name: name, Bot: bot, Mass: startMass, Hue: rand.Intn(360), Alive: true, Joined: time.Now()}
	bestGap := math.Inf(-1)
	for try := 0; try < 20; try++ {
		x := 100 + rand.Float64()*(worldSize-200)
		y := 100 + rand.Float64()*(worldSize-200)
		gap := math.Inf(1) // distance to the edge of the nearest cell
		for _, o := range g.players {
			if o.Alive {
				gap = min(gap, math.Hypot(o.X-x, o.Y-y)-radius(o.Mass))
			}
		}
		if gap > bestGap {
			bestGap, p.X, p.Y = gap, x, y
		}
	}
	g.players = append(g.players, p)
	return p
}

// ---- bots ----

func botName(slot int) string {
	name := botNames[slot%len(botNames)]
	if n := slot / len(botNames); n > 0 {
		name += " " + strconv.Itoa(n+1)
	}
	return name
}

// manageBots keeps exactly botCount bots alive, respawning the eaten ones.
func (g *Game) manageBots() {
	for len(g.bots) > g.botCount {
		g.bots[len(g.bots)-1].Alive = false
		g.bots = g.bots[:len(g.bots)-1]
	}
	for i := 0; i < g.botCount; i++ {
		if i < len(g.bots) && g.bots[i].Alive {
			continue
		}
		b := g.spawn(botName(i), true)
		b.slot = i
		if i < len(g.bots) {
			g.bots[i] = b
		} else {
			g.bots = append(g.bots, b)
		}
	}
}

// think picks a bot's heading: flee the nearest bigger cell, else chase the
// nearest smaller one, else go for the nearest food.
func (g *Game) think(b *Player) {
	d := difficulties[g.difficulty]
	if (g.tickN+b.slot)%d.think != 0 {
		return
	}
	const (
		food = iota
		prey
		threat
	)
	var seen [3]bool
	var tx, ty [3]float64
	best := [3]float64{d.vision, d.vision, d.vision}
	look := func(kind int, x, y, dist float64) {
		if dist < best[kind] {
			seen[kind], tx[kind], ty[kind], best[kind] = true, x, y, dist
		}
	}
	forCells(b.X, b.Y, d.vision, d.vision, func(cell int) {
		for _, o := range g.cells[cell] {
			dist := math.Hypot(o.X-b.X, o.Y-b.Y) - radius(o.Mass) // to the other cell's edge
			switch {
			case o == b || !o.Alive:
			case o.Mass >= eatRatio*b.Mass:
				look(threat, o.X, o.Y, dist)
			case b.Mass >= eatRatio*o.Mass:
				look(prey, o.X, o.Y, dist)
			}
		}
		for _, f := range g.food[cell] {
			x, y := float64(f.X), float64(f.Y)
			look(food, x, y, math.Hypot(x-b.X, y-b.Y))
		}
	})

	switch {
	case seen[threat]:
		b.dx, b.dy = b.X-tx[threat], b.Y-ty[threat]
	case seen[prey]:
		b.dx, b.dy = tx[prey]-b.X, ty[prey]-b.Y
	case seen[food]:
		b.dx, b.dy = tx[food]-b.X, ty[food]-b.Y
	default: // nothing in sight: wander
		b.dx, b.dy = rand.Float64()-0.5, rand.Float64()-0.5
	}
	// Stretch the heading so that bots always move at full speed.
	if dist := math.Hypot(b.dx, b.dy); dist > 0 {
		b.dx, b.dy = b.dx/dist*1000, b.dy/dist*1000
	}
}

// ---- networking ----

// broadcast sends every client what its viewport contains plus the leaderboard.
func (g *Game) broadcast(now time.Time) {
	top := append([]*Player(nil), g.players...)
	sort.Slice(top, func(i, j int) bool { return top[i].Mass > top[j].Mass })
	leaders := []Leader{}
	for _, p := range top[:min(10, len(top))] {
		leaders = append(leaders, Leader{p.ID, p.Name, int(p.Mass)})
	}

	ts := now.Sub(g.started).Milliseconds()
	for c := range g.clients {
		me := c.player
		c.maxMass = max(c.maxMass, me.Mass)
		s := Snapshot{T: "s", TS: ts, ID: me.ID, Eaten: me.Eaten, Leaders: leaders, Kills: g.kills,
			Players: []PlayerView{}, Food: [][3]int{}}
		if me.Alive {
			s.Mass = round1(me.Mass)
		}
		halfW, halfH := viewW/2/zoom(me.Mass), viewH/2/zoom(me.Mass)
		for _, p := range g.players {
			r := radius(p.Mass)
			if math.Abs(p.X-me.X) < halfW+r && math.Abs(p.Y-me.Y) < halfH+r {
				s.Players = append(s.Players, PlayerView{p.ID, round1(p.X), round1(p.Y), round1(r), p.Hue, p.Name})
			}
		}
		forCells(me.X, me.Y, halfW, halfH, func(cell int) {
			for _, f := range g.food[cell] {
				s.Food = append(s.Food, [3]int{f.X, f.Y, f.Hue})
			}
		})
		data, _ := json.Marshal(s)
		select {
		case c.send <- data:
		default: // the client is not keeping up: skip this snapshot
		}
	}
	g.kills = g.kills[:0]
}

// Join puts the client's cell into the world: on first entry and on respawn.
func (g *Game) Join(c *Client, name string) {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > 16 {
		name = string([]rune(name)[:16])
	}
	if name == "" {
		name = "Кандидат"
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if c.player == nil {
		c.joined = time.Now()
		g.clients[c] = struct{}{}
	} else if c.player.Alive {
		return
	}
	c.name = name
	c.player = g.spawn(name, false)
}

func (g *Game) SetTarget(c *Client, dx, dy float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if c.player != nil {
		c.player.dx, c.player.dy = dx, dy
	}
}

// Leave removes a disconnected client and records its session.
func (g *Game) Leave(c *Client) {
	g.mu.Lock()
	_, joined := g.clients[c]
	delete(g.clients, c)
	close(c.send) // nothing sends to a client that is not in g.clients
	var s Session
	if joined {
		c.player.Alive = false
		s = c.session()
	}
	g.mu.Unlock()
	if joined {
		g.store.Save(s)
	}
}

func (g *Game) SaveSessions() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for c := range g.clients {
		g.store.Save(c.session())
	}
}

// ---- admin ----

// PlayerRow is one line of the admin player table.
type PlayerRow struct {
	ID     uint32  `json:"id"`
	Name   string  `json:"name"`
	Bot    bool    `json:"bot"`
	Owner  bool    `json:"owner"`
	IP     string  `json:"ip"`
	UA     string  `json:"ua"`
	Joined int64   `json:"joined"` // unix ms
	Ping   int64   `json:"ping"`   // ms
	Mass   float64 `json:"mass"`   // 0 while dead
}

// Rows lists humans (oldest first), then bots.
func (g *Game) Rows() []PlayerRow {
	g.mu.Lock()
	defer g.mu.Unlock()
	rows := []PlayerRow{}
	for c := range g.clients {
		row := PlayerRow{ID: c.player.ID, Name: c.name, Owner: c.owner, IP: c.ip, UA: c.ua,
			Joined: c.joined.UnixMilli(), Ping: c.ping.Load()}
		if c.player.Alive {
			row.Mass = round1(c.player.Mass)
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Joined < rows[j].Joined })
	for _, b := range g.bots {
		rows = append(rows, PlayerRow{ID: b.ID, Name: b.Name, Bot: true, Joined: b.Joined.UnixMilli(), Mass: round1(b.Mass)})
	}
	return rows
}

func (g *Game) Settings() (bots int, level string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.botCount, g.difficulty
}

func (g *Game) SetBots(n int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.botCount = min(max(n, 0), maxBots)
}

func (g *Game) SetDifficulty(level string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := difficulties[level]; ok {
		g.difficulty = level
	}
}

// Kick disconnects a human or removes a bot (manageBots then spawns a new one).
func (g *Game) Kick(id uint32) {
	g.mu.Lock()
	var target *Client
	for c := range g.clients {
		if c.player.ID == id {
			target = c
		}
	}
	for _, b := range g.bots {
		if b.ID == id {
			b.Alive = false
		}
	}
	g.mu.Unlock()
	if target != nil {
		target.kick()
	}
}

// Reset starts the world over: fresh food and bots, living humans respawn small.
func (g *Game) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.players, g.bots = nil, nil
	for i := range g.food {
		g.food[i] = g.food[i][:0]
	}
	for i := 0; i < foodCount; i++ {
		g.addFood()
	}
	for c := range g.clients {
		if c.player.Alive {
			c.player = g.spawn(c.name, false)
		}
	}
}

// Load reports tick times over the last 10 s (ms) and the current world size.
func (g *Game) Load() (tickAvg, tickP99 float64, entities, conns int) {
	g.mu.Lock()
	times := append([]time.Duration(nil), g.tickTimes[:min(g.tickN, len(g.tickTimes))]...)
	entities, conns = len(g.players)+foodCount, len(g.clients)
	g.mu.Unlock()
	if len(times) == 0 {
		return
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	var sum time.Duration
	for _, t := range times {
		sum += t
	}
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	return ms(sum / time.Duration(len(times))), ms(times[len(times)*99/100]), entities, conns
}
