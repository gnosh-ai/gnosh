// Package shellquote writes text so that a POSIX shell reads it as one
// word: for a command line gnosh shows a person to copy, and for the
// command it hands ssh for another machine.
package shellquote

import "strings"

// plain are the bytes a word may hold and still be read as written, with
// no quotes around it.
const plain = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-./=:,+@%"

// Word writes s as one word: as it is when the shell reads it so, and in
// single quotes otherwise.
func Word(s string) string {
	if s != "" && strings.Trim(s, plain) == "" {
		return s
	}
	return Quote(s)
}

// Quote puts s in single quotes, whatever it holds, so that the shell
// takes it as one word and reads nothing in it. A single quote inside is
// written as four characters: one that ends the quotes, a backslash and a
// quote, and one that opens them again.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Words writes each of words as one word, with a space between them.
func Words(words []string) string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = Word(w)
	}
	return strings.Join(out, " ")
}
