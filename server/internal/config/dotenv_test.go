package config

import (
	"strings"
	"testing"
)

func TestParseDotenv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    map[string]string
		wantErr string
	}{
		{
			name:  "plain assignments",
			input: "A=1\nB=two\n",
			want:  map[string]string{"A": "1", "B": "two"},
		},
		{
			name:  "comments and blank lines are skipped",
			input: "# a comment\n\nA=1\n   # indented comment\nB=2\n",
			want:  map[string]string{"A": "1", "B": "2"},
		},
		{
			name:  "export prefix is stripped",
			input: "export ADMIN_PASSWORD=pw\n",
			want:  map[string]string{"ADMIN_PASSWORD": "pw"},
		},
		{
			name:  "double quotes unescape",
			input: `A="line\nbreak"` + "\n" + `B="say \"hi\""` + "\n",
			want:  map[string]string{"A": "line\nbreak", "B": `say "hi"`},
		},
		{
			name:  "single quotes are literal",
			input: `A='line\nbreak'` + "\n",
			want:  map[string]string{"A": `line\nbreak`},
		},
		{
			name:  "trailing comment stripped from unquoted value",
			input: "A=value # trailing\n",
			want:  map[string]string{"A": "value"},
		},
		{
			name:  "hash inside an unquoted value is kept",
			input: "A=pa#ss\n",
			want:  map[string]string{"A": "pa#ss"},
		},
		{
			name:  "hash inside a quoted value is kept",
			input: `A="pa ## ss"` + "\n",
			want:  map[string]string{"A": "pa ## ss"},
		},
		{
			name:  "value containing equals",
			input: "ADMIN_PASSWORD_HASH=$2y$10$ab=cd\n",
			want:  map[string]string{"ADMIN_PASSWORD_HASH": "$2y$10$ab=cd"},
		},
		{
			name:  "CRLF line endings",
			input: "A=1\r\nB=2\r\n",
			want:  map[string]string{"A": "1", "B": "2"},
		},
		{
			name:  "empty value",
			input: "A=\n",
			want:  map[string]string{"A": ""},
		},
		{
			name:    "line without equals is an error",
			input:   "ADMIN_PASSWORD\n",
			wantErr: "missing '='",
		},
		{
			name:    "empty key is an error",
			input:   "=value\n",
			wantErr: "empty key",
		},
		{
			name:    "unterminated double quote",
			input:   `A="oops` + "\n",
			wantErr: "unterminated double-quoted value",
		},
		{
			name:    "unterminated single quote",
			input:   "A='oops\n",
			wantErr: "unterminated single-quoted value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseDotenv(strings.NewReader(tt.input))
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("parseDotenv() error = nil, want error containing %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseDotenv() error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDotenv() error = %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("parseDotenv() = %v, want %v", got, tt.want)
			}
			for k, want := range tt.want {
				if got[k] != want {
					t.Errorf("parseDotenv()[%q] = %q, want %q", k, got[k], want)
				}
			}
		})
	}
}
