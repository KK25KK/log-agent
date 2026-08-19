package ids

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// New returns a collision-resistant identifier with a readable prefix.
func New(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return prefix + "_" + hex.EncodeToString(value[:]), nil
}
