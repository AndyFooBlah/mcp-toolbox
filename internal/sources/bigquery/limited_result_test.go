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

package bigquery

import (
	"math"
	"reflect"
	"testing"

	"github.com/googleapis/mcp-toolbox/internal/tools"
)

func TestLimitedResult(t *testing.T) {
	rows := func(n int) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = i
		}
		return out
	}
	tcs := []struct {
		name      string
		rows      []any
		totalRows uint64
		limit     int
		want      any
	}{
		{
			name:      "read stopped at the limit with more rows behind it",
			rows:      rows(50),
			totalRows: 12481,
			limit:     50,
			want:      tools.LimitedRows{Rows: rows(50), TotalRows: 12481, Limit: "maxQueryResultRows"},
		},
		{
			name:      "result is exactly the limit",
			rows:      rows(50),
			totalRows: 50,
			limit:     50,
			want:      rows(50),
		},
		{
			name:      "result is under the limit",
			rows:      rows(7),
			totalRows: 7,
			limit:     50,
			want:      rows(7),
		},
		{
			name:      "total not reported",
			rows:      rows(50),
			totalRows: 0,
			limit:     50,
			want:      rows(50),
		},
		{
			name:      "limit disabled",
			rows:      rows(80),
			totalRows: 80,
			limit:     0,
			want:      rows(80),
		},
		{
			name:      "total beyond int range is clamped",
			rows:      rows(2),
			totalRows: math.MaxUint64,
			limit:     2,
			want:      tools.LimitedRows{Rows: rows(2), TotalRows: math.MaxInt, Limit: "maxQueryResultRows"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			got := limitedResult(tc.rows, tc.totalRows, tc.limit)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}
