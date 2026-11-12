package pipeline

import (
	"fmt"
	"strings"

	"relay/internal/corpus"
	"relay/internal/llm"
)

// systemPrompt states the rules Verify will check, so a model that
// follows them passes.
const systemPrompt = `You answer questions about three research papers using ONLY the chunks given to you.
Rules:
- End every claim with the id of the chunk it comes from, in square brackets, e.g. [1706.03762v7#12].
- Include at least one exact quote of three or more words, copied word for word from a chunk, in double quotes, followed by that chunk's id.
- Do not add anything that is not in the chunks.
- If the chunks do not answer the question, reply exactly: ` + RefusalText + `
- Use at most four sentences. A sentence without a chunk id will be deleted.

Example of the format (the id here is made up; use the ids you are given):
The method avoids saturation [2401.00001v1#7]. The paper states that "values grow large in magnitude" [2401.00001v1#7].`

// Messages builds the draft request. Each chunk is one line,
// "[<id>] <text>": the format the stand-in reads (internal/standin).
func Messages(question string, chunks []corpus.Chunk) []llm.Message {
	var b strings.Builder
	fmt.Fprintf(&b, "Question: %s\n\nChunks:\n", question)
	for _, c := range chunks {
		fmt.Fprintf(&b, "[%s] %s\n", c.ID, c.Text)
	}
	return []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: b.String()},
	}
}

// Redraft asks once more, naming exactly what failed verification.
func Redraft(msgs []llm.Message, draft string, problems []string) []llm.Message {
	out := append([]llm.Message(nil), msgs...)
	return append(out,
		llm.Message{Role: "assistant", Content: draft},
		llm.Message{Role: "user", Content: "Your answer failed these checks:\n- " + strings.Join(problems, "\n- ") +
			"\nRewrite it following the rules. Quote only text that appears exactly in the chunks."},
	)
}

// Token estimate without a tokenizer, calibrated in the probe: about 72
// tokens of chat overhead plus one token per 5 characters (DECISIONS D9).
// The limiter settles the difference once real usage is known.
const (
	overheadTokens = 72
	charsPerToken  = 5
)

func estimateTokens(msgs []llm.Message) int {
	n := overheadTokens
	for _, m := range msgs {
		n += len(m.Content) / charsPerToken
	}
	return n
}
