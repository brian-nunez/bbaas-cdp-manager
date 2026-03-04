package recorder

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

func randomID(prefix string) (string, error) {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}

	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(bytes)), nil
}
