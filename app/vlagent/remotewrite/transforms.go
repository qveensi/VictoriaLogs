package remotewrite

import (
	"flag"
	"fmt"
	"strings"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/envtemplate"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/flagutil"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/fs/fscore"
	"gopkg.in/yaml.v2"

	"github.com/VictoriaMetrics/VictoriaLogs/app/vlinsert/insertutil"
	"github.com/VictoriaMetrics/VictoriaLogs/lib/logstorage"
)

var (
	transformsConfigPathGlobal = flag.String("remoteWrite.transformsConfig", "", "Optional path to transforms config, which are applied "+
		"to all the logs before sending them to -remoteWrite.url. See also -remoteWrite.urlTransformsConfig. "+
		"The path can point either to local file or to http url. "+
		"See https://docs.victoriametrics.com/victorialogs/vlagent/#transforming")

	transformConfigPaths = flagutil.NewArrayString("remoteWrite.urlTransformsConfig", "Optional path to transforms config for the corresponding -remoteWrite.url. "+
		"See also -remoteWrite.transformsConfig. The path can point either to local file or to http url. "+
		"See https://docs.victoriametrics.com/victorialogs/vlagent/#transforming")
)

type transformConfigs struct {
	global transformConfig
	perURL []transformConfig
}

func loadTransformConfigs() (*transformConfigs, error) {
	configs := &transformConfigs{}

	if *transformsConfigPathGlobal != "" {
		cfg, err := loadTransformConfig(*transformsConfigPathGlobal)
		if err != nil {
			return nil, err
		}
		configs.global = *cfg
	}

	for _, configPath := range *transformConfigPaths {
		if configPath == "" {
			continue
		}
		cfg, err := loadTransformConfig(configPath)
		if err != nil {
			return nil, err
		}
		configs.perURL = append(configs.perURL, *cfg)
	}
	return configs, nil
}

type transformConfig struct {
	transforms []transform
}

type transform struct {
	filter     *logstorage.Filter `yaml:"filter"`
	pipe       *logstorage.Query  `yaml:"pipe"`
	action     action             `yaml:"action"`
	transforms []transform        `yaml:"transforms"`
}

func (tr *transform) String() string {
	return fmt.Sprintf("filter=%q, pipe=%q, action=%d, transforms=%v", tr.filter, tr.pipe, tr.action, tr.transforms)
}

type action byte

const (
	actionContinue action = iota
	actionDrop
	actionSend
)

// evaluateTransforms evaluates given transforms and writes the result to dst.
// The function takes ownership of src, so src should not be used after the function returns.
func evaluateTransforms(trs []transform, dst, src *logstorage.LogRows) {
	src = evaluateTransformsInternal(trs, dst, src)
	src.ForEachRow(func(_ uint64, r *logstorage.InsertRow) {
		dst.MustAddInsertRow(r)
	})
	putLogRows(src)
}

func evaluateTransformsInternal(trs []transform, result, src *logstorage.LogRows) *logstorage.LogRows {
	for _, tr := range trs {
		switch tr.action {
		case actionDrop:
			// Drop matched log rows.
			unmatched := getLogRows()
			src.ForEachRow(func(_ uint64, r *logstorage.InsertRow) {
				if !tr.filter.MatchRow(r.Fields) {
					unmatched.MustAddInsertRow(r)
				}
			})
			putLogRows(src)
			src = unmatched
		case actionSend:
			// Split by matched and unmatched.
			matched := getLogRows()
			unmatched := getLogRows()
			src.ForEachRow(func(_ uint64, r *logstorage.InsertRow) {
				if tr.filter.MatchRow(r.Fields) {
					matched.MustAddInsertRow(r)
				} else {
					unmatched.MustAddInsertRow(r)
				}
			})
			// Unmatched continue through remaining transforms.
			putLogRows(src)
			src = unmatched

			// Process matched log rows: evaluate tr and write the result directly.
			if tr.pipe != nil {
				piped := getLogRows()
				logstorage.ApplyQueryToRows(tr.pipe, piped, matched)
				putLogRows(matched)
				matched = piped
			}
			matched = evaluateTransformsInternal(tr.transforms, result, matched)
			matched.ForEachRow(func(_ uint64, r *logstorage.InsertRow) {
				result.MustAddInsertRow(r)
			})
			putLogRows(matched)
		case actionContinue:
			// Process only matched log rows.
			matched := getLogRows()
			src.ForEachRow(func(_ uint64, r *logstorage.InsertRow) {
				if tr.filter.MatchRow(r.Fields) {
					matched.MustAddInsertRow(r)
				}
			})

			if tr.pipe != nil {
				piped := getLogRows()
				logstorage.ApplyQueryToRows(tr.pipe, piped, matched)
				putLogRows(matched)
				matched = piped
			}
			matched = evaluateTransformsInternal(tr.transforms, result, matched)

			// Process unmatched log rows.
			src.ForEachRow(func(_ uint64, r *logstorage.InsertRow) {
				if !tr.filter.MatchRow(r.Fields) {
					matched.MustAddInsertRow(r)
				}
			})

			putLogRows(src)
			src = matched
		default:
			panic(fmt.Errorf("BUG: unknown transform action %d", tr.action))
		}
	}
	return src
}

func loadTransformConfig(configPath string) (*transformConfig, error) {
	data, err := fscore.ReadFileOrHTTP(configPath)
	if err != nil {
		return nil, fmt.Errorf("cannot read transforms config from %q: %w", configPath, err)
	}
	data = envtemplate.ReplaceBytes(data)

	cfg, err := unmarshalTransformsConfig(data)
	if err != nil {
		return nil, fmt.Errorf("cannot unmarshal transforms config from %q: %w", configPath, err)
	}
	return cfg, nil
}

type rawTransformConfig struct {
	Filter     string               `yaml:"filter"`
	Pipe       string               `yaml:"pipe"`
	Action     string               `yaml:"action"`
	Transforms []rawTransformConfig `yaml:"transforms"`
}

func unmarshalTransformsConfig(data []byte) (*transformConfig, error) {
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	// Set strict mode to fail on unknown fields.
	dec.SetStrict(true)

	root := struct {
		Transforms []rawTransformConfig `yaml:"transforms"`
	}{}
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}

	cfg := &transformConfig{}
	if err := cfg.initFromRawConfig(root.Transforms); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (t *transformConfig) initFromRawConfig(raws []rawTransformConfig) error {
	t.transforms = make([]transform, 0, len(raws))
	for i, raw := range raws {
		var tr transform
		if err := tr.initFromRawConfig(raw); err != nil {
			return fmt.Errorf("cannot initialize transform with index %d: %w", i, err)
		}
		t.transforms = append(t.transforms, tr)
	}
	return nil
}

func (tr *transform) initFromRawConfig(raw rawTransformConfig) error {
	if err := tr.initFilter(raw.Filter); err != nil {
		return err
	}
	if err := tr.unmarshalPipe(raw.Pipe); err != nil {
		return err
	}
	if err := tr.initAction(raw.Action); err != nil {
		return err
	}
	if err := tr.initNestedTransforms(raw.Transforms); err != nil {
		return err
	}
	if err := tr.validate(); err != nil {
		return err
	}
	return nil
}

func (tr *transform) validate() error {
	if tr.action == actionDrop {
		if tr.pipe != nil {
			return fmt.Errorf("'pipe' cannot be used for drop action")
		}
		if len(tr.transforms) > 0 {
			return fmt.Errorf("nested transforms cannot be used with drop action")
		}
	}

	for i, tr := range tr.transforms {
		if err := tr.validate(); err != nil {
			return fmt.Errorf("invalid transforms by index %d: %w", i, err)
		}
	}
	return nil
}

func (tr *transform) initFilter(s string) error {
	if s == "" {
		s = "*"
	}

	filter, err := logstorage.ParseFilter(s)
	if err != nil {
		return fmt.Errorf("cannot parse LogsQL filter: %w", err)
	}
	tr.filter = filter
	return nil
}

func (tr *transform) unmarshalPipe(s string) error {
	if s == "" {
		return nil
	}
	q, err := logstorage.ParsePipes(s)
	if err != nil {
		return err
	}
	if !q.CanLiveTail() {
		return fmt.Errorf("cannot use pipe %q in transformations because it accumulates state", q)
	}
	tr.pipe = q
	return nil
}

func (tr *transform) initAction(s string) error {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "continue":
		tr.action = actionContinue
	case "drop":
		tr.action = actionDrop
	case "send":
		tr.action = actionSend
	default:
		return fmt.Errorf("unknown action: %q", s)
	}
	return nil
}

func (tr *transform) initNestedTransforms(cfgs []rawTransformConfig) error {
	tr.transforms = make([]transform, 0, len(cfgs))
	for i, cfg := range cfgs {
		var nested transform
		if err := nested.initFromRawConfig(cfg); err != nil {
			return fmt.Errorf("cannot initialize transform with index %d and filter %q: %w", i, tr.filter, err)
		}
		tr.transforms = append(tr.transforms, nested)
	}
	return nil
}

func getLogRows() *logstorage.LogRows {
	return logstorage.GetLogRows(nil, nil, nil, nil, *insertutil.DefaultMsgValue)
}

func putLogRows(rows *logstorage.LogRows) {
	logstorage.PutLogRows(rows)
}
