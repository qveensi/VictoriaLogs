package remotewrite

import (
	"testing"

	"github.com/VictoriaMetrics/VictoriaLogs/lib/logstorage"
)

func BenchmarkEvaluateTransforms(b *testing.B) {
	b.Run("set_field", func(b *testing.B) {
		config := `transforms:
  - pipe: format 'bar' as foo`
		in := `{"_msg":"benchmark"}`
		benchmarkEvaluateTransforms(b, config, in)
	})

	b.Run("parse_json", func(b *testing.B) {
		config := `transforms:
  - pipe: unpack_json from payload | delete payload`
		in := `{"payload":"{\"_msg\":\"bar\"}"}`
		benchmarkEvaluateTransforms(b, config, in)
	})
}

func benchmarkEvaluateTransforms(b *testing.B, config string, row string) {
	cfg, err := unmarshalTransformsConfig([]byte(config))
	if err != nil {
		b.Fatalf("unexpected error: %s", err)
	}

	p := logstorage.GetJSONParser()
	defer logstorage.PutJSONParser(p)

	if err := p.ParseLogMessage([]byte(row), nil, ""); err != nil {
		b.Fatalf("cannot parse input row %q: %s", row, err)
	}

	src := getLogRows()
	for range 100 {
		// Emulate a batch of logs.
		src.MustAdd(logstorage.TenantID{}, 0, p.Fields, 0)
	}

	b.ReportAllocs()
	b.SetBytes(int64(len(row)))
	b.RunParallel(func(pb *testing.PB) {
		dst := getLogRows()
		for pb.Next() {
			dst.ResetKeepSettings()
			srcLocal := getLogRows()
			src.ForEachRow(func(_ uint64, r *logstorage.InsertRow) {
				srcLocal.MustAddInsertRow(r)
			})
			evaluateTransforms(cfg.transforms, dst, srcLocal)
		}
	})
}
