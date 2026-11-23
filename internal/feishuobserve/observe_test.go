package feishuobserve

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func exampleConfig() Config {
	return Config{BotOpenID: "ou_fixture_bot", Groups: map[string]Group{
		"oc_fixture_group": {Name: "Fixture Group", Bindings: []Binding{
			{RootID: "om_alpha", TopicID: "ot_alpha", Thread: "project:alpha"},
			{RootID: "om_beta", TopicID: "ot_beta", Thread: "project:beta"},
		}},
		"oc_other_group": {Name: "Second Group", Bindings: []Binding{{RootID: "om_alpha", Thread: "project:other"}}},
	}}
}
func exampleEvent() Event {
	return Event{Type: "im.message.receive_v1", ChatID: "oc_fixture_group", ChatType: "group", SenderType: "user", SenderID: "ou_example",
		MessageID: "om_item", ReplyTo: "om_parent", RootID: "om_alpha", TopicID: "ot_alpha", MessageType: "text", Content: "synthetic question",
		Mentions: []Mention{{ID: "ou_fixture_bot"}}}
}

func TestProjectRoutesAndFilters(t *testing.T) {
	c := exampleConfig()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, reason, thread string
		change               func(*Event)
	}{
		{"nested reply", "matched", "project:alpha", func(e *Event) {}},
		{"same group other topic", "matched", "project:beta", func(e *Event) { e.RootID = "om_beta"; e.TopicID = "ot_beta" }},
		{"cross group isolation", "matched", "project:other", func(e *Event) { e.ChatID = "oc_other_group"; e.TopicID = "" }},
		{"top-level uses own ID", "matched", "project:alpha", func(e *Event) { e.RootID = ""; e.ReplyTo = ""; e.TopicID = ""; e.MessageID = "om_alpha" }},
		{"reply without root uses trusted topic binding", "matched", "project:alpha", func(e *Event) { e.RootID = "" }},
		{"reply parent is not a root binding", "missing-reply-lineage", "", func(e *Event) { e.RootID = ""; e.TopicID = ""; e.ReplyTo = "om_alpha" }},
		{"conflicting root and topic", "conflicting-topic", "", func(e *Event) { e.RootID = "om_beta" }},
		{"conflicting parent aliases", "conflicting-parent", "", func(e *Event) { e.ParentID = "om_different" }},
		{"unknown root no fallback", "unbound-topic", "", func(e *Event) { e.RootID = "om_missing"; e.TopicID = "" }},
		{"unknown group", "group-not-allowed", "", func(e *Event) { e.ChatID = "oc_private" }},
		{"not event", "wrong-event-type", "", func(e *Event) { e.Type = "other" }},
		{"DM not observed", "not-group", "", func(e *Event) { e.ChatType = "p2p" }},
		{"unmentioned text not an actual mention", "bot-not-mentioned", "", func(e *Event) { e.Mentions = nil; e.Content = "@ou_fixture_bot synthetic text" }},
		{"bot not observed", "not-human", "", func(e *Event) { e.SenderType = "bot" }},
		{"unsupported media", "unsupported-message", "", func(e *Event) { e.MessageType = "image" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := exampleEvent()
			tc.change(&e)
			got, reason := c.Project(e)
			if reason != tc.reason || got.Thread != tc.thread {
				t.Fatalf("reason=%q thread=%q", reason, got.Thread)
			}
		})
	}
}

func TestStageKeepsIDsAndDeduplicatesAcrossRestarts(t *testing.T) {
	c := exampleConfig()
	e := exampleEvent()
	e.EventID = "ev_first"
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "feishu-observer.sqlite")
	db, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	record, _ := c.Project(e)
	if st, err := Stage(db, record); st != "staged" || err != nil {
		t.Fatalf("first=%s %v", st, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	e.EventID = "ev_retry"
	e.Content = "different delivery body"
	record, _ = c.Project(e)
	if st, err := Stage(db, record); st != "duplicate" || err != nil {
		t.Fatalf("replay=%s %v", st, err)
	}
	var body string
	if err := db.QueryRow("SELECT payload FROM feishu_observer").Scan(&body); err != nil {
		t.Fatal(err)
	}
	var got Staged
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.EventID != "ev_first" || got.GroupName != "Fixture Group" || got.ParentID != "om_parent" || got.RootID != "om_alpha" || got.TopicID != "ot_alpha" ||
		got.SenderID != "ou_example" || got.MessageID != "om_item" || got.ChatID != "oc_fixture_group" || !strings.Contains(got.Content, "synthetic") {
		t.Fatalf("wrong projection: %#v", got)
	}
}

func TestRejectsWrongDBFilenameWithoutCreatingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.sqlite")
	if db, err := Open(path); err == nil {
		db.Close()
		t.Fatal("opened wrong database")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("created wrong database")
	}
}
