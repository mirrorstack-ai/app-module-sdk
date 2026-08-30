package contributions

import (
	"bytes"
	"encoding/json"

	jsonschemavalidator "github.com/santhosh-tekuri/jsonschema/v6"
)

const payloadSchemaURL = "urn:mirrorstack:contribution-payload"

// CompilePayloadValidator turns a manifest payload schema into the validator
// used by both host registration and SDK compatibility tests.
func CompilePayloadValidator(schemaDocument json.RawMessage) (func(json.RawMessage) error, error) {
	document, err := jsonschemavalidator.UnmarshalJSON(bytes.NewReader(schemaDocument))
	if err != nil {
		return nil, err
	}

	compiler := jsonschemavalidator.NewCompiler()
	compiler.DefaultDraft(jsonschemavalidator.Draft2020)
	if err := compiler.AddResource(payloadSchemaURL, document); err != nil {
		return nil, err
	}
	compiled, err := compiler.Compile(payloadSchemaURL)
	if err != nil {
		return nil, err
	}

	return func(payload json.RawMessage) error {
		value, err := jsonschemavalidator.UnmarshalJSON(bytes.NewReader(payload))
		if err != nil {
			return err
		}
		return compiled.Validate(value)
	}, nil
}
