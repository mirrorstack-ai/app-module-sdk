package mirrorstack

import (
	"context"
	"testing"

	"github.com/mirrorstack-ai/app-module-sdk/meter"
)

func TestMeterV2FacadeTypes(t *testing.T) {
	t.Parallel()
	var observation Observation
	var packageObservation meter.Observation = observation
	_ = packageObservation
	var record func(context.Context, string, float64, Observation) error = RecordObservation
	_ = record
	var module interface {
		RecordObservation(context.Context, string, float64, meter.Observation) error
	} = (*Module)(nil)
	_ = module
	var option MetricOption = BySubject
	_ = option
}
