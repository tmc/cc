package ccdistill

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Filter reports whether a Sample is worth keeping. The package ships
// [SignalFilter]; callers compose filters from configuration.
type Filter func(*Sample) bool

// SignalFilter returns a Filter that keeps Samples with at least one assistant
// turn, at least one human (non-tool-result) user turn, and at least
// minToolUses tool calls. A minToolUses of zero keeps tool-free chats.
func SignalFilter(minToolUses int) Filter {
	return func(s *Sample) bool {
		if s == nil {
			return false
		}
		hasAssistant := false
		hasHuman := false
		for _, t := range s.Turns {
			switch t.Role {
			case RoleAssistant:
				hasAssistant = true
			case RoleUser:
				if hasText(t.Blocks) {
					hasHuman = true
				}
			}
		}
		if !hasAssistant || !hasHuman {
			return false
		}
		return s.Meta.NumToolUses >= minToolUses
	}
}

// Window trims s to at most maxTurns by keeping the head (a leading system turn,
// if any, plus the first user turn) and the tail, dropping the middle and
// splicing in an assistant turn that marks how many turns were elided. A
// non-positive maxTurns, or a Sample already within the limit, is a no-op.
// Window reports whether the Sample was modified. Meta.NumTurns is left as the
// original count; the elision marker documents the cut.
func Window(s *Sample, maxTurns int) bool {
	if s == nil || maxTurns <= 0 || len(s.Turns) <= maxTurns {
		return false
	}

	var head []Turn
	rest := s.Turns
	if len(rest) > 0 && rest[0].Role == RoleSystem {
		head = append(head, rest[0])
		rest = rest[1:]
	}
	if len(rest) > 0 {
		head = append(head, rest[0])
		rest = rest[1:]
	}

	// Budget for the tail after the head and the single elision marker turn.
	tailLen := maxTurns - len(head) - 1
	if tailLen < 0 {
		tailLen = 0
	}
	if tailLen > len(rest) {
		tailLen = len(rest)
	}
	tail := rest[len(rest)-tailLen:]
	elided := len(rest) - tailLen
	if elided <= 0 {
		return false
	}

	marker := Turn{
		Role:   RoleAssistant,
		Blocks: []Block{{Kind: KindText, Text: fmt.Sprintf("[... %d turns elided ...]", elided)}},
	}

	out := make([]Turn, 0, len(head)+1+len(tail))
	out = append(out, head...)
	out = append(out, marker)
	out = append(out, tail...)
	s.Turns = out
	return true
}

// FingerprintFirstUser returns a stable hex hash of the first human user turn's
// text, for cross-run dedup. It returns "" when the Sample has no human user
// turn, so such Samples are never deduped.
func FingerprintFirstUser(s *Sample) string {
	if s == nil {
		return ""
	}
	text := firstPrompt(s.Turns)
	if text == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
