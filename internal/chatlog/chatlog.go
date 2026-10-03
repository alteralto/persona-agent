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
	if strings.HasPrefix(line, "[AutoClef]") {
		return Message{}, false // the mod's own status lines
	}
	for _, re := range []*regexp.Regexp{vanilla, plugin} {
		if m := re.FindStringSubmatch(line); m != nil {
			text := strings.TrimSpace(m[2])
			if text == "" {
				return Message{}, false
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
