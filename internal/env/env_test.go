package env

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOsEnv_Get(t *testing.T) {
	env := &osEnv{}

	// Test getting an existing environment variable
	t.Setenv("TEST_VAR", "test_value")

	value := env.Get("TEST_VAR")
	require.Equal(t, "test_value", value)

	// Test getting a non-existent environment variable
	value = env.Get("NON_EXISTENT_VAR")
	require.Equal(t, "", value)
}

func TestOsEnv_Env(t *testing.T) {
	env := &osEnv{}

	envVars := env.Env()

	// Environment should not be empty in normal circumstances
	require.NotNil(t, envVars)
	require.Greater(t, len(envVars), 0)

	// Each environment variable should be in key=value format
	for _, envVar := range envVars {
		require.Contains(t, envVar, "=")
	}
}

func TestNewFromMap(t *testing.T) {
	testMap := map[string]string{
		"KEY1": "value1",
		"KEY2": "value2",
	}

	env := NewFromMap(testMap)
	require.NotNil(t, env)
	require.IsType(t, &mapEnv{}, env)
}

func TestMapEnv_Get(t *testing.T) {
	testMap := map[string]string{
		"KEY1": "value1",
		"KEY2": "value2",
	}

	env := NewFromMap(testMap)

	// Test getting existing keys
	require.Equal(t, "value1", env.Get("KEY1"))
	require.Equal(t, "value2", env.Get("KEY2"))

	// Test getting non-existent key
	require.Equal(t, "", env.Get("NON_EXISTENT"))
}

func TestMapEnv_Env(t *testing.T) {
	t.Run("with values", func(t *testing.T) {
		testMap := map[string]string{
			"KEY1": "value1",
			"KEY2": "value2",
		}

		env := NewFromMap(testMap)
		envVars := env.Env()

		require.Len(t, envVars, 2)

		// Convert to map for easier testing (order is not guaranteed)
		envMap := make(map[string]string)
		for _, envVar := range envVars {
			parts := strings.SplitN(envVar, "=", 2)
			require.Len(t, parts, 2)
			envMap[parts[0]] = parts[1]
		}

		require.Equal(t, "value1", envMap["KEY1"])
		require.Equal(t, "value2", envMap["KEY2"])
	})

	t.Run("empty map", func(t *testing.T) {
		env := NewFromMap(map[string]string{})
		envVars := env.Env()
		require.NotNil(t, envVars)
		require.Len(t, envVars, 0)
	})

	t.Run("nil map", func(t *testing.T) {
		env := NewFromMap(nil)
		envVars := env.Env()
		require.NotNil(t, envVars)
		require.Len(t, envVars, 0)
	})
}

func TestMapEnv_GetEmptyValue(t *testing.T) {
	testMap := map[string]string{
		"EMPTY_KEY":  "",
		"NORMAL_KEY": "value",
	}

	env := NewFromMap(testMap)

	// Test that empty values are returned correctly
	require.Equal(t, "", env.Get("EMPTY_KEY"))
	require.Equal(t, "value", env.Get("NORMAL_KEY"))
}

func TestMapEnv_EnvFormat(t *testing.T) {
	testMap := map[string]string{
		"KEY_WITH_EQUALS": "value=with=equals",
		"KEY_WITH_SPACES": "value with spaces",
	}

	env := NewFromMap(testMap)
	envVars := env.Env()

	require.Len(t, envVars, 2)

	// Check that the format is correct even with special characters
	found := make(map[string]bool)
	for _, envVar := range envVars {
		if envVar == "KEY_WITH_EQUALS=value=with=equals" {
			found["equals"] = true
		}
		if envVar == "KEY_WITH_SPACES=value with spaces" {
			found["spaces"] = true
		}
	}

	require.True(t, found["equals"], "Should handle values with equals signs")
	require.True(t, found["spaces"], "Should handle values with spaces")
}

// While Crush is renamed Klash, a KLASH_ setting falls back to the CRUSH_
// one it replaces, and wins when both are set.
func TestLookupEnvFallsBackToTheNameBeforeTheRename(t *testing.T) {
	t.Run("only the old name", func(t *testing.T) {
		t.Setenv("CRUSH_ENV_TEST_SETTING", "old")
		v, ok := LookupEnv("KLASH_ENV_TEST_SETTING")
		require.True(t, ok)
		require.Equal(t, "old", v)
		require.Equal(t, "old", Getenv("KLASH_ENV_TEST_SETTING"))
	})
	t.Run("both names, the new one wins", func(t *testing.T) {
		t.Setenv("CRUSH_ENV_TEST_SETTING", "old")
		t.Setenv("KLASH_ENV_TEST_SETTING", "new")
		require.Equal(t, "new", Getenv("KLASH_ENV_TEST_SETTING"))
	})
	t.Run("the new name set empty still wins", func(t *testing.T) {
		t.Setenv("CRUSH_ENV_TEST_SETTING", "1")
		t.Setenv("KLASH_ENV_TEST_SETTING", "")
		v, ok := LookupEnv("KLASH_ENV_TEST_SETTING")
		require.True(t, ok)
		require.Empty(t, v)
	})
	t.Run("neither name", func(t *testing.T) {
		_, ok := LookupEnv("KLASH_ENV_TEST_UNSET")
		require.False(t, ok)
	})
	t.Run("other names have no fallback", func(t *testing.T) {
		t.Setenv("CRUSH_ENV_TEST_OTHER", "x")
		_, ok := LookupEnv("ENV_TEST_OTHER")
		require.False(t, ok)
		t.Setenv("ENV_TEST_PLAIN", "plain")
		require.Equal(t, "plain", Getenv("ENV_TEST_PLAIN"))
	})
}

func TestLookupBool(t *testing.T) {
	for _, tt := range []struct {
		name      string
		value     *string
		want, set bool
	}{
		{name: "unset", value: nil, want: false, set: false},
		{name: "empty means unset", value: new(""), want: false, set: false},
		{name: "not a boolean means unset", value: new("maybe"), want: false, set: false},
		{name: "true", value: new("1"), want: true, set: true},
		{name: "false is a value", value: new("0"), want: false, set: true},
		{name: "false spelled out", value: new("false"), want: false, set: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CRUSH_LOOKUP_BOOL_TEST", "")
			os.Unsetenv("CRUSH_LOOKUP_BOOL_TEST")
			t.Setenv("KLASH_LOOKUP_BOOL_TEST", "")
			os.Unsetenv("KLASH_LOOKUP_BOOL_TEST")
			if tt.value != nil {
				t.Setenv("KLASH_LOOKUP_BOOL_TEST", *tt.value)
			}
			got, set := LookupBool("KLASH_LOOKUP_BOOL_TEST")
			if got != tt.want || set != tt.set {
				t.Fatalf("LookupBool = (%v, %v), want (%v, %v)", got, set, tt.want, tt.set)
			}
		})
	}
}

// TestLookupBoolFallsBackToCrush keeps a setup that still exports the old
// CRUSH_ name working, as LookupEnv does.
func TestLookupBoolFallsBackToCrush(t *testing.T) {
	t.Setenv("KLASH_LOOKUP_BOOL_FALLBACK", "")
	os.Unsetenv("KLASH_LOOKUP_BOOL_FALLBACK")
	t.Setenv("CRUSH_LOOKUP_BOOL_FALLBACK", "1")
	got, set := LookupBool("KLASH_LOOKUP_BOOL_FALLBACK")
	if !got || !set {
		t.Fatalf("LookupBool = (%v, %v), want (true, true) from the CRUSH_ name", got, set)
	}
}
