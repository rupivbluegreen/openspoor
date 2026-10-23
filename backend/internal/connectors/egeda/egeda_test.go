package egeda_test

import (
	"testing"

	"github.com/openspoor/openspoor/internal/connectors/egeda"
)

func TestInfo(t *testing.T) {
	c := egeda.New()
	if c.Info().ID != egeda.ID || c.Profile.Hub == "" || c.Profile.DefaultFix == "" || len(c.Profile.Fixes) == 0 {
		t.Fatalf("%+v", c.Profile)
	}
}
