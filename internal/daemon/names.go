package daemon

import (
	"context"
	"sync"

	"github.com/busfactor/whatsmeow-cli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

// groupCache holds resolved group subjects for the daemon's lifetime. Fetching
// a subject is a network round-trip, so caching avoids re-fetching on every
// read. It is safe for concurrent use across request goroutines and the event
// goroutine.
//
// A generation counter lets a rename win over a slow, concurrent fetch: a
// GroupInfo rename event (rename) bumps the generation, and a fetch that
// started earlier (setFresh with the generation it snapshotted before the
// network call) is rejected rather than caching a now-stale subject.
type groupCache struct {
	mu  sync.Mutex
	m   map[string]string
	gen uint64
}

func newGroupCache() *groupCache {
	return &groupCache{m: map[string]string{}}
}

func (c *groupCache) get(jid string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	name, ok := c.m[jid]
	return name, ok
}

// generation returns the current invalidation counter; a fetch snapshots it
// before the network call and passes it to setFresh.
func (c *groupCache) generation() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

// setFresh caches a fetched name only if no rename happened since gen was
// snapshotted, so a fetch that raced a rename does not overwrite the new name.
func (c *groupCache) setFresh(jid, name string, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen == gen {
		c.m[jid] = name
	}
}

// rename applies an authoritative subject change from a GroupInfo event: it
// bumps the generation (invalidating any in-flight fetch) and stores the new
// name directly, or drops the entry when the subject was cleared.
func (c *groupCache) rename(jid, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	if name != "" {
		c.m[jid] = name
	} else {
		delete(c.m, jid)
	}
}

// resolved is a memoized contact lookup (name plus whether one was found).
type resolved struct {
	name string
	ok   bool
}

// nameResolver turns JIDs into display names for a single request. Contact
// lookups are local (whatsmeow's contact store) and group lookups go through
// the shared groupCache; both are memoized per request so a batch of rows
// resolves each distinct JID at most once. Every method falls back to the
// caller-provided value, so resolution never fails a response — a bare JID is
// always an acceptable last resort.
type nameResolver struct {
	cli       wa.Client
	connected bool
	groups    *groupCache
	contacts  map[string]resolved
	groupMemo map[string]string // per-request: "" means tried and unresolved
}

func newNameResolver(cli wa.Client, groups *groupCache) *nameResolver {
	return &nameResolver{
		cli:       cli,
		connected: cli.IsConnected(),
		groups:    groups,
		contacts:  map[string]resolved{},
		groupMemo: map[string]string{},
	}
}

// chatName resolves a chat's display name: a group's subject for groups, the
// other party's contact name for 1:1 chats. Returns fallback when nothing
// better is known.
func (r *nameResolver) chatName(ctx context.Context, jidStr string, isGroup bool, fallback string) string {
	if isGroup {
		return r.groupName(ctx, jidStr, fallback)
	}
	return r.contactName(ctx, jidStr, fallback)
}

// contactName resolves a user JID to a contact name, memoizing the lookup for
// the life of this resolver. The JID is normalized to its ADless identity
// because whatsmeow keys the contact store by ToNonAD (a stored sender JID may
// carry a device component). Returns fallback when the JID is unparseable or no
// name is known.
func (r *nameResolver) contactName(ctx context.Context, jidStr, fallback string) string {
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		return fallback
	}
	jid = jid.ToNonAD()
	key := jid.String()
	if res, seen := r.contacts[key]; seen {
		if res.ok {
			return res.name
		}
		return fallback
	}
	name, ok := r.cli.ContactName(ctx, jid)
	r.contacts[key] = resolved{name: name, ok: ok}
	if ok {
		return name
	}
	return fallback
}

// groupName resolves a group JID to its subject, memoizing hits and misses for
// the life of this resolver so an unresolvable group is not re-fetched once per
// row. Returns fallback when the subject is unknown.
func (r *nameResolver) groupName(ctx context.Context, jidStr, fallback string) string {
	if name, seen := r.groupMemo[jidStr]; seen {
		if name != "" {
			return name
		}
		return fallback
	}
	name := r.fetchGroupName(ctx, jidStr)
	r.groupMemo[jidStr] = name
	if name != "" {
		return name
	}
	return fallback
}

// fetchGroupName returns a group subject from the shared cache, or fetches it
// when connected (a cache miss while offline is skipped so a response never
// blocks on the network). Returns "" when the subject cannot be resolved.
func (r *nameResolver) fetchGroupName(ctx context.Context, jidStr string) string {
	if name, ok := r.groups.get(jidStr); ok {
		return name
	}
	if !r.connected {
		return ""
	}
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		return ""
	}
	gen := r.groups.generation()
	name, ok := r.cli.GroupName(ctx, jid)
	if !ok {
		return ""
	}
	r.groups.setFresh(jidStr, name, gen)
	return name
}
