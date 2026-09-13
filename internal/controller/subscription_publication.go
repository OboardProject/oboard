package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

// subscriptionPublicationTTL bounds how long a published body is kept. The key
// already contains every input, so the TTL is memory hygiene rather than
// correctness: an entry nobody asks for again should not be held forever.
const subscriptionPublicationTTL = 5 * time.Minute

// subscriptionPublicationCapacity bounds the number of retained bodies. A pull
// that misses simply rebuilds, so the cap trades a rebuild for a memory bound.
const subscriptionPublicationCapacity = 512

// subscriptionPublication is one rendered subscription: the body a client
// receives before optional age encryption, plus the revision and ETag derived
// from it.
type subscriptionPublication struct {
	revision string
	etag     string
	body     string
	builtAt  time.Time
}

// subscriptionPublicationInputs is everything a rendered body depends on that
// is not already covered by the routing revision.
//
// The key is the inputs themselves, not a watermark: a value that changes
// changes the key, so there is no table anyone can forget to invalidate on.
// That matters more here than anywhere else in the panel, because serving a
// stale subscription means handing a client the authorization state of a
// moment that has passed.
type subscriptionPublicationInputs struct {
	RoutingRevision uint64            `json:"routing_revision"`
	UserID          int64             `json:"user_id"`
	UserUpdatedAt   string            `json:"user_updated_at"`
	ProfileID       int64             `json:"profile_id"`
	ProfileRevision string            `json:"profile_revision"`
	Format          string            `json:"format"`
	RequestedFormat string            `json:"requested_format"`
	AutoFormat      bool              `json:"auto_format"`
	UserAgent       string            `json:"user_agent"`
	Query           string            `json:"query"`
	TemplateDigest  string            `json:"template_digest"`
	AlwaysDomain    bool              `json:"always_domain"`
	EffectiveNodes  map[string]bool   `json:"effective_nodes"`
	EffectiveGroups map[string]string `json:"effective_groups"`
	HiddenInbounds  []int64           `json:"hidden_inbounds"`
	NodeNames       map[string]string `json:"node_names"`
	PlanNodeNames   map[string]string `json:"plan_node_names"`
	OrderPositions  map[string]int    `json:"order_positions"`
	OrderPolicy     string            `json:"order_policy"`
	SSHHostKeys     map[int64]string  `json:"ssh_host_keys"`
	DeliveryStates  map[int64][2]bool `json:"delivery_states"`
	Credentials     map[string]string `json:"credentials"`
	AgeRecipient    string            `json:"age_recipient"`
	AgeEncrypted    bool              `json:"age_encrypted"`
}

// subscriptionDeliveryStates captures the per-server delivery confirmation the
// Snell shared-port annotation writes into the rendered servers. It changes on
// Agent confirmations, so it has to be part of the key.
func subscriptionDeliveryStates(servers []model.Server) map[int64][2]bool {
	states := make(map[int64][2]bool, len(servers))
	for _, server := range servers {
		states[server.ID] = [2]bool{server.UsersConfirmed, server.AuthorizationConfirmed}
	}
	return states
}

// subscriptionCredentialFingerprint identifies the rendered user's credential
// material without keeping any of it: the body embeds these secrets, so a
// rotation has to produce a different key.
func subscriptionCredentialFingerprint(sessionSecret string, user model.User) map[string]string {
	sum := sha256.Sum256([]byte("oboard-subscription-credentials-v1\x00" + sessionSecret + "\x00" + user.ProxyUUID + "\x00" + user.ProxyPassword + "\x00" + user.SubscriptionToken))
	return map[string]string{"proxy": hex.EncodeToString(sum[:])}
}

func (i subscriptionPublicationInputs) key() string {
	encoded, err := json.Marshal(i)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// subscriptionPublicationCache keeps rendered bodies keyed by their inputs.
type subscriptionPublicationCache struct {
	mu      sync.Mutex
	entries map[string]subscriptionPublication
}

func newSubscriptionPublicationCache() *subscriptionPublicationCache {
	return &subscriptionPublicationCache{entries: map[string]subscriptionPublication{}}
}

func (c *subscriptionPublicationCache) load(key string, now time.Time) (subscriptionPublication, bool) {
	if c == nil || key == "" {
		return subscriptionPublication{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return subscriptionPublication{}, false
	}
	if now.Sub(entry.builtAt) > subscriptionPublicationTTL {
		delete(c.entries, key)
		return subscriptionPublication{}, false
	}
	return entry, true
}

func (c *subscriptionPublicationCache) store(key string, entry subscriptionPublication, now time.Time) {
	if c == nil || key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= subscriptionPublicationCapacity {
		for existing, value := range c.entries {
			if now.Sub(value.builtAt) > subscriptionPublicationTTL {
				delete(c.entries, existing)
			}
		}
	}
	// Still full of live entries: drop one so a busy panel cannot grow without
	// bound. Which one does not matter; a miss only costs a rebuild.
	if len(c.entries) >= subscriptionPublicationCapacity {
		for existing := range c.entries {
			delete(c.entries, existing)
			break
		}
	}
	entry.builtAt = now
	c.entries[key] = entry
}

// publishSubscription renders the body for these inputs, or reuses the one
// already published for them. The returned revision and ETag are derived from
// the body, so a reused publication answers a conditional request without
// rebuilding anything.
//
// Empty inputs mean "do not publish": the caller has a credential whose payload
// is spent by this request.
func (s *Server) publishSubscription(inputs subscriptionPublicationInputs, nodes []core.SubscriptionNode, format model.SubscriptionFormat, opts core.SubscriptionRenderOptions, profileID int64, ageRecipient any) (string, string, string, error) {
	key := ""
	if inputs.UserID > 0 {
		key = inputs.key()
	}
	now := time.Now()
	if entry, ok := s.subscriptionPublications.load(key, now); ok {
		s.subscriptionPublicationHits.Add(1)
		return entry.body, entry.revision, entry.etag, nil
	}
	body, err := core.RenderSubscriptionNodesWithOptions(nodes, format, opts)
	if err != nil {
		return "", "", "", err
	}
	digest := sha256.Sum256([]byte("oboard-subscription-v3\x00" + strconv.FormatInt(profileID, 10) + "\x00" + string(format) + "\x00" + opts.TemplateDigest + "\x00" + body + "\x00" + fmt.Sprint(ageRecipient)))
	revision := fmt.Sprintf("sub_%x", digest[:16])
	etag := fmt.Sprintf("W/%q", revision)
	s.subscriptionPublicationBuilds.Add(1)
	s.subscriptionPublications.store(key, subscriptionPublication{revision: revision, etag: etag, body: body}, now)
	return body, revision, etag, nil
}

// subscriptionNodeNameOverrides flattens the pointer map the presentation
// queries return so it can take part in the key.
func subscriptionNodeNameOverrides(overrides map[string]*string) map[string]string {
	out := make(map[string]string, len(overrides))
	for key, value := range overrides {
		if value != nil {
			out[key] = *value
		}
	}
	return out
}

func subscriptionHiddenInboundList(hidden map[int64]bool) []int64 {
	out := make([]int64, 0, len(hidden))
	for id, on := range hidden {
		if on {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
