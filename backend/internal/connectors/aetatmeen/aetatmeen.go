// Package aetatmeen is the connector for Tatmeen, the UAE national pharmaceutical track
// and trace platform (launched by MOHAP with the Department of Health Abu Dhabi and the
// Dubai Health Authority, built on GS1 standards).
//
// TODO(spec): the Tatmeen interface specification is distributed to onboarded parties and
// is not public. This connector speaks the minimal JSON shape documented in package
// hubjson against the Open Spoor mock hub. Base URL, authentication, participant
// identification, message types, field names and aggregation reporting must be mapped from
// the Tatmeen onboarding documentation before production use.
package aetatmeen

import (
	connector "github.com/openspoor/connectors-sdk"
	"github.com/openspoor/openspoor/internal/connectors/hubjson"
)

// ID is the connector id.
const ID = "ae-tatmeen"

// Profile describes the Tatmeen connector.
func Profile() hubjson.Profile {
	return hubjson.Profile{
		Hub: "Tatmeen",
		Info: connector.Info{
			ID: ID, Name: "UAE — Tatmeen track and trace", Country: "AE", Market: "AE",
			Description: "Reports commissioning, aggregation, dispatch, receipt, decommissioning and dispensing to Tatmeen, the UAE national drug tracking platform. Message shape is a documented placeholder until mapped from the onboarding specification.",
			Triggers:    hubjson.Triggers(),
			Spec:        "MOHAP: national drug tracking system Tatmeen (public launch notice); GS1 General Specifications; EPCIS/CBV 2.0. TODO(spec): Tatmeen interface specification (onboarding only)",
			Config: []connector.ConfigField{
				{Key: "baseUrl", Label: "Tatmeen endpoint", Required: true, Default: "local-mock",
					Help: "TODO(spec): Tatmeen API base URL is issued at onboarding. 'local-mock' uses the built-in mock hub."},
				{Key: "participantId", Label: "Participant ID",
					Help: "TODO(spec): how a reporting party is identified (GLN or an onboarding-issued ID)."},
				{Key: "clientId", Label: "Client ID", Help: "TODO(spec): authentication scheme is defined in the onboarding specification."},
				{Key: "clientSecret", Label: "Client secret", Secret: true, Help: "TODO(spec): authentication scheme is defined in the onboarding specification."},
			},
		},
		Fixes:      fixes,
		DefaultFix: "Review the Tatmeen reason, correct the source data (master data, event or serial state) and resubmit.",
	}
}

// Rejection codes are the mock hub's (MOCK- prefix). TODO(spec): map Tatmeen's real error codes.
var fixes = map[string]string{
	"MOCK-BATCH-EXPIRY-MISSING": "Commissioning needs batch and expiry for every pack: add lotNumber and itemExpirationDate (ILMD) to the commissioning event or the serial state, then resubmit.",
	"MOCK-SERIAL-DUPLICATE":     "These serials were already commissioned in Tatmeen. Check for a duplicate commissioning event; if the earlier report was correct, no action is needed.",
	"MOCK-SERIAL-UNKNOWN":       "These serials were never commissioned in Tatmeen. Submit the COMMISSION report first, then resubmit this one.",
	"MOCK-SERIAL-TOO-LONG":      "A serial exceeds 20 characters (GS1 AI 21). Re-serialize from a compliant pool.",
	"MOCK-RECEIVER-GLN-MISSING": "Dispatch needs the receiver's GLN: add a destinationList owning_party to the shipping event (or set the partner GLN), then resubmit.",
	"MOCK-PARENT-MISSING":       "Aggregation needs a parent (SSCC or case SGTIN): capture the packing event with a parentID, then resubmit.",
	"MOCK-REPORT-TYPE-UNKNOWN":  "The hub did not recognise the report type. Check the connector version against the Tatmeen specification.",
}

// New creates the connector.
func New() *hubjson.Connector { return hubjson.New(Profile()) }
