package session

import "time"

// Session is a normalized, source-agnostic representation of an opencode
// session. It is produced either from the opencode chat text format (a
// markdown transcript) or from the opencode SQLite database, both of which
// are reduced to this common shape before replay.
type Session struct {
	ID          string
	Slug        string
	Title       string
	Directory   string
	TimeCreated time.Time
	TimeUpdated time.Time
	ParentID    string
	Messages    []Message
}

// Message is a single turn in a session.
type Message struct {
	ID          string
	SessionID   string
	Role        string
	TimeCreated time.Time
	Parts       []Part
}

// Part is a single part of a message. Part.Type mirrors the opencode part
// taxonomy ("text", "reasoning", "tool", "subtask", "file").
type Part struct {
	ID          string
	MessageID   string
	Type        string
	ToolName    string // set when Type == "tool"
	Text        string
	Input       []byte // raw JSON input of a tool call
	Output      []byte // raw output/error of a tool call
	Status      string // tool state.status: pending|running|completed|error
	ChildID     string // resolved child session id (task tool / subtask)
	Prompt      string // prompt text for subtask/task parts
	Description string
	Agent       string
}

// Lookup is the set of ways a session can be referenced by a user.
type Lookup struct {
	// Name matches session slug ("name"), e.g. "swift-pixel".
	Name string
	// ID matches the full session id, e.g. "ses_....".
	ID string
	// Title is a substring match against session title.
	Title string
	// Prompt matches the initial user prompt of a session.
	Prompt string
}

// Source identifies where a session was loaded from.
type Source string

const (
	SourceChat Source = "chat-text"
	SourceDB   Source = "opencode-db"
)
