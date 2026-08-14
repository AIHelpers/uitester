package allure

import (
	"crypto/rand"
	"fmt"
)

// newUUID generates a random UUID v4 using only crypto/rand, avoiding a
// dependency on google/uuid or similar just for report file naming.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])      // crypto/rand.Read on a fixed-size buffer never returns a short read without an error we can usefully act on here
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10

	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
