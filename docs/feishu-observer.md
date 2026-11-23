# Feishu group observer (experimental)

The separate `collie-feishu-observe` binary stages explicitly mapped Feishu
chat messages for inspection on the machine running Collie. It has no HTTP
listener, Collie credentials, Router/worker dispatch path, or reply capability.
It does **not** call Collie's existing `/hooks/{source}` endpoint: although that
endpoint accepts a thread and idempotency key, it enters the super-agent inbox.

Build: `go build -o collie-feishu-observe ./cmd/collie-feishu-observe`.
The defaults accept synthetic newline-delimited JSON on stdin, with no network.
From a root that contains this checkout:

```sh
umask 077
dir=$(mktemp -d)
cat > "$dir/config.json" <<'JSON'
{
  "bot_open_id": "ou_fixture_bot",
  "groups": {
    "oc_fixture_group": {
      "name": "Fixture Group",
      "bindings": [
        {"root_id": "om_fixture_root", "topic_id": "ot_fixture_topic", "thread": "project:fixture"}
      ]
    }
  }
}
JSON
printf '%s\n' '{"type":"im.message.receive_v1","chat_id":"oc_fixture_group","chat_type":"group","sender_id":"ou_fixture_human","sender_type":"user","message_id":"om_fixture_message","reply_to":"om_fixture_parent","root_id":"om_fixture_root","thread_id":"ot_fixture_topic","message_type":"text","content":"synthetic request","mentions":[{"id":"ou_fixture_bot"}]}' |
  ./collie-feishu-observe --config "$dir/config.json" --db "$dir/feishu-observer.sqlite"
```

Expected stderr: `staged`; repeat the command with the same message ID
to get `duplicate`. The private staging database table `feishu_observer`
retains the normalized matching text, IDs, sender, configured group name and
explicit target thread. Treat the database as private. Do not commit a real
configuration or database. Unmatched or disallowed events are not persisted;
stderr reports a bare reason such as `group-not-allowed`, `bot-not-mentioned`,
`unbound-topic` or `missing-reply-lineage`, with no message contents.

Bindings are scoped by group and identify a stable topic ID and/or root
message ID, not the immediate parent. A new top-level message can match its own
ID as root; replies with no known root or topic are never sent to a recent or
group-wide thread. If supplied IDs disagree about the binding, the event is
ignored. At least one allowlisted group and a bot open ID are required; only
text from a human mentioning the actual bot open ID is accepted by default.
Group entries can opt into observing unmentioned messages using
`"allow_unmentioned": true`, but actual provider delivery still depends on
permissions and app configuration.

## Optional live transport

Once an operator explicitly approves a **dedicated test bot and group**, add
`--live-profile <test-profile>` to use the already configured Lark CLI profile
on the Linux host. No HTTP route is exposed; the Lark CLI opens the outbound
long connection. Provision the app's `im.message.receive_v1` event and the
needed group-mention scope (`im:message.group_at_msg:readonly` for typical
Feishu group-@ delivery); confirm the actual tenant scope and bot membership.
No credentials belong in observer JSON. Reusing an already consumed bot/profile
can split event delivery with other consumers; do not point a second live
observer at an active production bot. This adapter does not install, configure,
start services, or log in for you. If future integration is approved it must
add a separate authorization/transfer step; staging alone never wakes Collie.

## Optional PM-first intake (preview only)

Set `"pm_review": true` in the observer config to queue **all** eligible
human messages allowed by that group’s listening mode for project-manager review, including messages with an
existing binding. A match no longer sends a message anywhere or takes the
regular staging path. The private local `feishu_pm_review` table contains a
single `pending-review` row per original message ID. `previous_threads` is
an optional list on a binding (maximum 8) for operator-provided conversation
references. The envelope keeps the group ID/name, source topic/root/parent,
sender, and message as explicitly **untrusted** data; it attaches known
current/previous thread references, not cross-group transcript bodies.
Unknown or conflicting references produce a reason instead of a guessed
target. An unmatched message is still queued for review where it passes the
group, human and text checks. By default a group uses mentions mode: only an
actual bot @ passes, including for already mapped threads. Setting
`"allow_unmentioned": true` on one allowlisted group opts just that group into
listen-all mode, where the provider must still deliver unmentioned events.
The live PM intake does not backfill: it accepts only messages whose original
creation time is at or after process activation (use a synchronized host clock).

This first slice does **not** run the live Collie PM/Router or a model.
The existing Router's authenticated capabilities can spawn workers, send
replies and subscribe to a thread; a prompt is not a capability boundary.
The observer has no acceptance, execution or send interface. The next step,
if approved, is an independently configured tool-free reviewer with a strict
recommendation result and an explicit human action gate before any dispatch.
