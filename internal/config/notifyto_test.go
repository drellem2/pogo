package config

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// notifyToSections maps each Config field that carries a watcher NotifyTo to
// the TOML section that sets it. TestNotifyToSectionsCoverEveryWatcher pins
// that this list and Config agree, so a watcher added later cannot ship a
// notify_to these tests do not see (mg-152d1).
var notifyToSections = map[string]string{
	"GHTeardown":      "gh_teardown",
	"GHIntake":        "gh_intake",
	"CarrierDrift":    "carrier_drift",
	"ReviewDecl":      "review_decl",
	"AckWatch":        "ack_watch",
	"DeafWatch":       "deaf_watch",
	"AbsentWatch":     "absent_watch",
	"ProgressWatch":   "progress_watch",
	"FirstTurn":       "first_turn",
	"MidSessionWedge": "midsession_wedge",
}

// notifyToFields returns the names of every Config field whose struct has a
// string NotifyTo, found by reflection rather than by a hand-kept list.
func notifyToFields(t *testing.T) []string {
	t.Helper()
	var names []string
	ct := reflect.TypeOf(Config{})
	for i := 0; i < ct.NumField(); i++ {
		f := ct.Field(i)
		if f.Type.Kind() != reflect.Struct {
			continue
		}
		if nt, ok := f.Type.FieldByName("NotifyTo"); ok && nt.Type.Kind() == reflect.String {
			names = append(names, f.Name)
		}
	}
	sort.Strings(names)
	return names
}

func notifyToOf(cfg *Config, field string) string {
	return reflect.ValueOf(cfg).Elem().FieldByName(field).FieldByName("NotifyTo").String()
}

// writeSandboxConfig isolates Load from the developer's real config and state
// and writes body as the user config file.
func writeSandboxConfig(t *testing.T, body string) {
	t.Helper()
	pogoHomeSandbox(t)
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "pogo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNotifyToSectionsCoverEveryWatcher(t *testing.T) {
	fields := notifyToFields(t)
	// Positive control: the reflection walk must find the watchers it exists
	// to find, or every per-site test below passes over an empty list.
	if len(fields) < 10 {
		t.Fatalf("reflection found %d NotifyTo fields (%v); want at least the 10 known watchers", len(fields), fields)
	}
	for _, f := range fields {
		if _, ok := notifyToSections[f]; !ok {
			t.Errorf("Config.%s has a NotifyTo that notifyToSections does not list — decide whether it follows [agents] coordinator and add it", f)
		}
	}
	for f := range notifyToSections {
		if _, ok := reflect.TypeOf(Config{}).FieldByName(f); !ok {
			t.Errorf("notifyToSections names Config.%s, which does not exist", f)
		}
	}
}

// An unset notify_to follows a RENAMED coordinator. "boss" is the discriminating
// fixture: a site whose default is the literal "mayor" (as every one was before
// mg-152d1) reads "mayor" here and fails its subtest.
func TestUnsetNotifyToFollowsRenamedCoordinator(t *testing.T) {
	writeSandboxConfig(t, "[agents]\ncoordinator = \"boss\"\n")
	cfg := Load()
	if cfg.Agents.Coordinator != "boss" {
		t.Fatalf("fixture did not load: coordinator = %q, want boss", cfg.Agents.Coordinator)
	}
	for _, f := range notifyToFields(t) {
		t.Run(f, func(t *testing.T) {
			if got := notifyToOf(cfg, f); got != "boss" {
				t.Errorf("[%s] notify_to = %q, want boss (the configured coordinator)", notifyToSections[f], got)
			}
		})
	}
}

// Unrenamed install: every default is still "mayor", as before.
func TestUnsetNotifyToDefaultsToMayor(t *testing.T) {
	writeSandboxConfig(t, "")
	cfg := Load()
	for _, f := range notifyToFields(t) {
		t.Run(f, func(t *testing.T) {
			if got := notifyToOf(cfg, f); got != DefaultCoordinator {
				t.Errorf("[%s] notify_to = %q, want %q", notifyToSections[f], got, DefaultCoordinator)
			}
		})
	}
}

// An explicit notify_to wins over the coordinator — including an explicit
// "mayor" on a host whose coordinator is renamed.
func TestExplicitNotifyToWinsOverCoordinator(t *testing.T) {
	for _, explicit := range []string{"pm-elsewhere", "mayor"} {
		body := "[agents]\ncoordinator = \"boss\"\n"
		for _, sec := range notifyToSections {
			body += "\n[" + sec + "]\nnotify_to = \"" + explicit + "\"\n"
		}
		writeSandboxConfig(t, body)
		cfg := Load()
		for _, f := range notifyToFields(t) {
			t.Run(explicit+"/"+f, func(t *testing.T) {
				if got := notifyToOf(cfg, f); got != explicit {
					t.Errorf("[%s] notify_to = %q, want explicit %q", notifyToSections[f], got, explicit)
				}
			})
		}
	}
}

// A refused rename keeps the running coordinator's name, and every notify_to
// that followed the configured name follows it back; an explicit one naming
// another box is left alone.
func TestGuardRunningCoordinatorRepointsFollowingNotifyTo(t *testing.T) {
	writeSandboxConfig(t, "[agents]\ncoordinator = \"ringmaster\"\n\n[ack_watch]\nnotify_to = \"pm-elsewhere\"\n")
	if err := RecordRunningCoordinator("mayor", liveProcess(t)); err != nil {
		t.Fatal(err)
	}
	cfg, refusal := GuardRunningCoordinator(Load())
	if refusal == nil {
		t.Fatal("rename of a running coordinator was allowed; the fixture needs a refusal")
	}
	for _, f := range notifyToFields(t) {
		t.Run(f, func(t *testing.T) {
			want := "mayor"
			if f == "AckWatch" {
				want = "pm-elsewhere"
			}
			if got := notifyToOf(cfg, f); got != want {
				t.Errorf("[%s] notify_to = %q, want %q", notifyToSections[f], got, want)
			}
		})
	}
}
