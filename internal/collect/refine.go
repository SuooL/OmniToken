package collect

import (
	"github.com/suool/omnitoken/internal/model"
	"github.com/suool/omnitoken/internal/parser/claudecode"
)

// RefineProvider upgrades ambiguous first-party events ("anthropic") to the
// probed billing channel (F9). Only local collection may call this — the
// probe reflects THIS machine; SSH-pulled events stay "anthropic" (inferred).
func RefineProvider(events []model.Event, probe func() string) {
	if probe == nil {
		return
	}
	p := probe()
	if p == "" {
		return
	}
	for i := range events {
		if events[i].Provider == "anthropic" && events[i].Source == claudecode.Source {
			events[i].Provider = p
		}
	}
}
