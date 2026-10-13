package dbtest

import (
	"testing"
)

// TestTargets verifies that Targets and ServerTargets return the expected
// dialect sets and that every target has complete execution configuration.
func TestTargets(t *testing.T) {
	all := Targets()
	if len(all) != 4 {
		t.Fatalf("Targets() returned %d targets, want 4", len(all))
	}
	wantDialects := []string{"postgres", "mysql", "mssql", "sqlite"}
	for i, tg := range all {
		if tg.Dialect != wantDialects[i] {
			t.Errorf("target %d dialect: got %q, want %q", i, tg.Dialect, wantDialects[i])
		}
		if len(tg.Client) == 0 {
			t.Errorf("target %s has empty Client", tg.Dialect)
		}
		if tg.DDL == "" {
			t.Errorf("target %s has empty DDL", tg.Dialect)
		}
		if tg.SelectHex == "" {
			t.Errorf("target %s has empty SelectHex", tg.Dialect)
		}
		if tg.Encode == nil {
			t.Errorf("target %s has nil Encode", tg.Dialect)
		}
	}

	servers := ServerTargets()
	if len(servers) != 3 {
		t.Fatalf("ServerTargets() returned %d targets, want 3", len(servers))
	}
	wantServers := []string{"postgres", "mysql", "mssql"}
	for i, tg := range servers {
		if tg.Dialect != wantServers[i] {
			t.Errorf("server target %d: got %q, want %q", i, tg.Dialect, wantServers[i])
		}
		if tg.Port == 0 {
			t.Errorf("server target %s has zero Port", tg.Dialect)
		}
	}
}

// TestLookup verifies target resolution including the sqlite3 alias and
// unknown dialect rejection.
func TestLookup(t *testing.T) {
	cases := []struct {
		name    string
		dialect string
		wantOK  bool
		wantDia string
	}{
		{"postgres", "postgres", true, "postgres"},
		{"mysql", "mysql", true, "mysql"},
		{"mssql", "mssql", true, "mssql"},
		{"sqlite", "sqlite", true, "sqlite"},
		{"sqlite3 alias", "sqlite3", true, "sqlite"},
		{"unknown", "oracle", false, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tg, ok := Lookup(tc.dialect)
			if ok != tc.wantOK {
				t.Fatalf("Lookup(%q): ok = %v, want %v", tc.dialect, ok, tc.wantOK)
			}
			if ok && tg.Dialect != tc.wantDia {
				t.Errorf("Lookup(%q).Dialect = %q, want %q", tc.dialect, tg.Dialect, tc.wantDia)
			}
		})
	}
}

// TestIsASCIIOnly verifies ASCII boundary detection on 7-bit versus multibyte
// UTF-8 strings.
func TestIsASCIIOnly(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"", true},
		{"simple text 123!", true},
		{"café", false},
		{"日本", false},
		{"🎉", false},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			if got := IsASCIIOnly(tc.input); got != tc.want {
				t.Errorf("IsASCIIOnly(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// TestEncoding verifies the hex representations used by the verification
// helpers against expected raw byte values.
func TestEncoding(t *testing.T) {
	if got, want := UTF8Hex("hello"), "68656c6c6f"; got != want {
		t.Errorf("UTF8Hex: got %q, want %q", got, want)
	}
	if got, want := UTF16LEHex("A"), "4100"; got != want {
		t.Errorf("UTF16LEHex single char: got %q, want %q", got, want)
	}
	// 🎉 is U+1F389, encoded in UTF-16LE as surrogate pair D83C DF89 (3cd8 89df).
	if got, want := UTF16LEHex("🎉"), "3cd889df"; got != want {
		t.Errorf("UTF16LEHex surrogate pair: got %q, want %q", got, want)
	}
}

// TestSQLiteTargetProperties checks that the SQLite target operates purely
// locally with zero network ports and no container initialization.
func TestSQLiteTargetProperties(t *testing.T) {
	tg, ok := Lookup("sqlite")
	if !ok {
		t.Fatal("sqlite target not found")
	}

	if port := tg.MappedPort(t); port != 0 {
		t.Errorf("sqlite MappedPort = %d, want 0", port)
	}
	if host := tg.Host(t); host != "" {
		t.Errorf("sqlite Host = %q, want empty", host)
	}
	if dsn := tg.DSN(t); dsn != SQLiteDBPath {
		t.Errorf("sqlite DSN = %q, want %q", dsn, SQLiteDBPath)
	}
	if c := tg.Container(t); c != nil {
		t.Errorf("sqlite Container = %v, want nil", c)
	}
}
