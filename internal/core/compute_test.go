package core

import (
	"fmt"
	"strings"
	"testing"
)

// panicMessage runs fn and returns the panic value as a string, or "" if fn
// returned normally. The constructors validate by panicking (like ExposeTable),
// so every rejection test goes through here.
func panicMessage(fn func()) (message string) {
	defer func() {
		if value := recover(); value != nil {
			message = fmt.Sprint(value)
		}
	}()
	fn()
	return ""
}

// Every pair the matrix declares legal must be accepted. Generated from
// fargateSizes rather than hand-listed so a future matrix edit is exercised
// automatically instead of silently untested.
func TestHeavy_AcceptsEveryLegalFargatePair(t *testing.T) {
	t.Parallel()

	for _, size := range fargateSizes {
		memories := size.Memory
		if len(memories) == 0 {
			for m := size.Min; m <= size.Max; m += size.Step {
				memories = append(memories, m)
			}
		}
		if len(memories) == 0 {
			t.Fatalf("fargateSizes row %v declares no legal memory value", size.VCPU)
		}
		for _, memoryMB := range memories {
			t.Run(fmt.Sprintf("%vvcpu_%dmb", size.VCPU, memoryMB), func(t *testing.T) {
				t.Parallel()
				got := Heavy(Res{VCPU: size.VCPU, MemoryMB: memoryMB})
				if got.class != classHeavy || got.vcpu != size.VCPU || got.memoryMB != memoryMB {
					t.Errorf("Heavy() = %+v, want class heavy, vcpu %v, memoryMB %d", got, size.VCPU, memoryMB)
				}
				if got.instance != "" {
					t.Errorf("Heavy() resolved instance %q, want empty — Fargate has no instance type", got.instance)
				}
			})
		}
	}
}

func TestHeavy_RejectsIllegalPairs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		res  Res
		// wants are substrings the panic MUST contain: the offending value as
		// declared, plus the rule that rejected it. Asserting both is what
		// stops a future edit from degrading the message to "invalid size".
		wants []string
	}{
		{
			// The canonical rejection: 512 MB is legal at 0.25 vCPU and nowhere
			// else, so this pair is exactly the kind of plausible-looking
			// mistake that must not survive to deploy.
			name:  "canonical 0.5 vCPU with 512MB",
			res:   Res{VCPU: 0.5, MemoryMB: 512},
			wants: []string{"VCPU: 0.5, MemoryMB: 512", "1024..4096 MB in steps of 1024 MB"},
		},
		{
			name:  "below row minimum",
			res:   Res{VCPU: 1, MemoryMB: 1024},
			wants: []string{"VCPU: 1, MemoryMB: 1024", "2048..8192 MB"},
		},
		{
			name:  "above row maximum",
			res:   Res{VCPU: 4, MemoryMB: 31744},
			wants: []string{"VCPU: 4, MemoryMB: 31744", "8192..30720 MB"},
		},
		{
			name:  "off step",
			res:   Res{VCPU: 8, MemoryMB: 18432},
			wants: []string{"VCPU: 8, MemoryMB: 18432", "steps of 4096 MB"},
		},
		{
			// The 0.25 row is an explicit set, not a range — its rejection
			// message must quote the set, not a range.
			name:  "explicit-set row rejects an off-set value",
			res:   Res{VCPU: 0.25, MemoryMB: 4096},
			wants: []string{"VCPU: 0.25, MemoryMB: 4096", "512, 1024, 2048 MB"},
		},
		{
			name:  "unknown vCPU",
			res:   Res{VCPU: 3, MemoryMB: 4096},
			wants: []string{"VCPU: 3", "0.25, 0.5, 1, 2, 4, 8, 16"},
		},
		{
			name:  "missing vCPU",
			res:   Res{MemoryMB: 2048},
			wants: []string{"VCPU: 0, MemoryMB: 2048", "both VCPU and MemoryMB are required"},
		},
		{
			name:  "missing memory",
			res:   Res{VCPU: 1},
			wants: []string{"VCPU: 1, MemoryMB: 0", "both VCPU and MemoryMB are required"},
		},
		{
			name:  "instance is GPU-only",
			res:   Res{VCPU: 1, MemoryMB: 2048, Instance: "g5.xlarge"},
			wants: []string{`"g5.xlarge"`, "Instance is GPU-only"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assertPanicMentions(t, func() { Heavy(test.res) }, "Heavy", test.wants)
		})
	}
}

func TestStandard_AcceptsLambdaSizing(t *testing.T) {
	t.Parallel()

	// Zero MemoryMB means "platform default", so Standard(Res{}) states today's
	// behaviour explicitly rather than being an under-specified request.
	for _, res := range []Res{{}, {MemoryMB: standardMinMemoryMB}, {MemoryMB: 512}, {MemoryMB: standardMaxMemoryMB}} {
		t.Run(fmt.Sprintf("%dmb", res.MemoryMB), func(t *testing.T) {
			t.Parallel()
			if message := panicMessage(func() { Standard(res) }); message != "" {
				t.Fatalf("Standard(%+v) panicked: %s", res, message)
			}
			got := Standard(res)
			if got.class != classStandard || got.memoryMB != res.MemoryMB {
				t.Errorf("Standard(%+v) = %+v, want class standard, memoryMB %d", res, got, res.MemoryMB)
			}
			if got.vcpu != 0 {
				t.Errorf("Standard() resolved vcpu %v, want 0 — Lambda derives vCPU from memory", got.vcpu)
			}
		})
	}
}

func TestStandard_RejectsUnhonourableRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		res   Res
		wants []string
	}{
		{
			// The load-bearing one: Lambda cannot honour a vCPU request, so
			// accepting it would be a silent lie about what the task gets.
			name:  "vCPU is not settable on Lambda",
			res:   Res{VCPU: 4},
			wants: []string{"VCPU: 4", "Lambda derives vCPU from memory"},
		},
		{
			name:  "instance is GPU-only",
			res:   Res{Instance: "g5.xlarge"},
			wants: []string{`"g5.xlarge"`, "Instance is GPU-only"},
		},
		{
			name:  "memory below Lambda minimum",
			res:   Res{MemoryMB: 127},
			wants: []string{"MemoryMB: 127", "[128, 10240]"},
		},
		{
			name:  "memory above Lambda maximum",
			res:   Res{MemoryMB: 10241},
			wants: []string{"MemoryMB: 10241", "[128, 10240]"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assertPanicMentions(t, func() { Standard(test.res) }, "Standard", test.wants)
		})
	}
}

// Every allowlisted instance must be accepted AND resolve its own dimensions —
// the manifest carries what the app owner will be billed for, not what was
// typed.
func TestGPU_AcceptsEveryAllowlistedInstance(t *testing.T) {
	t.Parallel()

	if len(gpuInstances) == 0 {
		t.Fatal("gpuInstances allowlist is empty")
	}
	for instance, inst := range gpuInstances {
		t.Run(instance, func(t *testing.T) {
			t.Parallel()
			got := GPU(Res{Instance: instance})
			if got.class != classGPU || got.instance != instance {
				t.Errorf("GPU(%q) = %+v, want class gpu and instance %q", instance, got, instance)
			}
			if got.vcpu != inst.VCPU || got.memoryMB != inst.MemoryMB {
				t.Errorf("GPU(%q) resolved %v vCPU / %d MB, want %v / %d", instance, got.vcpu, got.memoryMB, inst.VCPU, inst.MemoryMB)
			}
		})
	}
}

func TestGPU_RejectsContradictoryOrUnsupportedRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		res   Res
		wants []string
	}{
		{
			// g5.xlarge IS 4 vCPU / 16 GB. Naming a vCPU alongside it is a
			// contradiction the platform would otherwise silently resolve.
			name:  "vCPU alongside instance",
			res:   Res{Instance: "g5.xlarge", VCPU: 4},
			wants: []string{`"g5.xlarge"`, "VCPU: 4", "instance type determines vCPU and memory"},
		},
		{
			name:  "memory alongside instance",
			res:   Res{Instance: "g5.xlarge", MemoryMB: 16384},
			wants: []string{`"g5.xlarge"`, "MemoryMB: 16384", "instance type determines vCPU and memory"},
		},
		{
			name:  "unsupported instance",
			res:   Res{Instance: "g4dn.99xlarge"},
			wants: []string{`"g4dn.99xlarge"`, "unsupported instance type", "g4dn.xlarge"},
		},
		{
			// Bare metal is deliberately excluded from the allowlist.
			name:  "bare metal is not allowlisted",
			res:   Res{Instance: "g4dn.metal"},
			wants: []string{`"g4dn.metal"`, "unsupported instance type"},
		},
		{
			name:  "empty instance",
			res:   Res{},
			wants: []string{`""`, "unsupported instance type"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assertPanicMentions(t, func() { GPU(test.res) }, "GPU", test.wants)
		})
	}
}

// The supported-instance list in the panic must be deterministic, or the same
// mistake produces a different message on every run.
func TestGPU_PanicListsInstancesDeterministically(t *testing.T) {
	t.Parallel()

	first := panicMessage(func() { GPU(Res{Instance: "nope"}) })
	for range 20 {
		if got := panicMessage(func() { GPU(Res{Instance: "nope"}) }); got != first {
			t.Fatalf("panic message is not deterministic:\n%s\n%s", first, got)
		}
	}
}

func assertPanicMentions(t *testing.T, fn func(), label string, wants []string) {
	t.Helper()
	message := panicMessage(fn)
	if message == "" {
		t.Fatalf("%s() did not panic", label)
	}
	for _, want := range wants {
		if !strings.Contains(message, want) {
			t.Errorf("panic %q does not mention %q", message, want)
		}
	}
}
