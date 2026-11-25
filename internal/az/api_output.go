package az

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/jmespath-community/go-jmespath"
	"github.com/jmespath-community/go-jmespath/pkg/parsing"
)

// Validate the projection before dispatching an HTTP request, including one
// with side effects. Preserve raw JSON number precision for unprojected JSON.
func apiRenderer(expression, format string) (func(io.Writer, json.RawMessage) error, error) {
	switch format {
	case "", "json":
		if expression == "" {
			return nil, nil
		}
	case "tsv", "table", "none":
	default:
		return nil, errors.New("REST output supports json, tsv, table and none")
	}
	var projection jmespath.JMESPath
	var columns []string
	if expression != "" {
		var err error
		projection, err = jmespath.Compile(expression)
		if err != nil {
			return nil, errors.New("invalid JMESPath --query")
		}
		if format == "tsv" || format == "table" {
			node, _ := parsing.NewParser().Parse(expression)
			columns = projectionColumns(node)
		}
	}
	return func(out io.Writer, raw json.RawMessage) error {
		if format == "none" {
			return nil
		}
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		if projection == nil {
			decoder.UseNumber()
		}
		if err := decoder.Decode(&value); err != nil {
			return errors.New("invalid JSON response")
		}
		if projection != nil {
			var err error
			value, err = projection.Search(value)
			if err != nil {
				return errors.New("JMESPath --query could not be evaluated on the response")
			}
		}
		switch format {
		case "table", "tsv":
			return writeAPIRows(out, value, format == "table", columns)
		default:
			return json.NewEncoder(out).Encode(value)
		}
	}, nil
}

// The evaluator returns maps. Recover the declared column order of a terminal
// multiselect hash, including one nested in a projection or on the right of a pipe.
func projectionColumns(node parsing.ASTNode) []string {
	switch node.NodeType {
	case parsing.ASTMultiSelectHash:
		var columns []string
		for _, pair := range node.Children {
			key, ok := pair.Value.(string)
			if !ok {
				return nil
			}
			columns = append(columns, key)
		}
		return columns
	case parsing.ASTProjection, parsing.ASTFilterProjection, parsing.ASTValueProjection:
		return projectionColumns(node.Children[1])
	case parsing.ASTSubexpression, parsing.ASTPipe:
		return projectionColumns(node.Children[len(node.Children)-1])
	}
	return nil
}

func outputCell(value any) string {
	switch v := value.(type) {
	case nil:
		return "None"
	case []any:
		return strconv.Itoa(len(v))
	case map[string]any:
		return ""
	case bool:
		if v {
			return "True"
		}
		return "False"
	case string:
		return v
	default:
		raw, _ := json.Marshal(v)
		return string(raw)
	}
}

func writeAPIRows(out io.Writer, value any, table bool, columns []string) error {
	rows, ok := value.([]any)
	if !ok {
		rows = []any{value}
	}
	if len(rows) == 0 {
		return nil
	}
	var writer *tabwriter.Writer
	if table {
		writer = tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		out = writer
	}
	keys := columns
	if _, ok := rows[0].(map[string]any); table && ok {
		if len(keys) == 0 {
			all := map[string]bool{}
			for _, row := range rows {
				if object, ok := row.(map[string]any); ok {
					for key := range object {
						all[key] = true
					}
				}
			}
			keys = slices.Sorted(maps.Keys(all))
		}
		if _, err := fmt.Fprintln(out, strings.Join(keys, "\t")); err != nil {
			return err
		}
	}
	for _, row := range rows {
		var cells []string
		switch r := row.(type) {
		case map[string]any:
			rowKeys := keys
			if !table && len(rowKeys) == 0 {
				rowKeys = slices.Sorted(maps.Keys(r))
			}
			for _, key := range rowKeys {
				cells = append(cells, outputCell(r[key]))
			}
		case []any:
			for _, item := range r {
				cells = append(cells, outputCell(item))
			}
		case bool:
			cells = []string{strconv.FormatBool(r)}
		default:
			cells = []string{outputCell(r)}
		}
		if _, err := fmt.Fprintln(out, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}
	if writer != nil {
		return writer.Flush()
	}
	return nil
}
