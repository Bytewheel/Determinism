package replay

import (
	"encoding/json"
	"fmt"
)

// The four tool kinds that determinism can replay: bash, write, edit,
// apply_patch. Everything else is recorded and skipped.

type BashInput struct {
	Command string `json:"command"`
	Workdir string `json:"workdir"`
	Timeout int    `json:"timeout"`
}

type WriteInput struct {
	// DB sessions may store filePath or path depending on opencode version.
	FilePath string `json:"filePath"`
	Path     string `json:"path"`
	Content  string `json:"content"`
}

type EditInput struct {
	FilePath   string `json:"filePath"`
	Path       string `json:"path"`
	OldString  string `json:"oldString"`
	NewString  string `json:"newString"`
	ReplaceAll bool   `json:"replaceAll"`
}

type PatchInput struct {
	PatchText string `json:"patchText"`
}

// TaskInput is the subprompt input for the task tool.
type TaskInput struct {
	Description string `json:"description"`
	SubagentType string `json:"subagent_type"`
	Prompt      string `json:"prompt"`
}

func decode[T any](raw []byte) (T, error) {
	var v T
	if len(raw) == 0 {
		return v, fmt.Errorf("missing input")
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, err
	}
	return v, nil
}

// DecodeInput decodes the raw JSON input of a part based on the tool name.
func DecodeInput(partType, toolName string, raw []byte) (any, error) {
	if partType == "subtask" {
		return TaskInput{}, nil
	}
	switch toolName {
	case "bash":
		return decode[BashInput](raw)
	case "write":
		return decode[WriteInput](raw)
	case "edit":
		return decode[EditInput](raw)
	case "apply_patch":
		return decode[PatchInput](raw)
	case "task":
		return decode[TaskInput](raw)
	default:
		return nil, fmt.Errorf("no decoder for tool %q", toolName)
	}
}
