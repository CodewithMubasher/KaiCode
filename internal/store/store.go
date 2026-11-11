// Package store keeps everything Kai remembers in one SQLite file: custom
// providers, sessions, their messages, and small settings such as the active
// provider. It uses modernc.org/sqlite, a pure-Go driver, so no C compiler is
// needed.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	// ErrDuplicate means a provider with that name already exists.
	ErrDuplicate = errors.New("a provider with that name already exists")
	// ErrNotFound means the row does not exist.
	ErrNotFound = errors.New("not found")
)

// Store is a handle on the database. It is safe for concurrent use.
type Store struct{ db *sql.DB }

// migrations are applied in order; PRAGMA user_version records how many ran.
// Never edit an entry that has shipped: append a new one.
var migrations = []string{
	`CREATE TABLE providers (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		name       TEXT    NOT NULL UNIQUE COLLATE NOCASE,
		base_url   TEXT    NOT NULL,
		api_key    TEXT    NOT NULL DEFAULT '',
		model      TEXT    NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL
	);
	CREATE TABLE sessions (
		id         TEXT    PRIMARY KEY,
		title      TEXT    NOT NULL DEFAULT '',
		provider   TEXT    NOT NULL DEFAULT '',
		cwd        TEXT    NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);
	CREATE TABLE messages (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT    NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
		role       TEXT    NOT NULL,
		text       TEXT    NOT NULL,
		created_at INTEGER NOT NULL
	);
	CREATE INDEX messages_session ON messages(session_id, id);
	CREATE INDEX sessions_updated ON sessions(updated_at DESC);
	CREATE TABLE settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);`,

	// Session state added after the first release: the real token count the
	// provider reported, and one JSON blob holding everything the person saw
	// (including every timeline node), so a reopened session looks exactly as
	// it did when it was closed.
	`ALTER TABLE sessions ADD COLUMN tokens INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE sessions ADD COLUMN state TEXT NOT NULL DEFAULT '';
	ALTER TABLE messages ADD COLUMN steps TEXT NOT NULL DEFAULT '';`,
}

// Open opens (and creates, and migrates) the database at path.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("store: empty database path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// The file holds API keys, so create it readable by the owner only
	// before SQLite creates it with the default mode.
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600); err == nil {
		_ = f.Close()
	}
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: Kai is a single local process, and this rules out
	// "database is locked" between its own goroutines.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	var have int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&have); err != nil {
		return err
	}
	for i := have; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		t = time.Now()
	}
	return t.UnixMilli()
}

func fromMS(v int64) time.Time { return time.UnixMilli(v) }

// ---- providers -------------------------------------------------------------

// Provider is a custom OpenAI-compatible endpoint.
type Provider struct {
	ID        int64
	Name      string
	BaseURL   string
	APIKey    string // stored as plain text in the owner-only database file
	Model     string
	CreatedAt time.Time
}

// AddProvider saves a provider and returns it with its id.
func (s *Store) AddProvider(p Provider) (Provider, error) {
	p.Name = strings.TrimSpace(p.Name)
	p.BaseURL = strings.TrimSpace(p.BaseURL)
	p.APIKey = strings.TrimSpace(p.APIKey)
	p.Model = strings.TrimSpace(p.Model)
	if p.Name == "" || p.BaseURL == "" {
		return p, errors.New("a provider needs a name and a base URL")
	}
	now := time.Now()
	res, err := s.db.Exec(
		`INSERT INTO providers(name, base_url, api_key, model, created_at) VALUES(?,?,?,?,?)`,
		p.Name, p.BaseURL, p.APIKey, p.Model, ms(now))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return p, ErrDuplicate
		}
		return p, err
	}
	p.ID, _ = res.LastInsertId()
	p.CreatedAt = now
	return p, nil
}

const providerCols = `id, name, base_url, api_key, model, created_at`

func scanProvider(sc interface{ Scan(...any) error }) (Provider, error) {
	var p Provider
	var created int64
	if err := sc.Scan(&p.ID, &p.Name, &p.BaseURL, &p.APIKey, &p.Model, &created); err != nil {
		return p, err
	}
	p.CreatedAt = fromMS(created)
	return p, nil
}

// Providers lists every custom provider, by name.
func (s *Store) Providers() ([]Provider, error) {
	rows, err := s.db.Query(`SELECT ` + providerCols + ` FROM providers ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Provider
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ProviderByID returns one provider, or ErrNotFound.
func (s *Store) ProviderByID(id int64) (Provider, error) {
	p, err := scanProvider(s.db.QueryRow(`SELECT `+providerCols+` FROM providers WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// SetProviderModel changes the model a provider uses.
func (s *Store) SetProviderModel(id int64, model string) error {
	res, err := s.db.Exec(`UPDATE providers SET model = ? WHERE id = ?`, strings.TrimSpace(model), id)
	return affected(res, err)
}

// DeleteProvider removes a provider.
func (s *Store) DeleteProvider(id int64) error {
	res, err := s.db.Exec(`DELETE FROM providers WHERE id = ?`, id)
	return affected(res, err)
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- sessions --------------------------------------------------------------

// Session is one saved conversation.
type Session struct {
	ID        string
	Title     string
	Provider  string
	Cwd       string
	Tokens    int
	CreatedAt time.Time
	UpdatedAt time.Time
	Messages  int // filled by Sessions and Session
}

// CreateSession saves a new session; an existing id is left alone.
func (s *Store) CreateSession(x Session) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO sessions(id, title, provider, cwd, created_at, updated_at) VALUES(?,?,?,?,?,?)`,
		x.ID, x.Title, x.Provider, x.Cwd, ms(x.CreatedAt), ms(x.CreatedAt))
	return err
}

const sessionCols = `s.id, s.title, s.provider, s.cwd, s.tokens, s.created_at, s.updated_at,
	(SELECT COUNT(*) FROM messages m WHERE m.session_id = s.id)`

func scanSession(sc interface{ Scan(...any) error }) (Session, error) {
	var x Session
	var created, updated int64
	if err := sc.Scan(&x.ID, &x.Title, &x.Provider, &x.Cwd, &x.Tokens, &created, &updated, &x.Messages); err != nil {
		return x, err
	}
	x.CreatedAt, x.UpdatedAt = fromMS(created), fromMS(updated)
	return x, nil
}

// Sessions lists sessions, most recently used first. limit <= 0 means all.
func (s *Store) Sessions(limit int) ([]Session, error) {
	if limit <= 0 {
		limit = -1 // SQLite: no limit
	}
	rows, err := s.db.Query(`SELECT `+sessionCols+` FROM sessions s ORDER BY s.updated_at DESC, s.created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Session returns one session, or ErrNotFound.
func (s *Store) Session(id string) (Session, error) {
	x, err := scanSession(s.db.QueryRow(`SELECT `+sessionCols+` FROM sessions s WHERE s.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	return x, err
}

// RenameSession sets a session's title.
func (s *Store) RenameSession(id, title string) error {
	res, err := s.db.Exec(`UPDATE sessions SET title = ? WHERE id = ?`, strings.TrimSpace(title), id)
	return affected(res, err)
}

// DeleteSession removes a session and its messages.
func (s *Store) DeleteSession(id string) error {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	return affected(res, err)
}

// TouchSession records that a session was just used with a provider.
func (s *Store) TouchSession(id, provider string) error {
	_, err := s.db.Exec(`UPDATE sessions SET updated_at = ?, provider = ? WHERE id = ?`, ms(time.Now()), provider, id)
	return err
}

// SessionState returns the saved JSON state of a session (its chat and agent
// state), or "" when the session has none yet.
func (s *Store) SessionState(id string) (string, error) {
	var state string
	err := s.db.QueryRow(`SELECT state FROM sessions WHERE id = ?`, id).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return state, err
}

// SaveSessionState stores the JSON state of a session together with its token
// count. It only updates the row: the session itself is created by the first
// recorded message.
func (s *Store) SaveSessionState(id, state string, tokens int) error {
	res, err := s.db.Exec(`UPDATE sessions SET state = ?, tokens = ? WHERE id = ?`, state, tokens, id)
	return affected(res, err)
}

// ---- messages --------------------------------------------------------------

// Message is one entry of a session: role is "user", "assistant" or "tool".
type Message struct {
	ID    int64
	Role  string
	Text  string
	Steps string
	At    time.Time
}

// AppendMessage adds a message to a session and bumps its updated time.
func (s *Store) AppendMessage(sessionID, role, text string, at time.Time) error {
	return s.AppendMessageSteps(sessionID, role, text, "", at)
}

// AppendMessageSteps adds a message with an optional JSON rendering of what
// the person saw for it (timeline nodes), and bumps the session's updated time.
func (s *Store) AppendMessageSteps(sessionID, role, text, steps string, at time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO messages(session_id, role, text, steps, created_at) VALUES(?,?,?,?,?)`,
		sessionID, role, text, steps, ms(at)); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`UPDATE sessions SET updated_at = ? WHERE id = ?`, ms(time.Now()), sessionID); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Messages returns a session's messages in the order they were written.
func (s *Store) Messages(sessionID string) ([]Message, error) {
	rows, err := s.db.Query(`SELECT role, text, steps, created_at FROM messages WHERE session_id = ? ORDER BY id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var at int64
		if err := rows.Scan(&m.Role, &m.Text, &m.Steps, &at); err != nil {
			return nil, err
		}
		m.At = fromMS(at)
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---- settings --------------------------------------------------------------

// Setting reads a setting; ok is false when it was never set.
func (s *Store) Setting(key string) (value string, ok bool) {
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	return value, err == nil
}

// SetSetting writes a setting.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings(key, value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// UpdateSessionTokens saves the token count for a session.
func (s *Store) UpdateSessionTokens(id string, tokens int) error {
	_, err := s.db.Exec(`UPDATE sessions SET tokens = ? WHERE id = ?`, tokens, id)
	return err
}
