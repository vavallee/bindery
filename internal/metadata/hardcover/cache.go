package hardcover

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/vavallee/bindery/internal/metadata"
)

// ResolveCacheProvider freezes the live token for a complete cached fetch.
// Hashing keeps credentials out of cache keys. A token change selects a fresh
// namespace; old entries remain bounded and expire normally.
func (c *Client) ResolveCacheProvider(ctx context.Context) (metadata.Provider, string) {
	token := c.authorizationToken(ctx)
	return c.WithToken(token), fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
}
