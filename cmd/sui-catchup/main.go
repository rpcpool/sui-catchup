package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/gosuri/uilive"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"go.uber.org/atomic"
)

var (
	validator_addr  = flag.String("addr", "http://localhost:9187/metrics", "Validator metrics address")
	update_interval = flag.Int("interval", 1, "How often to check in seconds")

	metric_channel chan *dto.MetricFamily = make(chan *dto.MetricFamily, 2)

	highest_known_checkpoint  atomic.Float64
	highest_synced_checkpoint atomic.Float64
	last_delta                atomic.Float64
)

func main() {
	log.SetFlags(0)

	flag.Parse()

	if *validator_addr == "" {
		log.Fatal("Please specify -addr")
	}

	interval := time.Duration(*update_interval) * time.Second

	// Start with the DefaultTransport for sane defaults.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Conservatively disable HTTP keep-alives as this program will only
	// ever need a single HTTP request.
	transport.DisableKeepAlives = true
	// Timeout early if the server doesn't even return the headers.
	transport.ResponseHeaderTimeout = time.Minute

	writer := uilive.New()

	writer.Start()

	// Launch the reader that reads the state
	go monitorChannel(writer)

	// Fetch metrics every `schedule` duration
	ticker := time.NewTicker(interval)

	// Fetch state in a loop
	_, _ = fmt.Fprintf(writer, "")
	var errors int = 0
	for {
		err := fetchMetricFamilies(*validator_addr, metric_channel, transport)
		if err != nil {
			errors++
			str := ""
			for i := 0; i < errors; i++ {
				str += "."
			}
			_, _ = fmt.Fprintf(writer, "Error fetching metrics: %v %s\n", err, str)
			time.Sleep(time.Millisecond * 5)
		} else {
			if highest_known_checkpoint.Load() != 0 {
				if highest_known_checkpoint.Load()-highest_synced_checkpoint.Load() <= 0 {
					break
				}
			}
		}
		<-ticker.C
	}
	if highest_known_checkpoint.Load() != 0 {
		_, _ = fmt.Fprintf(writer, "Node caught up\n")
	}
	writer.Stop()
}

// FetchMetricFamilies retrieves metrics from the provided URL, decodes them
// into MetricFamily proto messages, and sends them to the provided channel. It
// returns after all MetricFamilies have been sent. The provided transport
// may be nil (in which case the default Transport is used).
func fetchMetricFamilies(url string, ch chan<- *dto.MetricFamily, transport http.RoundTripper) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("creating GET request for URL %q failed: %v", url, err)
	}
	//req.Header.Add("Accept", acceptHeader)
	client := http.Client{Transport: transport}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("executing GET request for URL %q failed: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET request for URL %q returned HTTP status %s", url, resp.Status)
	}
	return parseResponse(resp, ch)
}

func monitorChannel(writer *uilive.Writer) {
	for {
		f := <-metric_channel
		switch f.GetName() {
		case "highest_known_checkpoint":
			highest_known_checkpoint.Store(f.GetMetric()[0].GetGauge().GetValue())
		case "highest_synced_checkpoint":
			highest_synced_checkpoint.Store(f.GetMetric()[0].GetGauge().GetValue())
		}

		if highest_known_checkpoint.Load() != 0 && highest_synced_checkpoint.Load() != 0 {
			delta := highest_known_checkpoint.Load() - highest_synced_checkpoint.Load()
			rate := delta - last_delta.Load()
			last_delta.Store(delta)

			var str string
			if rate < 0 {
				str = fmt.Sprintf("catching up at %d/s", -int64(rate)/int64(*update_interval))
			} else {
				str = fmt.Sprintf("falling behind at %d/s", int64(rate)/int64(*update_interval))
			}
			_, _ = fmt.Fprintf(writer, "Catching up, %d checkpoints behind (%s)\n", int64(delta), str)
			time.Sleep(time.Millisecond * 5) // Needed to allow multiple updates
		}
	}
}

func parseReader(in io.Reader, ch chan<- *dto.MetricFamily) error {
	var parser expfmt.TextParser
	metricFamilies, err := parser.TextToMetricFamilies(in)
	if err != nil {
		return fmt.Errorf("reading text format failed: %v", err)
	}

	ch <- metricFamilies["highest_known_checkpoint"]
	ch <- metricFamilies["highest_synced_checkpoint"]

	return nil
}

func parseResponse(resp *http.Response, ch chan<- *dto.MetricFamily) error {
	if err := parseReader(resp.Body, ch); err != nil {
		return err
	}
	return nil
}
