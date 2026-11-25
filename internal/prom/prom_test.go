package prom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeProm answers instant queries from a table of query substrings, and
// range queries with three points.
func fakeProm(t *testing.T, answers map[string]string, fail *bool) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail != nil && *fail {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": "bad_data: parse error"})
			return
		}
		q := r.URL.Query().Get("query")
		if r.URL.Path == "/api/v1/query_range" {
			w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[1700000000,"1"],[1700003600,"2"],[1700007200,"NaN"]]}]}}`))
			return
		}
		// The longest matching pattern wins, so overlapping patterns are
		// deterministic (map order is random).
		best := ""
		for sub := range answers {
			if strings.Contains(q, sub) && len(sub) > len(best) {
				best = sub
			}
		}
		if best != "" {
			w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":` + answers[best] + `}}`))
			return
		}
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC) }
	return c
}

func vec(labels, value string) string {
	return `[{"metric":` + labels + `,"value":[1700000000,"` + value + `"]}]`
}

func TestOverview(t *testing.T) {
	c := fakeProm(t, map[string]string{
		`sum(increase(llm_gateway_cost_usd_total`:             vec(`{}`, "12.5"),
		`sum by (key) (increase(llm_gateway_cost_usd_total`:   `[{"metric":{"key":"team-a"},"value":[0,"10"]},{"metric":{"key":"team-b"},"value":[0,"2.5"]}]`,
		`llm_gateway_requests_total{status!="ok"}`:            vec(`{}`, "0.02"),
		`sum(increase(llm_gateway_requests_total`:             vec(`{}`, "1000"),
		`llm_gateway_cache_hits_total`:                        vec(`{}`, "NaN"),
		`llm_gateway_fallbacks_total`:                         vec(`{}`, "7"),
		`histogram_quantile`:                                  vec(`{}`, "1.8"),
		`llm_gateway_breaker_open`:                            `[{"metric":{"provider":"openai"},"value":[0,"1"]}]`,
		`sum by (alias) (increase(llm_gateway_requests_total`: `[{"metric":{"alias":"chat-default"},"value":[0,"900"]}]`,
	}, nil)

	o, err := c.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if o.SpendMonthUSD != 12.5 || o.Requests != 1000 || o.ErrorRate != 0.02 || o.Fallbacks != 7 || o.P95Seconds != 1.8 {
		t.Errorf("scalars: %+v", o)
	}
	if o.CacheHitRate != 0 {
		t.Errorf("NaN (no cache traffic) should read as 0, got %v", o.CacheHitRate)
	}
	if len(o.OpenBreakers) != 1 || o.OpenBreakers[0] != "openai" {
		t.Errorf("breakers: %v", o.OpenBreakers)
	}
	if o.SpendByKey["team-a"] != 10 || o.RequestsByModel["chat-default"] != 900 {
		t.Errorf("breakdowns: %v %v", o.SpendByKey, o.RequestsByModel)
	}
	if len(o.RequestsByHour) != 3 || o.RequestsByHour[1].Value != 2 || o.RequestsByHour[2].Value != 0 {
		t.Errorf("series: %+v", o.RequestsByHour)
	}
}

func TestMonthToDate(t *testing.T) {
	c := fakeProm(t, nil, nil)
	if got := c.MonthToDate(); got != (14*24+12)*time.Hour {
		t.Fatalf("month to date = %v", got)
	}
	if promDuration(time.Second) != "60s" {
		t.Fatal("durations should have a 1 minute floor")
	}
}

func TestErrorsPropagate(t *testing.T) {
	fail := true
	c := fakeProm(t, nil, &fail)
	if _, err := c.Overview(context.Background()); err == nil || !strings.Contains(err.Error(), "parse error") {
		t.Fatalf("err = %v", err)
	}
}

func TestBadURL(t *testing.T) {
	for _, u := range []string{"", "prometheus:9090", "ftp://x"} {
		if _, err := New(u); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
}
