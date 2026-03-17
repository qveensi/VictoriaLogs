package remotewrite

import (
	"reflect"
	"strings"
	"testing"

	"github.com/VictoriaMetrics/VictoriaLogs/lib/logstorage"
)

func TestEvaluateTransform(t *testing.T) {
	f := func(config string, row string, expected string) {
		t.Helper()

		cfg, err := unmarshalTransformsConfig([]byte(config))
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}

		p := logstorage.GetJSONParser()
		defer logstorage.PutJSONParser(p)

		if err := p.ParseLogMessage([]byte(row), nil, ""); err != nil {
			t.Fatalf("cannot parse input row %q: %s", row, err)
		}

		src := logstorage.GetLogRows(nil, nil, nil, nil, "")
		src.MustAdd(logstorage.TenantID{}, 0, p.Fields, 0)
		dst := logstorage.GetLogRows(nil, nil, nil, nil, "")
		evaluateTransforms(cfg.transforms, dst, src)

		var got string
		dst.ForEachRow(func(_ uint64, r *logstorage.InsertRow) {
			got += string(r.AppendJSON(nil)) + "\n"
		})
		if strings.TrimSpace(got) != expected {
			t.Fatalf("unexpected result\ngot:\n%s\nwant:\n%s", got, expected)
		}
	}

	// Empty transforms list - log passes through unchanged
	config := `transforms: []`
	in := `{"foo":"bar"}`
	expect := `{"_time":"1970-01-01T00:00:00Z","foo":"bar"}`
	f(config, in, expect)

	// Unpack incoming data
	config = `transforms:
  - pipe: unpack_json from payload | delete payload`
	in = `{"payload":"{\"_msg\":\"bar\"}"}`
	expect = `{"_time":"1970-01-01T00:00:00Z","_msg":"bar"}`
	f(config, in, expect)

	// Unpack incoming data from previous unpack
	config = `transforms:
  - pipe: unpack_json | unpack_logfmt`
	in = `{"_msg":"{\"_msg\":\"level=info _msg=\\\"user logged in\\\" user_id=123\"}"}`
	expect = `{"_time":"1970-01-01T00:00:00Z","_msg":"user logged in","level":"info","user_id":"123"}`
	f(config, in, expect)

	// Produces multiple rows from one row
	config = `transforms:
  - pipe: unroll by (array)`
	in = `{"_msg":"abc","array":[{"_msg":"foo"},{"_msg":"bar"},{"_msg":"baz"}]}`
	expect = `{"_time":"1970-01-01T00:00:00Z","_msg":"abc","array":"{\"_msg\":\"foo\"}"}
{"_time":"1970-01-01T00:00:00Z","_msg":"abc","array":"{\"_msg\":\"bar\"}"}
{"_time":"1970-01-01T00:00:00Z","_msg":"abc","array":"{\"_msg\":\"baz\"}"}`
	f(config, in, expect)

	// Nested transforms
	config = `transforms:
  - pipe: format 'bar' as foo
    transforms:
      - pipe: format 'pong' as ping
        transforms:
          - pipe: format 'google' as ok
            transforms:
              - pipe: format 'world' as hello
                transforms:
                  - pipe: format 'b' as a
  - pipe: format 'right' as left
    transforms:
      - pipe: format 'cola' as coca
        transforms:
          - pipe: format 'Metrics' as Victoria`
	in = `{"_msg":"some message"}`
	expect = `{"_time":"1970-01-01T00:00:00Z","_msg":"some message","foo":"bar","ping":"pong","ok":"google","hello":"world","a":"b","left":"right","coca":"cola","Victoria":"Metrics"}`
	f(config, in, expect)

	// Nested transform with producing multiple log rows
	config = `transforms:
  - pipe: format 'bar' as foo
    transforms:
      - pipe: format 'bar' as foo
        transforms:
          - pipe: unroll by (array)`
	in = `{"_msg":"some message","array":[{"_msg":"abc"},{"_msg":"def"}]}`
	expect = `{"_time":"1970-01-01T00:00:00Z","_msg":"some message","array":"{\"_msg\":\"abc\"}","foo":"bar"}
{"_time":"1970-01-01T00:00:00Z","_msg":"some message","array":"{\"_msg\":\"def\"}","foo":"bar"}`
	f(config, in, expect)

	// Action: drop unconditionally
	config = `transforms:
  - action: drop`
	in = `{"foo":"bar"}`
	expect = ``
	f(config, in, expect)

	// Action: drop with matching filter
	config = `transforms:
  - filter: level:=error
    action: drop`
	in = `{"level":"error","msg":"oops"}`
	expect = ``
	f(config, in, expect)

	in = `{"level":"info","_msg":"ok"}`
	expect = `{"_time":"1970-01-01T00:00:00Z","level":"info","_msg":"ok"}`
	f(config, in, expect)

	// Action: send skips remaining steps for matching logs
	config = `transforms:
  - filter: level:=error
    pipe: format 'high' as priority
    action: send
  - pipe: format 'low' as priority`
	in = `{"level":"error","_msg":"oops"}`
	expect = `{"_time":"1970-01-01T00:00:00Z","level":"error","_msg":"oops","priority":"high"}`
	f(config, in, expect)

	in = `{"level":"info","_msg":"ok"}`
	expect = `{"_time":"1970-01-01T00:00:00Z","level":"info","_msg":"ok","priority":"low"}`
	f(config, in, expect)

	// Nested transforms with drop - inner drop discards matching logs
	config = `transforms:
  - pipe: format 'yes' as processed
    transforms:
      - filter: env:=staging
        action: drop`
	in = `{"env":"staging","svc":"api"}`
	expect = ``
	f(config, in, expect)

	in = `{"svc":"api","_msg":"foo"}`
	expect = `{"_time":"1970-01-01T00:00:00Z","svc":"api","_msg":"foo","processed":"yes"}`
	f(config, in, expect)

	// Nested transforms with send - inner send breaks the pipeline
	config = `transforms:
  - transforms:
      - filter: level:=error
        pipe: format 'alerts' as __target
        action: send
      - pipe: format 'default' as __target
        action: send
  - pipe: format 'unreachable' as _msg`
	in = `{"level":"error","_msg":"boom"}`
	expect = `{"_time":"1970-01-01T00:00:00Z","level":"error","_msg":"boom","__target":"alerts"}`
	f(config, in, expect)

	in = `{"level":"info","_msg":"ok"}`
	expect = `{"_time":"1970-01-01T00:00:00Z","level":"info","_msg":"ok","__target":"default"}`
	f(config, in, expect)

	// Nested transforms with send - nested transforms run before send
	config = `transforms:
  - filter: level:=error
    pipe: format 'alerts' as __target
    action: send
    transforms:
      - pipe: format 'critical' as severity
  - pipe: format 'unreachable' as _msg`
	in = `{"level":"error","_msg":"boom"}`
	expect = `{"_time":"1970-01-01T00:00:00Z","level":"error","_msg":"boom","__target":"alerts","severity":"critical"}`
	f(config, in, expect)

	// Nested transforms must be applied only to rows matched by parent filter.
	config = `transforms:
  - filter: service:=checkout
    transforms:
      - filter: level:=error
        action: drop`
	in = `{"service":"payments","level":"error","_msg":"other service error"}`
	expect = `{"_time":"1970-01-01T00:00:00Z","service":"payments","level":"error","_msg":"other service error"}`
	f(config, in, expect)

	in = `{"service":"checkout","level":"error","_msg":"checkout error"}`
	expect = ``
	f(config, in, expect)

	in = `{"service":"checkout","level":"info","_msg":"checkout ok"}`
	expect = `{"_time":"1970-01-01T00:00:00Z","service":"checkout","level":"info","_msg":"checkout ok"}`
	f(config, in, expect)
}

func TestUnmarshalTransforms(t *testing.T) {
	f := func(configStr string, expected []transform) {
		t.Helper()

		got, err := unmarshalTransformsConfig([]byte(configStr))
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}

		if !reflect.DeepEqual(got.transforms, expected) {
			t.Fatalf("unexpected transforms config\ngot:\n%+v\nwant:\n%+v", got.transforms, expected)
		}
	}

	s := `transforms:
  # Parse JSON payload embedded in incoming logs
  - filter: '*'
    pipe: unpack_json from payload | delete payload`
	expected := []transform{
		{
			filter: mustParseFilter(t, "*"),
			pipe:   mustParseQuery(t, "unpack_json from payload | delete payload"),
		},
	}
	f(s, expected)

	s = `transforms:
  - filter: '*'
    pipe: unpack_nginx from error_log | delete error_log`
	expected = []transform{
		{
			filter: mustParseFilter(t, "*"),
			pipe:   mustParseQuery(t, "unpack_nginx from error_log | delete error_log"),
		},
	}
	f(s, expected)

	s = `transforms:
  - filter: service:=checkout
    transforms:
      # Mark critical errors for alerts sink, then forward immediately
      - filter: "level:=i('ERROR')"
        pipe: "extract 'order_id=<id>' from _msg | format 'high' as priority | format 'alerts' as __target"
        action: send
      # Drop noisy retries
      - filter: "event:='retry_attempt'"
        action: drop`
	expected = []transform{
		{
			filter: mustParseFilter(t, "service:=checkout"),
			transforms: []transform{
				{
					filter: mustParseFilter(t, "level:i('ERROR')"),
					pipe:   mustParseQuery(t, "extract 'order_id=<id>' from _msg | format 'high' as priority | format 'alerts' as __target"),
					action: actionSend,
				},
				{
					filter: mustParseFilter(t, "event:='retry_attempt'"),
					action: actionDrop,
				},
			},
		},
	}
	f(s, expected)

	s = `transforms:
  - filter: "kubernetes.pod_namespace:=staging"
    action: drop`
	expected = []transform{
		{
			filter: mustParseFilter(t, "kubernetes.pod_namespace:=staging"),
			action: actionDrop,
		},
	}
	f(s, expected)
}

func TestUnmarshalTransformsFailure(t *testing.T) {
	f := func(configStr string) {
		t.Helper()

		_, err := unmarshalTransformsConfig([]byte(configStr))
		if err == nil {
			t.Fatalf("expecting non-empty error")
		}
	}

	// Empty file
	f(``)

	// Invalid YAML
	f(`foo: bar`)

	// Unknown fields
	f(`transforms:
  - filter: error
    action: send
    foo: bar`)
}

func mustParseFilter(t *testing.T, filterStr string) *logstorage.Filter {
	t.Helper()
	f, err := logstorage.ParseFilter(filterStr)
	if err != nil {
		t.Fatalf("cannot parse filter %q: %s", filterStr, err)
	}
	return f
}

func mustParseQuery(t *testing.T, queryStr string) *logstorage.Query {
	t.Helper()
	ql, err := logstorage.ParseQuery(queryStr)
	if err != nil {
		t.Fatalf("cannot parse query %q: %s", queryStr, err)
	}
	return ql
}
