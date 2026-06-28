package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/lambogreny/jetder-mcp/internal/jetder"
)

// Shared Kubernetes-event parsing used by BOTH deployment-diagnose ("Deploy Doctor")
// and deployment-events. The eventUrl is a finite application/json endpoint (NOT the
// SSE log stream). Its payload is tolerant: a bare array, an object wrapping
// {items:[...]} or {events:[...]}, or null/empty when there are no events.

// eventsJSONMaxBytes bounds the eventUrl read (it is a small JSON document).
const eventsJSONMaxBytes = 64 * 1024

// involvedObject is the Kubernetes object an event refers to (e.g. a Pod). The live
// Jetder eventUrl shape does NOT currently include it, but we parse it tolerantly: it
// may be an object {kind,name,namespace} OR a bare string like "Pod/web-abc". Either
// way it must not break the whole array parse.
type involvedObject struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// UnmarshalJSON accepts either an object or a plain string for involvedObject. A
// string "Kind/Name" (or just "Name") is split into Kind/Name; a non-JSON/odd value
// is ignored rather than failing the surrounding event.
func (o *involvedObject) UnmarshalJSON(b []byte) error {
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '"' { // a JSON string
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return nil // tolerate
		}
		if i := strings.Index(s, "/"); i >= 0 {
			o.Kind, o.Name = s[:i], s[i+1:]
		} else {
			o.Name = s
		}
		return nil
	}
	// Otherwise treat as an object; use an alias to avoid recursion.
	type alias involvedObject
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return nil // tolerate an unexpected shape
	}
	*o = involvedObject(a)
	return nil
}

// eventItem is a tolerant view of a Kubernetes event from the eventUrl JSON. The
// `clean` field holds the sanitized event text and is the ONLY text downstream code
// should read — it is scrubbed of secrets (incl. the eventUrl/JWT) at fetch time.
// All string fields that reach output are sanitized in fetchEvents.
//
// LIVE-VERIFIED shape (jetder log-server, 2026-06-28): a JSON ARRAY of
// {lastSeen, type, reason, message}. count / involvedObject are NOT present today;
// they are still parsed tolerantly (lastTimestamp/lastSeen both accepted) so a richer
// future payload Just Works.
type eventItem struct {
	Type    string `json:"type"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Note    string `json:"note"`
	Count   int    `json:"count"`
	// Accept the live "lastSeen" plus "lastTimestamp" and the newer k8s "eventTime".
	LastSeen       string         `json:"lastSeen"`
	LastTimestamp  string         `json:"lastTimestamp"`
	EventTime      string         `json:"eventTime"`
	InvolvedObject involvedObject `json:"involvedObject"`
	clean          string         // sanitized Reason+Message, set by fetchEvents
}

// when returns the event timestamp, preferring lastSeen (the live field), then
// lastTimestamp, then the newer eventTime. A zero-value time ("0001-01-01T00:00:00Z")
// is treated as absent.
func (e eventItem) when() string {
	for _, t := range []string{e.LastSeen, e.LastTimestamp, e.EventTime} {
		t = strings.TrimSpace(t)
		if t != "" && !strings.HasPrefix(t, "0001-01-01") {
			return t
		}
	}
	return ""
}

func (e eventItem) rawText() string {
	msg := e.Message
	if msg == "" {
		msg = e.Note
	}
	return strings.TrimSpace(e.Reason + " " + msg)
}

// text returns the SANITIZED event text (set at fetch time). Never the raw text.
func (e eventItem) text() string { return e.clean }

// errEventsUnparseable signals a populated-but-unrecognized eventUrl payload — a
// standalone caller should surface this rather than pretend there are no events.
var errEventsUnparseable = errors.New("event data was not in a recognized format")

// fetchEventsResult reads the eventUrl (finite JSON), tolerantly parses it, sanitizes
// every event field up front (with eventURL), and returns the events plus whether the
// read was truncated. Unlike fetchEvents it DISTINGUISHES errors from "no events":
//   - empty eventURL  → ("", false, nil) is NOT handled here; the caller checks first.
//   - FetchJSON error → returned (already URL-scrubbed by FetchJSON).
//   - null / empty body → ([], false, nil): genuinely no events.
//   - populated but unrecognized → errEventsUnparseable.
//   - truncated read → events parsed best-effort, truncated=true.
func fetchEventsResult(ctx context.Context, adapter *jetder.Adapter, eventURL string, maxBytes int64) ([]eventItem, bool, error) {
	b, truncated, err := adapter.FetchJSON(ctx, eventURL, maxBytes)
	if err != nil {
		return nil, false, err
	}
	trimmed := strings.TrimSpace(string(b))
	if trimmed == "" || trimmed == "null" {
		return nil, truncated, nil // genuinely no events
	}
	body := []byte(trimmed)

	var events []eventItem
	if body[0] == '[' {
		// A JSON array — the live shape.
		if err := json.Unmarshal(body, &events); err != nil {
			if truncated { // cut mid-array → report truncation, not a hard error
				return nil, true, nil
			}
			return nil, false, errEventsUnparseable
		}
	} else {
		// An object: must wrap the events under a recognized key, else it's unknown.
		var wrap struct {
			Items  *[]eventItem `json:"items"`
			Events *[]eventItem `json:"events"`
		}
		if err := json.Unmarshal(body, &wrap); err != nil {
			if truncated {
				return nil, true, nil
			}
			return nil, false, errEventsUnparseable
		}
		switch {
		case wrap.Items != nil:
			events = *wrap.Items
		case wrap.Events != nil:
			events = *wrap.Events
		default:
			// Valid JSON object but no items/events key → unrecognized shape.
			return nil, false, errEventsUnparseable
		}
	}

	sanitizeEvents(adapter, events, eventURL)
	return events, truncated, nil
}

// fetchEvents is the SILENT, best-effort variant used by Deploy Doctor: it returns nil
// on any error/empty (the Doctor degrades gracefully — events are one of several
// signals). Standalone callers must use fetchEventsResult instead.
func fetchEvents(ctx context.Context, adapter *jetder.Adapter, eventURL string) []eventItem {
	if strings.TrimSpace(eventURL) == "" {
		return nil
	}
	events, _, err := fetchEventsResult(ctx, adapter, eventURL, eventsJSONMaxBytes)
	if err != nil {
		return nil
	}
	return events
}

// sanitizeEvents scrubs every event field that can reach output (text + involved
// object + timestamp) with eventURL, so a body echoing the URL/JWT can't leak.
func sanitizeEvents(adapter *jetder.Adapter, events []eventItem, eventURL string) {
	for i := range events {
		events[i].clean = sanitizeLog(adapter, events[i].rawText(), eventURL)
		events[i].InvolvedObject.Kind = sanitizeLog(adapter, events[i].InvolvedObject.Kind, eventURL)
		events[i].InvolvedObject.Name = sanitizeLog(adapter, events[i].InvolvedObject.Name, eventURL)
		events[i].InvolvedObject.Namespace = sanitizeLog(adapter, events[i].InvolvedObject.Namespace, eventURL)
		events[i].LastSeen = sanitizeLog(adapter, events[i].LastSeen, eventURL)
		events[i].LastTimestamp = sanitizeLog(adapter, events[i].LastTimestamp, eventURL)
		events[i].EventTime = sanitizeLog(adapter, events[i].EventTime, eventURL)
	}
}
