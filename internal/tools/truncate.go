// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tools

import (
	"encoding/json"
	"unicode/utf8"
)

// Truncation describes how a tool result was capped. It is surfaced to the
// caller alongside the (truncated) result: MCP handlers append it as an extra
// text content item, the HTTP API returns it as a sibling field of the result.
// Callers are LLM agents, so the notice is what lets a model distinguish a
// capped result from a complete one.
type Truncation struct {
	Truncated bool `json:"truncated"`
	// AppliedLimit names the cap(s) that fired: "maxRows",
	// "maxResponseBytes", a source's own limit such as
	// "maxQueryResultRows", or several joined by commas.
	AppliedLimit string `json:"appliedLimit"`
	// ReturnedRows/TotalRows are set for list-shaped results. TotalRows is
	// the size of the full result where that is known, which for a
	// LimitedRows result is the count its source reported.
	ReturnedRows int `json:"returnedRows,omitempty"`
	TotalRows    int `json:"totalRows,omitempty"`
	// ReturnedBytes/LimitBytes are set when the byte cap fired.
	ReturnedBytes int `json:"returnedBytes,omitempty"`
	LimitBytes    int `json:"limitBytes,omitempty"`
}

// LimitedRows is a list result that its source already cut short under a
// limit of its own, before any result cap ran. A source returns it instead of
// a bare []any so the truncation notice can report the real size of the
// result and name the limit that fired. Without it a source-level limit is
// invisible: the caller gets a short list and no sign that rows are missing.
//
// CapResult unwraps it. Anything that serializes it without going through
// CapResult gets the rows alone, the same output as a bare []any.
type LimitedRows struct {
	Rows []any
	// TotalRows is how many rows the full result has.
	TotalRows int
	// Limit names the source setting that cut the result, as the user
	// writes it in configuration (for example "maxQueryResultRows").
	Limit string
}

// MarshalJSON renders the rows alone.
func (l LimitedRows) MarshalJSON() ([]byte, error) {
	if l.Rows == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(l.Rows)
}

// CapDeclarer is the view of a tool needed to resolve its effective caps.
// Tools satisfy it for free by embedding BaseTool.
type CapDeclarer interface {
	GetMaxRows() *int
	GetMaxResponseBytes() *int
}

// ResolveCap picks the effective cap for one dimension. A tool that declares
// the field wins outright, including when it declares 0 — that is how a tool
// whose results are always small opts out of a server-wide default. A tool
// that omits the field inherits the default.
func ResolveCap(declared *int, serverDefault int) int {
	if declared != nil {
		return *declared
	}
	return serverDefault
}

// CapResultForTool applies the caps in effect for a tool: the tool's own
// declared caps where it set them, otherwise the server-wide defaults the
// caller passes in. Invocation sites call this rather than CapResult directly
// so the precedence rule lives in one place.
func CapResultForTool(t CapDeclarer, result any, defaultRows, defaultBytes int) (any, *Truncation) {
	return CapResult(
		result,
		ResolveCap(t.GetMaxRows(), defaultRows),
		ResolveCap(t.GetMaxResponseBytes(), defaultBytes),
	)
}

// CapResult applies a tool's declared result caps to an invocation result.
// maxRows caps list-shaped results ([]any) by element count; maxBytes caps
// the JSON-serialized size — for list results whole trailing elements are
// dropped (never a syntactically broken element), for other results the
// serialized form is cut at a UTF-8 boundary and returned as a string. A zero
// cap means unlimited. The returned Truncation is nil when nothing was
// dropped, so the uncapped path stays allocation-free.
//
// A LimitedRows result is always unwrapped to its rows and always carries a
// notice, caps or no caps, because its source already dropped rows. The
// smaller limit wins: a maxRows below the source's limit cuts further and is
// the one named, otherwise the source's limit is.
func CapResult(result any, maxRows, maxBytes int) (any, *Truncation) {
	if limited, ok := result.(LimitedRows); ok {
		return capListResult(limited.Rows, limited.TotalRows, limited.Limit, maxRows, maxBytes)
	}
	if maxRows <= 0 && maxBytes <= 0 {
		return result, nil
	}

	if rows, ok := result.([]any); ok {
		return capListResult(rows, len(rows), "", maxRows, maxBytes)
	}
	if maxBytes <= 0 {
		return result, nil
	}
	serialized, err := json.Marshal(result)
	if err != nil || len(serialized) <= maxBytes {
		// Marshal failures surface downstream where the result is rendered.
		return result, nil
	}
	kept := truncateUTF8(serialized, maxBytes)
	// Cutting a serialized document mid-way leaves a fragment that is not
	// valid JSON, so it cannot be handed back as raw JSON — it is carried as
	// a string value inside a valid object instead. The key names it for the
	// calling model, which sees a partial document rather than a malformed one.
	return map[string]string{"truncatedResult": string(kept)}, &Truncation{
		Truncated:     true,
		AppliedLimit:  "maxResponseBytes",
		ReturnedBytes: len(kept),
		LimitBytes:    maxBytes,
	}
}

// capListResult caps rows. totalRows is the size of the full result, and
// sourceLimit names a limit the source already applied to get from totalRows
// down to rows ("" when the source applied none).
func capListResult(rows []any, totalRows int, sourceLimit string, maxRows, maxBytes int) (any, *Truncation) {
	applied := ""
	if sourceLimit != "" && totalRows > len(rows) {
		applied = sourceLimit
	}

	if maxRows > 0 && len(rows) > maxRows {
		rows = rows[:maxRows]
		// maxRows is the tighter limit, so it replaces the source's.
		applied = "maxRows"
	}

	var returnedBytes int
	bytesFired := false
	if maxBytes > 0 {
		// Byte budget counts each element's serialized JSON, which is what
		// both response surfaces render (MCP emits one text item per element).
		kept := len(rows)
		size := 0
		for i, row := range rows {
			rowJSON, err := json.Marshal(row)
			if err != nil {
				continue
			}
			if size+len(rowJSON) > maxBytes {
				kept = i
				break
			}
			size += len(rowJSON)
		}
		if kept < len(rows) {
			rows = rows[:kept]
			if applied != "" {
				applied += ",maxResponseBytes"
			} else {
				applied = "maxResponseBytes"
			}
			returnedBytes = size
			bytesFired = true
		}
	}

	if applied == "" {
		return rows, nil
	}
	t := &Truncation{
		Truncated:    true,
		AppliedLimit: applied,
		ReturnedRows: len(rows),
		TotalRows:    totalRows,
	}
	if bytesFired {
		t.ReturnedBytes = returnedBytes
		t.LimitBytes = maxBytes
	}
	return rows, t
}

// truncateUTF8 cuts b to at most limit bytes without splitting a rune.
func truncateUTF8(b []byte, limit int) []byte {
	if len(b) <= limit {
		return b
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(b[cut]) {
		cut--
	}
	return b[:cut]
}
