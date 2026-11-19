package main

import (
	"database/sql"

	_ "modernc.org/sqlite"
)

// Session is one finished human visit, as kept in SQLite.
type Session struct {
	Name    string  `json:"name"`
	IP      string  `json:"ip"`
	UA      string  `json:"ua"`
	Start   int64   `json:"start"` // unix ms
	End     int64   `json:"end"`   // unix ms
	MaxMass float64 `json:"maxMass"`
	Owner   bool    `json:"owner"`
}

type Store struct{ db *sql.DB }

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite has a single writer; avoids SQLITE_BUSY
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS sessions (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		name       TEXT NOT NULL,
		ip         TEXT NOT NULL,
		ua         TEXT NOT NULL,
		started_at INTEGER NOT NULL,
		ended_at   INTEGER NOT NULL,
		max_mass   REAL NOT NULL,
		is_owner   INTEGER NOT NULL
	)`)
	return &Store{db}, err
}

func (s *Store) Save(v Session) error {
	_, err := s.db.Exec(
		`INSERT INTO sessions (name, ip, ua, started_at, ended_at, max_mass, is_owner) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		v.Name, v.IP, v.UA, v.Start, v.End, v.MaxMass, v.Owner)
	return err
}

// Recent returns the last n sessions, newest first.
func (s *Store) Recent(n int) ([]Session, error) {
	rows, err := s.db.Query(
		`SELECT name, ip, ua, started_at, ended_at, max_mass, is_owner FROM sessions ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Session{}
	for rows.Next() {
		var v Session
		if err := rows.Scan(&v.Name, &v.IP, &v.UA, &v.Start, &v.End, &v.MaxMass, &v.Owner); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}
