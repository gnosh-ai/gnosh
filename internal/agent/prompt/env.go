package prompt

import "strings"

// envPointer stands where a system prompt's <env> block was.
const envPointer = "Your environment (working directory, platform, machine, today's date, git status) is in the <env> block at the start of the conversation."

// SplitEnv takes the <env> block out of a built system prompt and returns
// the prompt without it, with a line saying where it went, and the block.
//
// The block is the only part of the system prompt that differs from one
// session to the next with the same config: the working directory, the
// date, git status. A local model server (llama.cpp) reuses what it has
// already read only up to the first token that differs, and the tools are
// rendered after the system prompt, so with the block inside it every new
// session re-reads the whole prompt and every tool: 70-90 s for Qwen on the
// AI box. Sent as the first user message instead, the block comes after the
// system prompt and the tools, which a new session then shares with the
// last one; reading them again took 0.5 s instead of 33 in a test.
func SplitEnv(system string) (rest, env string) {
	start := strings.Index(system, "<env>")
	if start < 0 {
		return system, ""
	}
	end := strings.Index(system[start:], "</env>")
	if end < 0 {
		return system, ""
	}
	end += start + len("</env>")
	return system[:start] + envPointer + system[end:], system[start:end]
}
