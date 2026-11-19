package transcript

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"time"
)

// idGen makes UUIDv7 identifiers (RFC 9562): 48 bits of Unix milliseconds,
// 12 bits of the time within the millisecond, and 62 random bits. The ids
// of one generator sort in the order they were made, even when the clock
// stands still or steps back. The zero value is ready to use.
type idGen struct {
	mu   sync.Mutex
	last uint64 // the 60 time bits of the id made last
}

// next makes an id for the moment now, drawing its random bits from r.
func (g *idGen) next(now time.Time, r io.Reader) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	var b [16]byte
	if _, err := io.ReadFull(r, b[8:]); err != nil {
		return "", fmt.Errorf("transcript: reading random bytes for an id: %w", err)
	}
	ms := uint64(now.UnixMilli())
	frac := uint64(now.Nanosecond()%1_000_000) * 4096 / 1_000_000
	tv := ms<<12 | frac

	g.mu.Lock()
	if tv <= g.last {
		tv = g.last + 1
	}
	g.last = tv
	g.mu.Unlock()

	ms, frac = tv>>12, tv&0xfff
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	b[6] = 0x70 | byte(frac>>8)
	b[7] = byte(frac)
	b[8] = 0x80 | b[8]&0x3f
	return formatUUID(b), nil
}

// formatUUID writes sixteen bytes in the usual 8-4-4-4-12 form.
func formatUUID(b [16]byte) string {
	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out[:])
}

// ValidID reports whether s has the form of a session's id, and so can
// stand as a name in a path.
func ValidID(s string) bool { return validID(s) }

// validID reports whether s has the form of a UUID in lower case: 32 hex
// digits in groups of 8, 4, 4, 4 and 12.
func validID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
	}
	return true
}

// idTime returns the time a UUIDv7 was made, and false when id is not one.
func idTime(id string) (time.Time, bool) {
	if !validID(id) || id[14] != '7' {
		return time.Time{}, false
	}
	raw, err := hex.DecodeString(id[0:8] + id[9:13])
	if err != nil {
		return time.Time{}, false
	}
	var ms int64
	for _, c := range raw {
		ms = ms<<8 | int64(c)
	}
	return time.UnixMilli(ms).UTC(), true
}
