package config

import "testing"

func TestMetricsSink(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		opts          *Options
		wantURL, wKey string
	}{
		{"nil defaults to gnosh's analytics", nil, DefaultMetricsURL, "gnosh"},
		{"opted-in with no url goes to gnosh's analytics", &Options{}, DefaultMetricsURL, "gnosh"},
		{"a custom url overrides the default", &Options{MetricsURL: "https://my.collector"}, "https://my.collector", "gnosh"},
		{"url and key", &Options{MetricsURL: "https://my.collector", MetricsKey: "k1"}, "https://my.collector", "k1"},
		{"a key but no url still goes to the default", &Options{MetricsKey: "k1"}, DefaultMetricsURL, "k1"},
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
	if got := (&Options{UpdateRepo: "gnosh-ai/gnosh"}).UpdateCheckRepo(); got != "gnosh-ai/gnosh" {
		t.Fatalf("UpdateCheckRepo() = %q", got)
	}
}
