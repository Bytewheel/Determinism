package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// ParseChatText parses an opencode transcript in the chat text format emitted
// by the TUI's transcript exporter (see packages/tui/src/util/transcript.ts).
func ParseChatText(path string) (*Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read transcript: %w", err)
	}
	return parseChatText(data)
}

type parseMode int

const (
	modeNone parseMode = iota
	modeTitle
	modeUser
	modeAssistant
	modeTool
	modeThinking
	modeFenceInput
	modeFenceOutput
	modeFenceError
)

func parseChatText(data []byte) (*Session, error) {
	s := &Session{Slug: "chat-transcript"}
	var (
		curMsg    *Message
		curPart   *Part
		mode      parseMode
		lineNo    int
		titleDone bool
	)

	flushPart := func() {
		if curMsg != nil && curPart != nil && curPart.Type != "" {
			curMsg.Parts = append(curMsg.Parts, *curPart)
		}
		curPart = nil
	}
	flushMsg := func() {
		flushPart()
		if curMsg != nil {
			s.Messages = append(s.Messages, *curMsg)
		}
		curMsg = nil
	}
	newPart := func(typ string) *Part {
		flushPart()
		curPart = &Part{Type: typ}
		return curPart
	}
	setMode := func(m parseMode) {
		mode = m
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	for scanner.Scan() {
		lineNo++
		t := strings.TrimRight(scanner.Text(), "\r")
		trimmed := strings.TrimSpace(t)

		// Handle fenced blocks: any line that is exactly ``` opens or closes.
		if strings.HasPrefix(trimmed, "```") {
			if mode == modeFenceInput || mode == modeFenceOutput || mode == modeFenceError {
				// closing fence
				setMode(modeNone)
				continue
			}
			switch {
			case curPart != nil && curPart.Type == "tool" && curPart.Input == nil:
				mode = modeFenceInput
			case curPart != nil && curPart.Type == "tool" && curPart.Output == nil:
				mode = modeFenceOutput
			default:
				// stray fence (e.g. markdown in a text part) — treat as text
				mode = modeFenceOutput
			}
			continue
		}

		// Inside a fenced block: capture raw content.
		if mode == modeFenceInput || mode == modeFenceOutput || mode == modeFenceError {
			if curPart == nil {
				curPart = newPart("text")
			}
			switch mode {
			case modeFenceInput:
				curPart.Input = append(curPart.Input, []byte(t+"\n")...)
			case modeFenceOutput:
				curPart.Output = append(curPart.Output, []byte(t+"\n")...)
			case modeFenceError:
				curPart.Output = append(curPart.Output, []byte(t+"\n")...)
			}
			continue
		}

		// Structural lines.
		switch {
		case trimmed == "":
			continue

		case strings.HasPrefix(trimmed, "# ") && !titleDone:
			s.Title = strings.TrimPrefix(trimmed, "# ")
			titleDone = true
			setMode(modeNone)
			continue

		case strings.HasPrefix(trimmed, "**Session ID:**"):
			s.ID = strings.TrimSpace(strings.TrimPrefix(trimmed, "**Session ID:**"))
			setMode(modeNone)
			continue

		case strings.HasPrefix(trimmed, "**Created:**") || strings.HasPrefix(trimmed, "**Updated:**"):
			setMode(modeNone)
			continue

		case trimmed == "---":
			flushMsg()
			setMode(modeNone)
			continue

		case trimmed == "## User":
			flushMsg()
			curMsg = &Message{Role: "user", TimeCreated: tsFor(lineNo)}
			setMode(modeUser)
			continue

		case strings.HasPrefix(trimmed, "## Assistant"):
			flushMsg()
			curMsg = &Message{Role: "assistant", TimeCreated: tsFor(lineNo)}
			setMode(modeAssistant)
			continue

		case strings.HasPrefix(trimmed, "**Tool: "):
			if curMsg == nil {
				curMsg = &Message{Role: "assistant", TimeCreated: tsFor(lineNo)}
			}
			p := newPart("tool")
			p.ToolName = strings.TrimSuffix(strings.TrimPrefix(trimmed, "**Tool: "), "**")
			setMode(modeTool)
			continue

		case trimmed == "**Input:**":
			if curPart == nil {
				newPart("tool")
			}
			setMode(modeNone)
			continue

		case trimmed == "**Output:**":
			if curPart == nil {
				newPart("tool")
			}
			setMode(modeNone)
			continue

		case trimmed == "**Error:**":
			if curPart == nil {
				newPart("tool")
			}
			setMode(modeNone)
			continue

		case trimmed == "_Thinking:_":
			if curMsg == nil {
				curMsg = &Message{Role: "assistant", TimeCreated: tsFor(lineNo)}
			}
			newPart("reasoning")
			setMode(modeThinking)
			continue
		}

		// Plain content lines.
		switch mode {
		case modeThinking:
			if curPart == nil || curPart.Type != "reasoning" {
				newPart("reasoning")
			}
			curPart.Text += t + "\n"
		case modeNone:
			if curMsg == nil {
				curMsg = &Message{Role: "user", TimeCreated: tsFor(lineNo)}
			}
			if curPart == nil {
				newPart("text")
			}
			if curPart.Type == "reasoning" {
				// reasoning followed by prose: start a fresh text part
				newPart("text")
			}
			curPart.Text += t + "\n"
		default:
			// modeUser/modeAssistant/modeTool: text content
			if curPart == nil || curPart.Type == "tool" {
				newPart("text")
			}
			curPart.Text += t + "\n"
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	flushMsg()
	return s, nil
}

var (
	titleRe   = regexp.MustCompile(`(?m)^#\s+(.+)$`)
	slugStrip = regexp.MustCompile(`[^a-z0-9]+`)
)

// tsFor returns a synthetic, monotonically increasing timestamp for a line so
// ordering stays stable during replay (the chat format has no per-message
// timestamps).
func tsFor(line int) time.Time { return time.UnixMilli(1_700_000_000_000 + int64(line)) }

// ParseToolPart validates and normalizes a tool part captured from the chat
// format. It confirms the input parses as JSON; if the first block was
// actually output text (malformed transcript), it is swapped so callers get a
// best-effort view.
func ParseToolPart(p *Part) error {
	if p.Type != "tool" {
		return nil
	}
	var probe map[string]any
	if err := json.Unmarshal(p.Input, &probe); err != nil {
		// If input does not parse as JSON but output does, they were reversed.
		var outProbe map[string]any
		if json.Unmarshal(p.Output, &outProbe) == nil {
			p.Input, p.Output = p.Output, p.Input
		}
	}
	return nil
}
