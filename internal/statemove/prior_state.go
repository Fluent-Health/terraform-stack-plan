package statemove

import (
	"encoding/json"

	tfjson "github.com/hashicorp/terraform-json"
)

// PriorStateAddrs parses prior_state from a raw Terraform plan JSON document
// and returns an AddressSet mapping every resource address to its provider name.
// Returns nil when the JSON is unparseable or the plan has no prior_state.
//
// The prior_state is the raw Terraform state before the plan ran — the same
// addresses that apply-time xmove validation will see in the live state. Using
// this instead of ResourceChanges for source address validation prevents
// module-level moved{} blocks from causing a plan/apply split: those blocks
// rename resources inside ResourceChanges (e.g. module.foo → module.foo[0])
// without setting per-resource PreviousAddress, so the plan shows post-rename
// addresses while the live state still holds the originals.
//
// Parsing bypasses tfjson.Plan/State.Validate() (which requires FormatVersion)
// so that this function works on both real plan JSON and test fixtures.
func PriorStateAddrs(planJSON []byte) AddressSet {
	// Use a lightweight extraction struct to avoid tfjson's Validate() calls,
	// which require a valid FormatVersion that test fixtures may not carry.
	var raw struct {
		PriorState *struct {
			Values *tfjson.StateValues `json:"values,omitempty"`
		} `json:"prior_state,omitempty"`
	}
	if err := json.Unmarshal(planJSON, &raw); err != nil || raw.PriorState == nil {
		return nil
	}
	// Synthesize a *tfjson.State so we can reuse stateAddresses.
	synth := new(tfjson.State)
	synth.Values = raw.PriorState.Values
	addrs := stateAddresses(synth)
	if len(addrs) == 0 {
		return nil
	}
	return addrs
}
