// Package feishuobserve projects normalized Lark CLI events into an isolated Collie preview inbox.
package feishuobserve

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	_ "modernc.org/sqlite"
)

var genericID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func typedID(prefix, id string) bool {
	return strings.HasPrefix(id, prefix) && len(id) > len(prefix) && genericID.MatchString(id)
}

var threadKey = regexp.MustCompile(`^[A-Za-z0-9_-]+:[A-Za-z0-9:_-]{1,190}$`)

type Binding struct {
	PreviousThreads []string `json:"previous_threads,omitempty"`
	RootID          string   `json:"root_id"`
	TopicID         string   `json:"topic_id"`
	Thread          string   `json:"thread"`
}
type Group struct {
	Name             string    `json:"name"`
	AllowUnmentioned bool      `json:"allow_unmentioned"`
	Bindings         []Binding `json:"bindings"`
}
type Config struct {
	PMReview  bool             `json:"pm_review,omitempty"`
	BotOpenID string           `json:"bot_open_id"`
	Groups    map[string]Group `json:"groups"`
}

func (c Config) Validate() error {
	if !typedID("ou_", c.BotOpenID) || len(c.Groups) == 0 {
		return errors.New("config requires a bot open ID and at least one allowed group")
	}
	destinations := map[string]bool{}
	for id, g := range c.Groups {
		if !typedID("oc_", id) || strings.TrimSpace(g.Name) == "" || len(g.Name) > 256 {
			return errors.New("each allowed group must have a valid chat ID and display name")
		}
		roots, topics := map[string]bool{}, map[string]bool{}
		for _, b := range g.Bindings {
			if !threadKey.MatchString(b.Thread) || (b.RootID == "" && b.TopicID == "") ||
				(b.RootID != "" && (!typedID("om_", b.RootID) || roots[b.RootID])) ||
				(b.TopicID != "" && (!genericID.MatchString(b.TopicID) || topics[b.TopicID])) || destinations[b.Thread] {
				return errors.New("bindings require unique roots/topics and destination threads")
			}
			if b.RootID != "" {
				roots[b.RootID] = true
			}
			if b.TopicID != "" {
				topics[b.TopicID] = true
			}
			destinations[b.Thread] = true
			if len(b.PreviousThreads) > 8 {
				return errors.New("too many previous thread references")
			}
			seen := map[string]bool{b.Thread: true}
			for _, prev := range b.PreviousThreads {
				if !threadKey.MatchString(prev) || seen[prev] {
					return errors.New("invalid previous thread reference")
				}
				seen[prev] = true
			}
		}
	}
	return nil
}

type Mention struct {
	ID string `json:"id"`
}
type Event struct {
	Type        string    `json:"type"`
	ChatID      string    `json:"chat_id"`
	ChatType    string    `json:"chat_type"`
	SenderID    string    `json:"sender_id"`
	SenderType  string    `json:"sender_type"`
	MessageID   string    `json:"message_id"`
	EventID     string    `json:"event_id"`
	ParentID    string    `json:"parent_id"`
	ReplyTo     string    `json:"reply_to"`
	RootID      string    `json:"root_id"`
	TopicID     string    `json:"thread_id"`
	Created     string    `json:"create_time"`
	MessageType string    `json:"message_type"`
	Content     string    `json:"content"`
	Mentions    []Mention `json:"mentions"`
}
type Staged struct {
	Event
	GroupName string `json:"group_name"`
	Thread    string `json:"collie_thread"`
}

func (c Config) screen(e Event) (Staged, string) {
	if e.Type != "im.message.receive_v1" {
		return Staged{}, "wrong-event-type"
	}
	if e.ChatType != "group" {
		return Staged{}, "not-group"
	}
	g, ok := c.Groups[e.ChatID]
	if !ok {
		return Staged{}, "group-not-allowed"
	}
	if e.SenderType != "user" || !typedID("ou_", e.SenderID) || e.SenderID == c.BotOpenID {
		return Staged{}, "not-human"
	}
	if !typedID("om_", e.MessageID) || (e.RootID != "" && !typedID("om_", e.RootID)) ||
		(e.ParentID != "" && !typedID("om_", e.ParentID)) || (e.ReplyTo != "" && !typedID("om_", e.ReplyTo)) ||
		(e.TopicID != "" && !genericID.MatchString(e.TopicID)) {
		return Staged{}, "invalid-ids"
	}
	if e.ParentID != "" && e.ReplyTo != "" && e.ParentID != e.ReplyTo {
		return Staged{}, "conflicting-parent"
	}
	if e.ParentID == "" {
		e.ParentID = e.ReplyTo
	}
	mentioned := false
	for _, m := range e.Mentions {
		if m.ID == c.BotOpenID {
			mentioned = true
			break
		}
	}
	if !mentioned && !g.AllowUnmentioned {
		return Staged{}, "bot-not-mentioned"
	}
	if e.MessageType != "text" || strings.TrimSpace(e.Content) == "" {
		return Staged{}, "unsupported-message"
	}
	if len(e.Content) > 16000 {
		return Staged{}, "oversized-content"
	}
	return Staged{Event: e, GroupName: g.Name}, "eligible"
}

func (c Config) Project(e Event) (Staged, string) {
	screened, why := c.screen(e)
	if why != "eligible" {
		return Staged{}, why
	}
	e = screened.Event
	g := c.Groups[e.ChatID]
	root := e.RootID
	if root == "" && e.ParentID == "" {
		root = e.MessageID
	}
	if root == "" && e.TopicID == "" {
		return Staged{}, "missing-reply-lineage"
	}
	var target string
	for _, b := range g.Bindings {
		if (root != "" && root == b.RootID) || (e.TopicID != "" && e.TopicID == b.TopicID) {
			// When a binding specifies both IDs, an explicit conflicting ID is not an alias.
			if (e.RootID != "" && b.RootID != "" && e.RootID != b.RootID) ||
				(e.TopicID != "" && b.TopicID != "" && e.TopicID != b.TopicID) ||
				(target != "" && target != b.Thread) {
				return Staged{}, "conflicting-topic"
			}
			target = b.Thread
		}
	}
	if target == "" {
		return Staged{}, "unbound-topic"
	}
	return Staged{Event: e, GroupName: g.Name, Thread: target}, "matched"
}

// Open prepares a persistently deduplicated, local-only inbox. An existing file
// must be private and in a strict (non-group/world-writable) directory.
func Open(path string) (*sql.DB, error) {
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "?#") {
		return nil, errors.New("staging database path must be absolute and not contain ? or #")
	}
	// Require a dedicated filename rather than accidentally adding a table to the hub DB.
	const filename = "feishu-observer.sqlite"
	if filepath.Base(path) != filename {
		return nil, fmt.Errorf("staging database must be named %s", filename)
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil || !dir.IsDir() || dir.Mode().Perm()&0077 != 0 {
		return nil, errors.New("choose a private existing staging directory (mode 0700)")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		if err := file.Close(); err != nil {
			return nil, err
		}
	} else if !os.IsExist(err) {
		return nil, err
	}
	stat, err := os.Lstat(path)
	if err != nil || !stat.Mode().IsRegular() || stat.Mode().Perm()&0077 != 0 {
		return nil, errors.New("staging database must be a private regular file (mode 0600)")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS feishu_observer (
 message_id TEXT PRIMARY KEY, thread TEXT NOT NULL, payload TEXT NOT NULL, staged_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
 ); CREATE TABLE IF NOT EXISTS feishu_pm_review (
 message_id TEXT PRIMARY KEY, envelope TEXT NOT NULL, state TEXT NOT NULL CHECK(state = 'pending-review'),
 queued_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
 )`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func Stage(db *sql.DB, s Staged) (string, error) {
	payload, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	r, err := db.Exec("INSERT INTO feishu_observer(message_id,thread,payload) VALUES (?,?,?) ON CONFLICT(message_id) DO NOTHING",
		s.MessageID, s.Thread, string(payload))
	if err != nil {
		return "", err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "duplicate", nil
	}
	return "staged", nil
}
