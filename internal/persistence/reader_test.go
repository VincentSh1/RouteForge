package persistence

import (
	"strings"
	"testing"
)

func TestHistoryRequestIDValidation(t *testing.T) {
	id, err := NewRequestID()
	if err != nil || !ValidRequestID(id) {
		t.Fatal("generated ID rejected")
	}
	for _, invalid := range []string{"", "rfreq_short", "user_" + strings.Repeat("A", 22), "rfreq_" + strings.Repeat("A", 21) + "B", id + "=", strings.Repeat("a", 10000)} {
		if ValidRequestID(invalid) {
			t.Fatal("malformed ID accepted")
		}
	}
}
