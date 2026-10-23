package aetatmeen_test

import (
	"testing"

	"github.com/openspoor/openspoor/internal/connectors/aetatmeen"
)

func TestInfo(t *testing.T) {
	c := aetatmeen.New()
	if c.Info().ID != aetatmeen.ID || c.Profile.Hub == "" || c.Profile.DefaultFix == "" || len(c.Profile.Fixes) == 0 {
		t.Fatalf("%+v", c.Profile)
	}
}
