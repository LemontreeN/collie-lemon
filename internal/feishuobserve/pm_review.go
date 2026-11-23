package feishuobserve

import (
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// PMEnvelope is a review request, never a Collie Trigger or an instruction.
// Message is untrusted group data. References are explicit operator-provided
// hints, not authorization to pull history, start work, or contact anyone.
type PMEnvelope struct {
	Purpose         string   `json:"purpose"`
	Message         Event    `json:"untrusted_message"`
	GroupName       string   `json:"group_name"`
	RootKey         string   `json:"root_key,omitempty"`
	CurrentThread   string   `json:"current_thread,omitempty"`
	PreviousThreads []string `json:"previous_threads,omitempty"`
	ContextStatus   string   `json:"context_status"`
}

func (c Config) ProjectPMReview(e Event) (PMEnvelope, string) {
	if !c.PMReview {
		return PMEnvelope{}, "pm-review-disabled"
	}
	sc, why := c.screen(e)
	if why != "eligible" {
		return PMEnvelope{}, why
	}
	e = sc.Event
	root := e.RootID
	if root == "" && e.ParentID == "" {
		root = e.MessageID
	}
	out := PMEnvelope{Purpose: "recommend-continuation-or-new-thread-only", Message: e, GroupName: sc.GroupName, ContextStatus: "unbound-topic"}
	if root != "" {
		out.RootKey = strings.Join([]string{e.ChatID, "root", root}, ":")
	}
	if root == "" && e.TopicID == "" {
		out.ContextStatus = "missing-reply-lineage"
	}
	for _, b := range c.Groups[e.ChatID].Bindings {
		if (root != "" && b.RootID == root) || (e.TopicID != "" && b.TopicID == e.TopicID) {
			if (e.RootID != "" && b.RootID != "" && e.RootID != b.RootID) || (e.TopicID != "" && b.TopicID != "" && e.TopicID != b.TopicID) || (out.CurrentThread != "" && out.CurrentThread != b.Thread) {
				out.ContextStatus = "conflicting-topic"
				out.CurrentThread = ""
				out.PreviousThreads = nil
				return out, "review"
			}
			out.CurrentThread = b.Thread
			out.PreviousThreads = append([]string(nil), b.PreviousThreads...)
			out.ContextStatus = "bound-hint"
		}
	}
	return out, "review"
}

// QueuePMReview writes only to the private observer database. No router,
// agent, worker, connector, model, or outbound communication is available.
func (c Config) QueuePMReview(db *sql.DB, e Event) (string, error) {
	envelope, status := c.ProjectPMReview(e)
	if status != "review" {
		return status, nil
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	result, err := db.Exec("INSERT INTO feishu_pm_review(message_id,envelope,state) VALUES(?,?,'pending-review') ON CONFLICT(message_id) DO NOTHING",
		e.MessageID, string(body))
	if err != nil {
		return "", err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "duplicate", nil
	}
	return "queued-pm-review", nil
}

// FreshPMEvent drops buffered/history messages by original creation time, not
// delivery time. The observer and provider hosts should have synchronized clocks.
func FreshPMEvent(e Event, activated, now time.Time) bool {
	ms, err := strconv.ParseInt(e.Created, 10, 64)
	return err == nil && ms >= activated.UnixMilli() && ms <= now.Add(30*time.Second).UnixMilli()
}
