// Package chatlog turns raw chat lines from the game into player messages.
package chatlog

import (
	"regexp"
	"strings"
)

type Message struct {
	Nick string
	Text string
}

var (
	formatCodes = regexp.MustCompile(`§.`)
	// <Nick> text  (vanilla)
	vanilla = regexp.MustCompile(`^<([A-Za-z0-9_]{2,16})>\s*(.+)$`)
	// [Rank] [Clan] Nick: text  /  Nick » text  /  Nick -> text
	plugin = regexp.MustCompile(`^(?:\[[^\]]*\]\s*)*([A-Za-z0-9_]{2,16})\s*(?::|»|->|>)\s*(.+)$`)
)

// Parse extracts nick and text from one chat line; ok=false for system lines.
func Parse(line string) (Message, bool) {
	line = strings.TrimSpace(formatCodes.ReplaceAllString(line, ""))
	for _, mod := range []string{"[AutoClef]", "[Tungsten]", "[Baritone]"} {
		if strings.HasPrefix(line, mod) {
			return Message{}, false // the mods' own status lines ("MovementQueue: off path ...")
		}
	}
	for _, re := range []*regexp.Regexp{vanilla, plugin} {
		if m := re.FindStringSubmatch(line); m != nil {
			text := strings.TrimSpace(m[2])
			if text == "" || logLevels[strings.ToUpper(m[1])] {
				return Message{}, false // "WARNING: Failed! No block path" is the mod, not a player
			}
			return Message{Nick: m[1], Text: text}, true
		}
	}
	return Message{}, false
}

// NewLines returns the lines of cur that were not in prev. Both are tails of the same
// ring buffer (oldest first), so the overlap is a suffix of prev equal to a prefix of cur.
func NewLines(prev, cur []string) []string {
	maxK := len(prev)
	if len(cur) < maxK {
		maxK = len(cur)
	}
	for k := maxK; k > 0; k-- {
		if equal(prev[len(prev)-k:], cur[:k]) {
			return cur[k:]
		}
	}
	return cur
}

func equal(a, b []string) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// logLevels: words that open a mod's log line printed to chat, never a player's name.
var logLevels = map[string]bool{"WARNING": true, "WARN": true, "ERROR": true, "INFO": true, "DEBUG": true, "SEVERE": true}
