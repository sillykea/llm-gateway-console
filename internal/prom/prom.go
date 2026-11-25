// Package prom reads the gateway's metrics from Prometheus.
//
// The console asks Prometheus rather than scraping gateway pods itself:
// Prometheus already sums across replicas and keeps history through
// restarts, which per-pod counters can't. Only fixed queries are sent; the
// browser never gets to run arbitrary PromQL.
package prom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
)

// Client queries one Prometheus server.
type Client struct {
	base   *url.URL
	http   *http.Client
	now    func() time.Time
	Window time.Duration // how far back "recent" numbers look
}

// New returns a client for the Prometheus at baseURL.
func New(baseURL string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("prometheus url %q must be an http(s) URL", baseURL)
	}
	return &Client{base: u, http: &http.Client{Timeout: 10 * time.Second}, now: time.Now, Window: 24 * time.Hour}, nil
}

// Sample is one labelled value.
type Sample struct {
	Labels map[string]string
	Value  float64
}

// Point is one value in a time series.
type Point struct {
	Time  time.Time `json:"t"`
	Value float64   `json:"v"`
}

type apiResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
			Values [][2]any          `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

func (c *Client) get(ctx context.Context, path string, q url.Values) (*apiResponse, error) {
	u := c.base.JoinPath(path)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r apiResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&r); err != nil {
		return nil, fmt.Errorf("prometheus: status %d: %w", resp.StatusCode, err)
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("prometheus: %s", r.Error)
	}
	return &r, nil
}

func parseValue(v any) (float64, error) {
	s, ok := v.(string)
	if !ok {
		return 0, errors.New("prometheus: value is not a string")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if f != f { // NaN, e.g. 0/0 for a hit ratio with no traffic
		return 0, nil
	}
	return f, nil
}

func parseTime(v any) (time.Time, error) {
	f, ok := v.(float64)
	if !ok {
		return time.Time{}, errors.New("prometheus: timestamp is not a number")
	}
	return time.Unix(0, int64(f*float64(time.Second))).UTC(), nil
}

// Query runs an instant query and returns a vector.
func (c *Client) Query(ctx context.Context, promql string) ([]Sample, error) {
	r, err := c.get(ctx, "/api/v1/query", url.Values{"query": {promql}})
	if err != nil {
		return nil, err
	}
	if r.Data.ResultType != "vector" {
		return nil, fmt.Errorf("prometheus: want a vector, got %q", r.Data.ResultType)
	}
	out := make([]Sample, 0, len(r.Data.Result))
	for _, res := range r.Data.Result {
		v, err := parseValue(res.Value[1])
		if err != nil {
			return nil, err
		}
		out = append(out, Sample{Labels: res.Metric, Value: v})
	}
	return out, nil
}

// Scalar runs an instant query that sums to one number. No result is 0.
func (c *Client) Scalar(ctx context.Context, promql string) (float64, error) {
	s, err := c.Query(ctx, promql)
	if err != nil || len(s) == 0 {
		return 0, err
	}
	return s[0].Value, nil
}

// Range runs a range query for one series. Gaps come back as missing points.
func (c *Client) Range(ctx context.Context, promql string, start, end time.Time, step time.Duration) ([]Point, error) {
	r, err := c.get(ctx, "/api/v1/query_range", url.Values{
		"query": {promql},
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
		"step":  {strconv.Itoa(int(step.Seconds()))},
	})
	if err != nil {
		return nil, err
	}
	if r.Data.ResultType != "matrix" {
		return nil, fmt.Errorf("prometheus: want a matrix, got %q", r.Data.ResultType)
	}
	out := []Point{}
	if len(r.Data.Result) == 0 {
		return out, nil
	}
	for _, v := range r.Data.Result[0].Values {
		t, err := parseTime(v[0])
		if err != nil {
			return nil, err
		}
		f, err := parseValue(v[1])
		if err != nil {
			return nil, err
		}
		out = append(out, Point{Time: t, Value: f})
	}
	return out, nil
}

// promDuration renders d the way PromQL expects, in whole seconds.
func promDuration(d time.Duration) string {
	return strconv.Itoa(max(60, int(d.Seconds()))) + "s"
}

// MonthToDate is the time since the start of the current UTC month, which is
// what the gateway's budgets count against.
func (c *Client) MonthToDate() time.Duration {
	now := c.now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return now.Sub(start)
}

// Overview is everything on the console's front page.
type Overview struct {
	SpendMonthUSD   float64            `json:"spend_month_usd"`
	Requests        float64            `json:"requests"`
	ErrorRate       float64            `json:"error_rate"`
	CacheHitRate    float64            `json:"cache_hit_rate"`
	Fallbacks       float64            `json:"fallbacks"`
	P95Seconds      float64            `json:"p95_seconds"`
	OpenBreakers    []string           `json:"open_breakers"`
	RequestsByHour  []Point            `json:"requests_by_hour"`
	SpendByDay      []Point            `json:"spend_by_day"`
	SpendByKey      map[string]float64 `json:"spend_by_key"`
	RequestsByModel map[string]float64 `json:"requests_by_model"`
	WindowHours     int                `json:"window_hours"`
	GeneratedAt     time.Time          `json:"generated_at"`
}

// SpendByKey returns month-to-date spend per key name.
func (c *Client) SpendByKey(ctx context.Context) (map[string]float64, error) {
	s, err := c.Query(ctx, fmt.Sprintf(`sum by (key) (increase(llm_gateway_cost_usd_total[%s]))`, promDuration(c.MonthToDate())))
	if err != nil {
		return nil, err
	}
	out := map[string]float64{}
	for _, x := range s {
		out[x.Labels["key"]] = x.Value
	}
	return out, nil
}

// Overview runs the front page queries. Any failure fails the whole thing:
// a dashboard with some numbers silently missing is worse than an error.
func (c *Client) Overview(ctx context.Context) (*Overview, error) {
	w := promDuration(c.Window)
	o := &Overview{WindowHours: int(c.Window.Hours()), GeneratedAt: c.now().UTC()}
	var err error
	scalar := func(dst *float64, q string) {
		if err == nil {
			*dst, err = c.Scalar(ctx, q)
		}
	}
	scalar(&o.SpendMonthUSD, fmt.Sprintf(`sum(increase(llm_gateway_cost_usd_total[%s]))`, promDuration(c.MonthToDate())))
	scalar(&o.Requests, fmt.Sprintf(`sum(increase(llm_gateway_requests_total[%s]))`, w))
	scalar(&o.ErrorRate, fmt.Sprintf(`sum(increase(llm_gateway_requests_total{status!="ok"}[%[1]s])) / sum(increase(llm_gateway_requests_total[%[1]s]))`, w))
	scalar(&o.CacheHitRate, fmt.Sprintf(`sum(increase(llm_gateway_cache_hits_total[%[1]s])) / (sum(increase(llm_gateway_cache_hits_total[%[1]s])) + sum(increase(llm_gateway_cache_misses_total[%[1]s])))`, w))
	scalar(&o.Fallbacks, fmt.Sprintf(`sum(increase(llm_gateway_fallbacks_total[%s]))`, w))
	scalar(&o.P95Seconds, `histogram_quantile(0.95, sum by (le) (rate(llm_gateway_request_duration_seconds_bucket[1h])))`)
	if err != nil {
		return nil, err
	}

	breakers, err := c.Query(ctx, `max by (provider) (llm_gateway_breaker_open) > 0`)
	if err != nil {
		return nil, err
	}
	o.OpenBreakers = []string{}
	for _, b := range breakers {
		o.OpenBreakers = append(o.OpenBreakers, b.Labels["provider"])
	}
	sort.Strings(o.OpenBreakers)

	byModel, err := c.Query(ctx, fmt.Sprintf(`sum by (alias) (increase(llm_gateway_requests_total[%s]))`, w))
	if err != nil {
		return nil, err
	}
	o.RequestsByModel = map[string]float64{}
	for _, m := range byModel {
		o.RequestsByModel[m.Labels["alias"]] = m.Value
	}

	if o.SpendByKey, err = c.SpendByKey(ctx); err != nil {
		return nil, err
	}

	end := c.now().UTC().Truncate(time.Hour)
	if o.RequestsByHour, err = c.Range(ctx, `sum(increase(llm_gateway_requests_total[1h]))`, end.Add(-23*time.Hour), end, time.Hour); err != nil {
		return nil, err
	}
	day := c.now().UTC().Truncate(24 * time.Hour)
	if o.SpendByDay, err = c.Range(ctx, `sum(increase(llm_gateway_cost_usd_total[1d]))`, day.Add(-13*24*time.Hour), day, 24*time.Hour); err != nil {
		return nil, err
	}
	return o, nil
}
