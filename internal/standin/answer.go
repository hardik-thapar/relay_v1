package standin

import (
	"fmt"
	"regexp"
	"strings"
)

// The pipeline renders each retrieved chunk on its own line as
// "[<chunk id>] <text>". The stand-in answers by quoting the first chunk it
// sees, so its answers pass verification and load runs measure traffic,
// not redraft storms. With no chunks it refuses, as a real model should.
var chunkLine = regexp.MustCompile(`^\[([^\]\s]+#\d+)\]\s+(.+)$`)

const quoteWords = 12

func answer(msgs []message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "user" {
			continue
		}
		for _, line := range strings.Split(msgs[i].Content, "\n") {
			m := chunkLine.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil {
				continue
			}
			words := strings.Fields(m[2])
			if len(words) > quoteWords {
				words = words[:quoteWords]
			}
			return fmt.Sprintf("The papers state: \"%s\" [%s].", strings.Join(words, " "), m[1])
		}
		break // only the last user message counts
	}
	return "The papers do not say."
}
