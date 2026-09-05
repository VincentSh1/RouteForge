package persistence

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestRequestIDFormatAndUniqueness(t *testing.T) {
	pattern := regexp.MustCompile(`^rfreq_[A-Za-z0-9_-]{22}$`)
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id, err := NewRequestID()
		if err != nil {
			t.Fatalf("NewRequestID() error = %v", err)
		}
		if !pattern.MatchString(id) {
			t.Fatalf("request ID %q has unexpected format", id)
		}
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate request ID %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestOperationalRecordsExposeNoSensitiveContentFields(t *testing.T) {
	var names []string
	for _, value := range []reflect.Type{reflect.TypeOf(RequestRecord{}), reflect.TypeOf(AttemptRecord{})} {
		for i := 0; i < value.NumField(); i++ {
			names = append(names, strings.ToLower(value.Field(i).Name))
		}
	}
	joined := strings.Join(names, " ")
	for _, prohibited := range []string{
		"prompt", "messages", "responsecontent", "requestbody", "responsebody",
		"authorization", "apikey", "userid", "ipaddress", "rawerror", "traceid",
	} {
		if strings.Contains(joined, prohibited) {
			t.Fatalf("persistence records contain prohibited field %q", prohibited)
		}
	}
}
