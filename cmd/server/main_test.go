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

// TestEntityWithoutADSNIsRefused is the guard rail for the property the whole
// operator split rests on.
//
// store/mem is one process's memory. Four bank processes each calling mem.New
// hold four disconnected universes, and a payment from Aurora to Verde would
// post into an Aurora that Verde has never heard of. Postgres-optional is
// load-bearing (CLAUDE.md), so the split cannot require a database — the
// default is every listener in one process — but -entity, which is the mode
// that genuinely splits processes, cannot work without one.
//
// The message is the teaching, so it is asserted rather than merely the error.
func TestEntityWithoutADSNIsRefused(t *testing.T) {
	err := checkEntityMode("aurora", "")
	if err == nil {
		t.Fatal("-entity with no -database was accepted; it cannot work")
	}
	for _, want := range []string{"in-memory", "-database"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q:\n%s", want, err)
		}
	}

	if err := checkEntityMode("aurora", "postgres://cbs@localhost/cbs"); err != nil {
		t.Errorf("-entity with a DSN was refused: %v", err)
	}
	// The default — every listener in one process — needs no database.
	if err := checkEntityMode("", ""); err != nil {
		t.Errorf("the default was refused: %v", err)
	}
}

// TestSelectEntityNamesABankHowAReaderWould pins that -entity accepts something
// a person can type. Participant ids are generated (bank_1, bank_3, …) and are
// not memorable, so a name works too.
func TestSelectEntityNamesABankHowAReaderWould(t *testing.T) {
	entities := []entity{
		{key: centralBankKey, name: "Central bank", addr: ":8081"},
		{key: clearingHouseKey, name: "Clearing house", addr: ":8082"},
		{key: "bank_1", name: "Aurora Bank", addr: ":8083"},
		{key: "bank_3", name: "Crédit Soleil", addr: ":8084"},
	}

	for _, tc := range []struct{ arg, wantKey string }{
		{"bank_1", "bank_1"},
		{"aurora", "bank_1"},
		{"Aurora Bank", "bank_1"},
		{"credit-soleil", "bank_3"},
		{"central-bank", centralBankKey},
		{"clearing-house", clearingHouseKey},
	} {
		got, err := selectEntity(entities, tc.arg)
		if err != nil {
			t.Errorf("selectEntity(%q): %v", tc.arg, err)
			continue
		}
		if got.key != tc.wantKey {
			t.Errorf("selectEntity(%q) = %s, want %s", tc.arg, got.key, tc.wantKey)
		}
	}

	if _, err := selectEntity(entities, "nobody"); err == nil {
		t.Error("an unknown entity was accepted")
	}
}

// TestResolveEntitiesNarrowsToOne pins the wiring between -entity and the
// listener table.
//
// It is a test rather than a reading because this is the step that shipped
// broken once: the narrowing was written into main, compiled, and did nothing,
// so `-entity aurora` quietly started all six listeners and only a live probe
// caught it.
func TestResolveEntitiesNarrowsToOne(t *testing.T) {
	all := []entity{
		{key: centralBankKey, name: "Central bank", addr: ":8081"},
		{key: clearingHouseKey, name: "Clearing house", addr: ":8082"},
		{key: "bank_1", name: "Aurora Bank", addr: ":8083"},
		{key: "bank_3", name: "Banca Verde", addr: ":8084"},
	}

	got, err := resolveEntities(all, "")
	if err != nil || len(got) != len(all) {
		t.Fatalf("resolveEntities(all, \"\") = %d entities, %v; want all %d", len(got), err, len(all))
	}

	got, err = resolveEntities(all, "aurora")
	if err != nil {
		t.Fatalf("resolveEntities(all, \"aurora\"): %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("-entity aurora started %d listeners, want 1: %+v", len(got), got)
	}
	if got[0].key != "bank_1" {
		t.Errorf("-entity aurora selected %s, want bank_1", got[0].key)
	}
	// The port is the one the whole-system plan gave it, so an entity answers
	// at the same address whichever mode started it.
	if got[0].addr != ":8083" {
		t.Errorf("-entity aurora bound %s, want :8083", got[0].addr)
	}

	if _, err := resolveEntities(all, "nobody"); err == nil {
		t.Error("an unknown entity was accepted")
	}
}
