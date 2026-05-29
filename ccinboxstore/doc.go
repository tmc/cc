// Package ccinboxstore stores and parses per-agent inbox message files under a
// team's inboxes/ directory.
//
// Each agent has a JSON file of [InboxMessage] values. [AppendInbox] and
// [ReadUnread] serialize concurrent access with a sidecar flock so messages are
// never lost or partially read. [ParseMessage] decodes an inbox message's text
// payload into a [StructuredMessage] when it carries a typed control message.
//
//	msg := ccinboxstore.InboxMessage{From: "lead", Text: "ping"}
//	if err := ccinboxstore.AppendInbox(ctx, "review", "worker", msg); err != nil {
//		log.Fatal(err)
//	}
package ccinboxstore
