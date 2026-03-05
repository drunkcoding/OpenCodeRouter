package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"opencoderouter/internal/tui/model"
)

const (
	inspectPreviewMaxLines  = 12
	inspectPreviewMaxRunes  = 1800
	inspectFetchMaxAttempts = 3
	inspectFetchRetryDelay  = 120 * time.Millisecond
)

var inspectANSIRegex = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

type exportedSession struct {
	Messages []exportedMessage `json:"messages"`
}

type exportedMessage struct {
	Info  exportedMessageInfo `json:"info"`
	Parts []exportedPart      `json:"parts"`
}

type exportedMessageInfo struct {
	Role string `json:"role"`
}

type exportedPart struct {
	Type    string            `json:"type"`
	Text    string            `json:"text"`
	Ignored bool              `json:"ignored"`
	State   exportedPartState `json:"state"`
}

type exportedPartState struct {
	Status string `json:"status"`
	Output string `json:"output"`
}

func (s *ProbeService) FetchSessionInspectLatestBlock(ctx context.Context, host model.Host, session model.Session) (string, error) {
	if strings.TrimSpace(session.ID) == "" {
		return "", errors.New("session id is missing")
	}
	if strings.TrimSpace(session.Directory) == "" {
		return "", errors.New("session directory is missing")
	}

	remoteCmd := buildInspectRemoteCmd(host, session)
	args := s.buildSSHArgs(host, remoteCmd)
	args = stripTTYArgs(args)

	var lastParseErr error
	for attempt := 1; attempt <= inspectFetchMaxAttempts; attempt++ {
		raw, err := s.runner.Run(ctx, "ssh", args...)
		if err != nil {
			return "", fmt.Errorf("fetch session inspect export: %w", err)
		}

		block, parseErr := extractLatestConversationBlock(raw)
		if parseErr == nil {
			return block, nil
		}

		lastParseErr = parseErr
		if attempt == inspectFetchMaxAttempts || ctx.Err() != nil {
			break
		}

		timer := time.NewTimer(inspectFetchRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", fmt.Errorf("fetch session inspect export canceled: %w", ctx.Err())
		case <-timer.C:
		}
	}

	if ctx.Err() != nil {
		return "", fmt.Errorf("fetch session inspect export canceled: %w", ctx.Err())
	}

	return "", fmt.Errorf("parse session inspect export: %w", lastParseErr)
}

func buildInspectRemoteCmd(host model.Host, session model.Session) string {
	bin := strings.TrimSpace(host.OpencodeBin)
	if bin == "" {
		bin = "opencode"
	}

	binEsc := shellEscapeSingleQuotes(bin)
	dirEsc := shellEscapeSingleQuotes(session.Directory)
	idEsc := shellEscapeSingleQuotes(session.ID)

	return fmt.Sprintf("set -e; OC=$(command -v '%s' || echo \"$HOME/.opencode/bin/%s\"); cd '%s' && \"$OC\" export '%s'", binEsc, binEsc, dirEsc, idEsc)
}

func shellEscapeSingleQuotes(input string) string {
	return strings.ReplaceAll(input, "'", "'\"'\"'")
}

func stripTTYArgs(args []string) []string {
	result := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "-t" {
			continue
		}
		result = append(result, arg)
	}
	return result
}

func extractLatestConversationBlock(raw []byte) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", nil
	}

	var payload exportedSession
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		start := bytes.IndexByte(trimmed, '{')
		end := bytes.LastIndexByte(trimmed, '}')
		if start >= 0 && end > start {
			if recoverErr := json.Unmarshal(trimmed[start:end+1], &payload); recoverErr == nil {
				err = nil
			} else {
				return "", err
			}
		} else {
			return "", err
		}
	}

	toolFallback := ""
	for i := len(payload.Messages) - 1; i >= 0; i-- {
		msg := payload.Messages[i]
		for j := len(msg.Parts) - 1; j >= 0; j-- {
			part := msg.Parts[j]
			switch part.Type {
			case "text":
				text := strings.TrimSpace(part.Text)
				if text == "" || part.Ignored {
					continue
				}
				return clampConversationBlock(text), nil
			case "tool":
				if toolFallback != "" {
					continue
				}
				if !strings.EqualFold(strings.TrimSpace(part.State.Status), "completed") {
					continue
				}
				out := strings.TrimSpace(part.State.Output)
				if out == "" {
					continue
				}
				toolFallback = clampConversationBlock(out)
			}
		}
	}

	return toolFallback, nil
}

func clampConversationBlock(input string) string {
	cleaned := strings.ReplaceAll(input, "\r\n", "\n")
	cleaned = strings.ReplaceAll(cleaned, "\r", "\n")
	cleaned = inspectANSIRegex.ReplaceAllString(cleaned, "")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return ""
	}

	lines := strings.Split(cleaned, "\n")
	if len(lines) > inspectPreviewMaxLines {
		cleaned = strings.Join(lines[:inspectPreviewMaxLines], "\n") + "\n..."
	} else {
		cleaned = strings.Join(lines, "\n")
	}

	runes := []rune(cleaned)
	if len(runes) > inspectPreviewMaxRunes {
		return string(runes[:inspectPreviewMaxRunes]) + "..."
	}

	return cleaned
}
