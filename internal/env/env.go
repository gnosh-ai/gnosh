package env

import (
	"os"
	"strconv"
	"strings"
)

type Env interface {
	Get(key string) string
	Env() []string
}

type osEnv struct{}

// Get implements Env.
func (o *osEnv) Get(key string) string {
	return os.Getenv(key)
}

func (o *osEnv) Env() []string {
	return os.Environ()
}

func New() Env {
	return &osEnv{}
}

type mapEnv struct {
	m map[string]string
}

// Get implements Env.
func (m *mapEnv) Get(key string) string {
	if value, ok := m.m[key]; ok {
		return value
	}
	return ""
}

// Env implements Env.
func (m *mapEnv) Env() []string {
	env := make([]string, 0, len(m.m))
	for k, v := range m.m {
		env = append(env, k+"="+v)
	}
	return env
}

func NewFromMap(m map[string]string) Env {
	if m == nil {
		m = make(map[string]string)
	}
	return &mapEnv{m: m}
}

// LookupEnv is os.LookupEnv for the program's own settings while it is
// renamed from Crush to Crash: a CRASH_ name that is not set falls back to
// the CRUSH_ name it replaces, so existing setups keep working, and the
// CRASH_ name wins when both are set. Other names are looked up as given.
func LookupEnv(key string) (string, bool) {
	if v, ok := os.LookupEnv(key); ok {
		return v, true
	}
	if name, ok := strings.CutPrefix(key, "CRASH_"); ok {
		return os.LookupEnv("CRUSH_" + name)
	}
	return "", false
}

// LookupBool reads key as a boolean through [LookupEnv]. set is false when
// the variable is absent, empty or not a boolean, so a caller can tell "not
// asked" from "asked for false". The program's switches that reach Charm's
// servers rely on that: unset leaves them off, and only an explicit value
// changes them.
func LookupBool(key string) (value, set bool) {
	s, ok := LookupEnv(key)
	if !ok || s == "" {
		return false, false
	}
	v, err := strconv.ParseBool(s)
	if err != nil {
		return false, false
	}
	return v, true
}

// Getenv is [LookupEnv] without the presence flag, like os.Getenv.
func Getenv(key string) string {
	v, _ := LookupEnv(key)
	return v
}
