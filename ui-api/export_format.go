package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"

	"cloud.google.com/go/bigquery"
)

// nullToString is a tiny shim so handlers can treat NullString like a
// plain string. Nulls become the empty string in exports and UI output —
// we intentionally do NOT try to distinguish "empty title" from "missing
// title" in the export, since the logger doesn't distinguish them either.
func nullToString(v bigquery.NullString) string {
	if !v.Valid {
		return ""
	}
	return v.StringVal
}

// formatExport renders `rows` (already reduced to the caller-requested
// columns) in the requested format. We stick to the stdlib for the two
// formats it supports natively (CSV, JSON, XML) and hand-roll the rest.
// YAML and TOML emission is deliberately minimal — quoting every string
// avoids all the ambiguity around numbers, booleans, dates, and special
// characters.
func formatExport(format string, cols []string, rows []map[string]any) ([]byte, error) {
	switch format {
	case "csv":
		return formatCSV(cols, rows)
	case "json":
		return formatJSON(rows)
	case "ndjson":
		return formatNDJSON(rows)
	case "xml":
		return formatXML(cols, rows)
	case "yaml":
		return formatYAML(cols, rows), nil
	case "toml":
		return formatTOML(cols, rows), nil
	}
	return nil, fmt.Errorf("unknown format %q", format)
}

func formatCSV(cols []string, rows []map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(cols); err != nil {
		return nil, err
	}
	rec := make([]string, len(cols))
	for _, r := range rows {
		for i, c := range cols {
			rec[i] = fmt.Sprintf("%v", r[c])
		}
		if err := w.Write(rec); err != nil {
			return nil, err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func formatJSON(rows []map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rows); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func formatNDJSON(rows []map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// xmlPlay is an internal shim: encoding/xml needs a struct with fixed
// field names, but our columns are dynamic. We render by hand below.
func formatXML(cols []string, rows []map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	buf.WriteString("<plays>\n")
	for _, r := range rows {
		buf.WriteString("  <play>\n")
		for _, c := range cols {
			v := fmt.Sprintf("%v", r[c])
			// encoding/xml's EscapeText is the stable way to escape.
			var esc bytes.Buffer
			if err := xml.EscapeText(&esc, []byte(v)); err != nil {
				return nil, err
			}
			buf.WriteString("    <")
			buf.WriteString(c)
			buf.WriteString(">")
			buf.Write(esc.Bytes())
			buf.WriteString("</")
			buf.WriteString(c)
			buf.WriteString(">\n")
		}
		buf.WriteString("  </play>\n")
	}
	buf.WriteString("</plays>\n")
	return buf.Bytes(), nil
}

// yamlQuote returns a safely double-quoted YAML scalar. We escape the
// seven characters YAML treats specially inside a double-quoted string
// (backslash, double-quote, and the usual control chars). Going through
// json.Marshal would also work, but it escapes non-ASCII with \u which
// is ugly for track titles.
func yamlQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\x%02x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func formatYAML(cols []string, rows []map[string]any) []byte {
	var buf bytes.Buffer
	for _, r := range rows {
		buf.WriteString("- ")
		first := true
		for _, c := range cols {
			if !first {
				buf.WriteString("  ")
			}
			first = false
			buf.WriteString(c)
			buf.WriteString(": ")
			buf.WriteString(yamlQuote(fmt.Sprintf("%v", r[c])))
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes()
}

// tomlQuote uses TOML's basic-string rules, which happen to be the same
// as JSON for the characters we care about.
func tomlQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// formatTOML emits an "array of tables" ([[plays]] ... [[plays]] ...).
// That's the idiomatic way to represent a list of records in TOML; a
// top-level array of inline tables would also be legal but is harder to
// read for long records.
func formatTOML(cols []string, rows []map[string]any) []byte {
	var buf bytes.Buffer
	for _, r := range rows {
		buf.WriteString("[[plays]]\n")
		for _, c := range cols {
			buf.WriteString(c)
			buf.WriteString(" = ")
			buf.WriteString(tomlQuote(fmt.Sprintf("%v", r[c])))
			buf.WriteByte('\n')
		}
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}
