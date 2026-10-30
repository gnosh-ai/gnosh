// Copyright (C) 2026 Jae Kwon
//
// This file is part of klash's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

// Package fmt formats as Go's fmt does. Gno has no fmt, and models write Go,
// so klash's VMs run this one's formatting natively, with Go's own fmt
// (internal/gnovm/fmtnative.go): every verb, flag, width and precision works
// as in Go, %v and %+v print structs, slices and maps, and Errorf wraps
// with %w.
package fmt

import "io"

// sprintf, sprint and sprintln are Go's fmt.Sprintf, Sprint and Sprintln,
// run natively over the operands.
func sprintf(format string, args []any) string
func sprint(args []any) string
func sprintln(args []any) string

// Stringer is implemented by a value that has a String method, which
// formats it for %v and %s.
type Stringer interface {
	String() string
}

// operands applies the Error and String methods that Go's fmt calls: the
// native formatter cannot call methods written in Gno. Each such operand
// goes as a byMethod, its method's text with the value itself, since Go
// uses the text only for %v, %s, %q, %x and %X (%d of a time.Month is 3).
// A method that panics gives <nil>, as Go's does for a nil receiver.
func operands(a []any) []any {
	out := make([]any, len(a))
	for i, x := range a {
		switch v := x.(type) {
		case error:
			out[i] = byMethod{S: method(v.Error), V: x}
		case Stringer:
			out[i] = byMethod{S: method(v.String), V: x}
		default:
			out[i] = x
		}
	}
	return out
}

// byMethod is an operand with an Error or String method: S is the text it
// gave, V the operand.
type byMethod struct {
	S string
	V any
}

func method(f func() string) (s string) {
	defer func() {
		if recover() != nil {
			s = "<nil>"
		}
	}()
	return f()
}

// Sprintf formats according to a format specifier and returns the result.
func Sprintf(format string, a ...any) string { return sprintf(format, operands(a)) }

// Sprint formats its operands in their default formats and returns the
// result; spaces separate operands when neither is a string.
func Sprint(a ...any) string { return sprint(operands(a)) }

// Sprintln formats its operands in their default formats, with spaces
// between them and a newline after, and returns the result.
func Sprintln(a ...any) string { return sprintln(operands(a)) }

// Printf formats according to a format specifier and prints the result.
func Printf(format string, a ...any) (n int, err error) {
	s := Sprintf(format, a...)
	print(s)
	return len(s), nil
}

// Print formats its operands as Sprint does and prints the result.
func Print(a ...any) (n int, err error) {
	s := Sprint(a...)
	print(s)
	return len(s), nil
}

// Println formats its operands as Sprintln does and prints the result.
func Println(a ...any) (n int, err error) {
	s := Sprintln(a...)
	print(s)
	return len(s), nil
}

// Fprintf formats according to a format specifier and writes to w.
func Fprintf(w io.Writer, format string, a ...any) (n int, err error) {
	return w.Write([]byte(Sprintf(format, a...)))
}

// Fprint formats its operands as Sprint does and writes to w.
func Fprint(w io.Writer, a ...any) (n int, err error) {
	return w.Write([]byte(Sprint(a...)))
}

// Fprintln formats its operands as Sprintln does and writes to w.
func Fprintln(w io.Writer, a ...any) (n int, err error) {
	return w.Write([]byte(Sprintln(a...)))
}

// Appendf formats according to a format specifier and appends to b.
func Appendf(b []byte, format string, a ...any) []byte { return append(b, Sprintf(format, a...)...) }

// Append formats its operands as Sprint does and appends to b.
func Append(b []byte, a ...any) []byte { return append(b, Sprint(a...)...) }

// Appendln formats its operands as Sprintln does and appends to b.
func Appendln(b []byte, a ...any) []byte { return append(b, Sprintln(a...)...) }

// Errorf formats according to a format specifier and returns an error with
// that text. Each %w operand is formatted as %v and wrapped: errors.Is and
// errors.As see through to it, and errors.Unwrap returns it if it is the
// only one.
func Errorf(format string, a ...any) error {
	var wrapped []error
	f := []byte(format)
	arg := 0
	for i := 0; i < len(f); i++ {
		if f[i] != '%' {
			continue
		}
		// Find the verb past the flags, width, precision and any explicit
		// operand index, keeping count of the operands used.
		j := i + 1
		for j < len(f) && !isVerb(f[j]) {
			switch f[j] {
			case '[':
				n := 0
				for j+1 < len(f) && f[j+1] >= '0' && f[j+1] <= '9' {
					j++
					n = n*10 + int(f[j]-'0')
				}
				arg = n - 1
			case '*':
				arg++
			}
			j++
		}
		if j == len(f) {
			break
		}
		if f[j] != '%' {
			if f[j] == 'w' {
				f[j] = 'v'
				if arg >= 0 && arg < len(a) {
					if err, ok := a[arg].(error); ok && err != nil {
						wrapped = append(wrapped, err)
					}
				}
			}
			arg++
		}
		i = j
	}
	msg := Sprintf(string(f), a...)
	if len(wrapped) > 1 {
		return &wrapErrors{msg: msg, errs: wrapped}
	}
	e := &wrapError{msg: msg}
	if len(wrapped) == 1 {
		e.err = wrapped[0]
	}
	return e
}

func isVerb(c byte) bool {
	return c == '%' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// wrapError is what Errorf returns, wrapping at most one error.
type wrapError struct {
	msg string
	err error
}

func (e *wrapError) Error() string { return e.msg }
func (e *wrapError) Unwrap() error { return e.err }

// wrapErrors is what Errorf returns when it wraps several.
type wrapErrors struct {
	msg  string
	errs []error
}

func (e *wrapErrors) Error() string   { return e.msg }
func (e *wrapErrors) Unwrap() []error { return e.errs }
