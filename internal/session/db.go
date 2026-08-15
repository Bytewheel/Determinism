package session

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Loader reads sessions from the opencode SQLite database.
type Loader struct {
	db *sql.DB
}

// Open opens the opencode database read-only. path is the sqlite file; it may
// be the directory containing opencode.db (default search) or the .db path.
func Open(path string) (*Loader, error) {
	if path == "" {
		path = defaultDBPath()
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		path = filepath.Join(path, "opencode.db")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("opencode db not found at %s: %w", path, err)
	}
	dsn := "file:" + path + "?mode=ro&_pragma=busy_timeout(10000)&_pragma=query_only(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return &Loader{db: db}, nil
}

// Close releases the database handle.
func (l *Loader) Close() error { return l.db.Close() }

func defaultDBPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "opencode", "opencode.db")
}

// List returns all sessions with basic metadata, most recently updated first.
func (l *Loader) List(limit int) ([]Session, error) {
	q := `SELECT id, slug, directory, title, COALESCE(parent_id,''), time_created, time_updated
	      FROM session ORDER BY time_updated DESC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := l.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		var tc, tu int64
		if err := rows.Scan(&s.ID, &s.Slug, &s.Directory, &s.Title, &s.ParentID, &tc, &tu); err != nil {
			return nil, err
		}
		s.TimeCreated = time.UnixMilli(tc)
		s.TimeUpdated = time.UnixMilli(tu)
		out = append(out, s)
	}
	return out, rows.Err()
}

// Find resolves a Lookup against the database. It returns the first matching
// session. The lookup semantics, in priority order:
//   - exact session id
//   - exact slug ("session name")
//   - title substring
//   - initial user prompt text
func (l *Loader) Find(lk Lookup) (*Session, error) {
	if lk.ID != "" {
		if s, err := l.byID(lk.ID); err == nil {
			return s, nil
		}
	}
	if lk.Name != "" {
		if s, err := l.bySlug(lk.Name); err == nil {
			return s, nil
		}
	}
	if lk.Title != "" {
		if s, err := l.byTitle(lk.Title); err == nil {
			return s, nil
		}
	}
	if lk.Prompt != "" {
		if s, err := l.byPrompt(lk.Prompt); err == nil {
			return s, nil
		}
	}
	return nil, fmt.Errorf("no session matches lookup %+v", lk)
}

func (l *Loader) byID(id string) (*Session, error) {
	s, err := l.scanSession(`WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	return l.Load(s.ID)
}

func (l *Loader) bySlug(slug string) (*Session, error) {
	s, err := l.scanSession(`WHERE slug = ?`, slug)
	if err != nil {
		return nil, err
	}
	return l.Load(s.ID)
}

func (l *Loader) byTitle(substr string) (*Session, error) {
	s, err := l.scanSession(`WHERE lower(title) LIKE '%' || lower(?) || '%'`, substr)
	if err != nil {
		return nil, err
	}
	return l.Load(s.ID)
}

func (l *Loader) scanSession(where string, arg any) (*Session, error) {
	q := `SELECT id, slug, directory, title, COALESCE(parent_id,''), time_created, time_updated
	      FROM session ` + where + ` ORDER BY time_updated DESC LIMIT 1`
	var s Session
	var tc, tu int64
	err := l.db.QueryRow(q, arg).Scan(&s.ID, &s.Slug, &s.Directory, &s.Title, &s.ParentID, &tc, &tu)
	if err != nil {
		return nil, err
	}
	s.TimeCreated = time.UnixMilli(tc)
	s.TimeUpdated = time.UnixMilli(tu)
	return &s, nil
}

// byPrompt finds a session whose first user message contains the prompt text.
func (l *Loader) byPrompt(prompt string) (*Session, error) {
	needle := strings.ToLower(prompt)
	sessions, err := l.List(5000)
	if err != nil {
		return nil, err
	}
	for _, meta := range sessions {
		text, err := l.firstUserPrompt(meta.ID)
		if err != nil || text == "" {
			continue
		}
		if strings.Contains(strings.ToLower(text), needle) {
			return l.Load(meta.ID)
		}
	}
	return nil, fmt.Errorf("no session with initial prompt containing %q", prompt)
}

// firstUserPrompt returns the concatenated text of the first user message.
func (l *Loader) firstUserPrompt(sessionID string) (string, error) {
	var msgID string
	err := l.db.QueryRow(
		`SELECT id FROM message
		 WHERE session_id = ? AND json_extract(data,'$.role') = 'user'
		 ORDER BY time_created, id LIMIT 1`,
		sessionID,
	).Scan(&msgID)
	if err != nil {
		return "", err
	}
	parts, err := l.parts(msgID, sessionID)
	if err != nil {
		return "", err
	}
	var text strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			text.WriteString(p.Text)
		}
	}
	return text.String(), nil
}

// Load hydrates a session with all messages and parts, in creation order.
func (l *Loader) Load(id string) (*Session, error) {
	meta, err := l.scanSession(`WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	msgs, err := l.messages(id)
	if err != nil {
		return nil, err
	}
	meta.Messages = msgs
	return meta, nil
}

func (l *Loader) messages(sessionID string) ([]Message, error) {
	rows, err := l.db.Query(
		`SELECT id, time_created, data FROM message WHERE session_id = ? ORDER BY time_created, id`,
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var msgs []Message
	for rows.Next() {
		var m Message
		var tc int64
		var data string
		if err := rows.Scan(&m.ID, &tc, &data); err != nil {
			return nil, err
		}
		m.SessionID = sessionID
		m.TimeCreated = time.UnixMilli(tc)
		if err := decodeMessage(&m, []byte(data)); err != nil {
			return nil, err
		}
		parts, err := l.parts(m.ID, sessionID)
		if err != nil {
			return nil, err
		}
		m.Parts = parts
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func (l *Loader) parts(messageID, sessionID string) ([]Part, error) {
	rows, err := l.db.Query(
		`SELECT id, time_created, data FROM part WHERE message_id = ? AND session_id = ? ORDER BY time_created, id`,
		messageID, sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var parts []Part
	for rows.Next() {
		var p Part
		var tc int64
		var data string
		if err := rows.Scan(&p.ID, &tc, &data); err != nil {
			return nil, err
		}
		p.MessageID = messageID
		if err := decodePart(&p, []byte(data)); err != nil {
			return nil, err
		}
		parts = append(parts, p)
	}
	return parts, rows.Err()
}

type msgData struct {
	Role string `json:"role"`
}

func decodeMessage(m *Message, data []byte) error {
	var d msgData
	if err := json.Unmarshal(data, &d); err != nil {
		return fmt.Errorf("decode message %s: %w", m.ID, err)
	}
	m.Role = d.Role
	return nil
}

type partData struct {
	Type        string          `json:"type"`
	Text        string          `json:"text"`
	Reasoning   string          `json:"reasoning"`
	Tool        string          `json:"tool"`
	State       partState       `json:"state"`
	Prompt      string          `json:"prompt"`
	Description string          `json:"description"`
	Agent       string          `json:"agent"`
	Metadata    json.RawMessage `json:"metadata"`
}

type partState struct {
	Status string          `json:"status"`
	Input  json.RawMessage `json:"input"`
	Output json.RawMessage `json:"output"`
	Error  json.RawMessage `json:"error"`
	Meta   json.RawMessage `json:"metadata"`
}

var taskIDRe = regexp.MustCompile(`task_id:\s*(ses_[A-Za-z0-9]+)`)

func decodePart(p *Part, data []byte) error {
	var d partData
	if err := json.Unmarshal(data, &d); err != nil {
		return fmt.Errorf("decode part %s: %w", p.ID, err)
	}
	p.Type = d.Type
	switch d.Type {
	case "text":
		p.Text = d.Text
	case "reasoning":
		p.Text = d.Reasoning
	case "tool":
		p.ToolName = d.Tool
		p.Status = d.State.Status
		p.Input = d.State.Input
		if len(d.State.Output) > 0 {
			p.Output = d.State.Output
		} else if len(d.State.Error) > 0 {
			p.Output = d.State.Error
		}
		// Resolve child session for task tool from output task_id.
		if d.Tool == "task" && len(p.Output) > 0 {
			if m := taskIDRe.FindSubmatch(p.Output); len(m) == 2 {
				p.ChildID = string(m[1])
			}
		}
	case "subtask":
		p.Prompt = d.Prompt
		p.Description = d.Description
		p.Agent = d.Agent
	}
	return nil
}

// Children returns the child sessions of a session (subagent sessions spawned
// by the given session), in creation order.
func (l *Loader) Children(parentID string) ([]Session, error) {
	rows, err := l.db.Query(
		`SELECT id, slug, directory, title, COALESCE(parent_id,''), time_created, time_updated
		 FROM session WHERE parent_id = ? ORDER BY time_created, id`,
		parentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		var tc, tu int64
		if err := rows.Scan(&s.ID, &s.Slug, &s.Directory, &s.Title, &s.ParentID, &tc, &tu); err != nil {
			return nil, err
		}
		s.TimeCreated = time.UnixMilli(tc)
		s.TimeUpdated = time.UnixMilli(tu)
		out = append(out, s)
	}
	return out, rows.Err()
}

// FindChild matches a child session of parentID by description/title
// substring, preferring the most recently created match. Used to resolve
// subprompts whose task tool recorded no task_id.
func (l *Loader) FindChild(parentID, description string) (string, error) {
	rows, err := l.db.Query(
		`SELECT id FROM session
		 WHERE parent_id = ? AND (lower(title) LIKE '%' || lower(?) || '%')
		 ORDER BY time_created DESC LIMIT 1`,
		parentID, description,
	)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		return id, nil
	}
	return "", fmt.Errorf("no child session of %s matching %q", parentID, description)
}
