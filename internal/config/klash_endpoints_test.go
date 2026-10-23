package config

import "testing"

func TestMetricsSink(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		opts          *Options
		wantURL, wKey string
	}{
		{"nil", nil, "", ""},
		{"empty sends nowhere", &Options{}, "", ""},
		{"url with no key defaults to klash", &Options{MetricsURL: "https://klash.gno.run"}, "https://klash.gno.run", "klash"},
		{"url and key", &Options{MetricsURL: "https://klash.gno.run", MetricsKey: "k1"}, "https://klash.gno.run", "k1"},
		{"a key without a url still sends nowhere", &Options{MetricsKey: "k1"}, "", "k1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			url, key := c.opts.MetricsSink()
			if url != c.wantURL || key != c.wKey {
				t.Fatalf("MetricsSink() = (%q,%q), want (%q,%q)", url, key, c.wantURL, c.wKey)
			}
		})
	}
}

func TestUpdateCheckRepo(t *testing.T) {
	t.Parallel()
	if (*Options)(nil).UpdateCheckRepo() != "" {
		t.Fatal("nil options should have no repo")
	}
	if (&Options{}).UpdateCheckRepo() != "" {
		t.Fatal("unset repo should be empty, so no check and never Charm")
	}
	if got := (&Options{UpdateRepo: "jaekwon/klash"}).UpdateCheckRepo(); got != "jaekwon/klash" {
		t.Fatalf("UpdateCheckRepo() = %q", got)
	}
}
