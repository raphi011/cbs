package main

import "strings"

import "testing"

// TestRedactNeverEchoesAPassword is the one thing worth pinning here: a DSN
// reaches this process from the environment, and the log line that records
// which database was opened is the easiest place to leak the credential in it.
func TestRedactNeverEchoesAPassword(t *testing.T) {
	const secret = "hunter2"

	cases := []struct {
		name string
		dsn  string
		want string
	}{
		{
			name: "userinfo password",
			dsn:  "postgres://cbs:" + secret + "@localhost:5432/cbs?sslmode=disable",
			want: "postgres://cbs:xxxxx@localhost:5432/cbs?sslmode=disable",
		},
		{
			name: "password query parameter",
			dsn:  "postgres://cbs@localhost:5432/cbs?password=" + secret + "&sslmode=disable",
			want: "postgres://cbs@localhost:5432/cbs?password=xxxxx&sslmode=disable",
		},
		{
			// libpq's keyword form is not a URL. Rather than guess at its
			// shape, redact refuses to echo it at all.
			name: "keyword form is not echoed",
			dsn:  "host=localhost user=cbs password=" + secret + " dbname=cbs",
			want: "<redacted>",
		},
		{
			name: "no password at all is left readable",
			dsn:  "postgres://cbs@localhost:5432/cbs?sslmode=disable",
			want: "postgres://cbs@localhost:5432/cbs?sslmode=disable",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redact(tc.dsn)
			if strings.Contains(got, secret) {
				t.Fatalf("redact leaked the password: %q", got)
			}
			if got != tc.want {
				t.Errorf("redact(%q)\n got %q\nwant %q", tc.dsn, got, tc.want)
			}
		})
	}
}
