package main

import (
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

const checkpointLines = `# HELP highest_known_checkpoint Highest known checkpoint
# TYPE highest_known_checkpoint gauge
highest_known_checkpoint 402882990
# HELP highest_synced_checkpoint Highest synced checkpoint
# TYPE highest_synced_checkpoint gauge
highest_synced_checkpoint 402882962
`

const dbOpBlock = `# HELP db_op db_op
# TYPE db_op histogram
db_op_bucket{ks="objects",op="get",le="5"} 1
db_op_bucket{ks="objects",op="get",le="+Inf"} 2
db_op_sum{ks="objects",op="get"} 12
db_op_count{ks="objects",op="get"} 2
`

// RocksDB nodes expose each family once.
var rocksdbMetrics = dbOpBlock + checkpointLines + `# HELP highest_synced_checkpoint_age Seconds since the last synced checkpoint
# TYPE highest_synced_checkpoint_age gauge
highest_synced_checkpoint_age 3
`

// Tidehunter nodes expose db_op once per DB registry.
var tidehunterMetrics = dbOpBlock + `# HELP uptime uptime
# TYPE uptime counter
uptime 8428
` + dbOpBlock + checkpointLines + dbOpBlock

func TestParseReaderCheckpoints(t *testing.T) {
	for name, metrics := range map[string]string{"rocksdb": rocksdbMetrics, "tidehunter": tidehunterMetrics} {
		t.Run(name, func(t *testing.T) {
			ch := make(chan *dto.MetricFamily, len(checkpointMetrics))
			if err := parseReader(strings.NewReader(metrics), ch); err != nil {
				t.Fatalf("parseReader: %v", err)
			}
			close(ch)

			got := map[string]float64{}
			for f := range ch {
				got[f.GetName()] = f.GetMetric()[0].GetGauge().GetValue()
			}
			if got["highest_known_checkpoint"] != 402882990 {
				t.Errorf("highest_known_checkpoint = %v, want 402882990", got["highest_known_checkpoint"])
			}
			if got["highest_synced_checkpoint"] != 402882962 {
				t.Errorf("highest_synced_checkpoint = %v, want 402882962", got["highest_synced_checkpoint"])
			}
		})
	}
}

func TestTidehunterMetricsRejectedByStrictParser(t *testing.T) {
	var parser expfmt.TextParser
	if _, err := parser.TextToMetricFamilies(strings.NewReader(tidehunterMetrics)); err == nil {
		t.Fatal("strict parser accepted repeated db_op families; the fixture no longer reproduces the bug")
	}
}

func TestParseReaderMissingCheckpoints(t *testing.T) {
	ch := make(chan *dto.MetricFamily, len(checkpointMetrics))
	if err := parseReader(strings.NewReader(dbOpBlock+dbOpBlock), ch); err == nil {
		t.Fatal("parseReader returned no error for a page without checkpoint metrics")
	}
	if len(ch) != 0 {
		t.Fatalf("parseReader sent %d families for a page without checkpoint metrics", len(ch))
	}
}

func TestMetricLineName(t *testing.T) {
	cases := map[string]string{
		"# HELP highest_known_checkpoint Highest known checkpoint": "highest_known_checkpoint",
		"# TYPE db_op histogram":                                   "db_op",
		`db_op_bucket{ks="objects",le="5"} 1`:                      "db_op_bucket",
		"highest_synced_checkpoint 402882962":                      "highest_synced_checkpoint",
		"# some comment":                                           "",
		"":                                                         "",
	}
	for line, want := range cases {
		if got := metricLineName(line); got != want {
			t.Errorf("metricLineName(%q) = %q, want %q", line, got, want)
		}
	}
}
