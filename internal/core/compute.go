package core

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/mirrorstack-ai/app-module-sdk/internal/registry"
)

// Res is the resource request for a task's execution class.
//
// Go has no keyword arguments, so a named-field struct is the closest readable
// form to Heavy(cpu=4, ram=8192). One struct serves all three classes, but each
// class honours a DIFFERENT subset — the fields a class does not honour are
// rejected rather than ignored, because silently dropping a resource request is
// how an author ends up paying for compute they did not get.
type Res struct {
	// VCPU is honoured by Heavy only, and must form a legal pair with MemoryMB
	// (see fargateSizes). Standard rejects it — Lambda DERIVES vCPU from memory
	// and does not expose it as a dial. GPU rejects it too: the instance type
	// fixes it.
	VCPU float64

	// MemoryMB is honoured by Standard (Lambda's 128..10240 MB) and Heavy
	// (Fargate's matrix). GPU rejects it — the instance type fixes it.
	//
	// AWS's own Fargate table calls these values MB while metering MiB; the
	// numbers here are the ones the ECS API takes, so the same convention is
	// kept rather than silently converted.
	MemoryMB int

	// Instance is honoured by GPU only, and must name an allowlisted type (see
	// gpuInstances). Standard and Heavy reject it: neither Lambda nor Fargate
	// is instance-shaped, so accepting one would imply a control the platform
	// cannot honour.
	Instance string
}

// Compute is a VALIDATED execution-class descriptor. Its fields are unexported
// so the only way to obtain one is through Standard, Heavy or GPU — which is
// what makes "if you hold a Compute, it is legal" true, and lets WithCompute
// skip re-validating at registration.
type Compute struct {
	class    string
	vcpu     float64
	memoryMB int
	instance string
}

// Execution classes, as projected into the manifest. The platform reads these
// to pick a runner at deploy: standard -> Lambda, heavy -> Fargate,
// gpu -> ECS on EC2.
const (
	classStandard = "standard"
	classHeavy    = "heavy"
	classGPU      = "gpu"
)

// Lambda's configurable memory range. Lambda scales vCPU WITH memory on a fixed
// curve, so memory is the only sizing dial Standard can honestly offer.
const (
	standardMinMemoryMB = 128
	standardMaxMemoryMB = 10240
)

// fargateSize is one row of Fargate's task-size matrix: a legal vCPU value and
// the memory it admits, either as an explicit set (Memory) or as an inclusive
// range walked in Step increments.
type fargateSize struct {
	VCPU   float64
	Min    int
	Max    int
	Step   int
	Memory []int // set instead of Min/Max/Step when the row is not a range
}

// fargateSizes is AWS Fargate's task CPU/memory matrix, held as DATA rather
// than a chain of ifs so it can be read against the AWS table, walked
// exhaustively by tests, and quoted verbatim in panic messages.
//
// The matrix is NOT free-form: 0.25 vCPU admits exactly three memory values,
// and every other row is a range with a per-row step. That is why
// Heavy(Res{VCPU: 0.5, MemoryMB: 512}) has to fail — 512 MB is legal at
// 0.25 vCPU and nowhere else. Rejecting it at declaration rather than at deploy
// is the whole point: a resource request that only fails during a deploy is the
// worst of both worlds.
//
// Region is ap-northeast-1 and the runner is ARM64/Graviton only — Graviton is
// exactly 20% cheaper on both vCPU and memory, and one architecture means one
// price per meter. The matrix itself is identical across architectures.
var fargateSizes = []fargateSize{
	{VCPU: 0.25, Memory: []int{512, 1024, 2048}},
	{VCPU: 0.5, Min: 1024, Max: 4096, Step: 1024},
	{VCPU: 1, Min: 2048, Max: 8192, Step: 1024},
	{VCPU: 2, Min: 4096, Max: 16384, Step: 1024},
	{VCPU: 4, Min: 8192, Max: 30720, Step: 1024},
	{VCPU: 8, Min: 16384, Max: 61440, Step: 4096},
	{VCPU: 16, Min: 32768, Max: 122880, Step: 8192},
}

// gpuInstance is an allowlisted GPU instance's fixed dimensions. They are
// resolved into the descriptor rather than requested, so the manifest carries
// what the app owner will actually be billed for.
type gpuInstance struct {
	VCPU     float64
	MemoryMB int
	GPUs     int
}

// gpuInstances is the platform ALLOWLIST of GPU instance types.
//
// GPU is not a Fargate flag — Fargate does not support GPU at all, so this
// class is a materially different runner (an EC2 capacity provider with an ASG,
// an NVIDIA-driver AMI, and instance-billed rather than task-billed compute).
// An allowlist rather than "any EC2 type" keeps the platform ceiling meaningful
// for third-party modules, where "give me the biggest box" is a cost attack.
//
// This wave deliberately exposes only the ARM64 g5g.xlarge declaration. The
// platform still controls production admission behind its GPU canary.
var gpuInstances = map[string]gpuInstance{
	// The first GPU wave is ARM64-only. Declaration does not itself enable
	// production admission; the platform's canary/allowlist remains authoritative.
	// Reserve 1 GiB of the host's 8 GiB for Bottlerocket and the ECS agent;
	// 7168 MiB is the runner-enforceable allocation exposed to module authors.
	"g5g.xlarge": {VCPU: 4, MemoryMB: 7168, GPUs: 1},
}

// Standard selects AWS Lambda — the default execution class, and the one every
// task registered before WithCompute existed already runs on.
//
// MemoryMB is the ONLY dial: Lambda derives vCPU from memory on a fixed curve,
// so a VCPU request here cannot be honoured and is a panic rather than a silent
// no-op. Zero MemoryMB means "platform default" and is legal, so
// Standard(Res{}) states today's behaviour explicitly.
//
// Panics on programmer error, matching ExposeTable and OnTask: an illegal
// declaration is caught at module init, not at deploy.
//
//	ms.WithCompute(ms.Standard(ms.Res{MemoryMB: 512}))
func Standard(r Res) Compute {
	if r.VCPU != 0 {
		panic(fmt.Sprintf(
			"mirrorstack: Standard(Res{VCPU: %v}) — VCPU is not settable on Standard: Lambda derives vCPU from memory. Set MemoryMB instead, or use Heavy for an explicit vCPU count",
			r.VCPU,
		))
	}
	if r.Instance != "" {
		panic(fmt.Sprintf(
			"mirrorstack: Standard(Res{Instance: %q}) — Instance is GPU-only: Standard runs on Lambda, which has no instance types",
			r.Instance,
		))
	}
	if r.MemoryMB != 0 && (r.MemoryMB < standardMinMemoryMB || r.MemoryMB > standardMaxMemoryMB) {
		panic(fmt.Sprintf(
			"mirrorstack: Standard(Res{MemoryMB: %d}) — MemoryMB must be within [%d, %d] (Lambda's range), or zero for the platform default",
			r.MemoryMB, standardMinMemoryMB, standardMaxMemoryMB,
		))
	}
	return Compute{class: classStandard, memoryMB: r.MemoryMB}
}

// Heavy selects AWS Fargate — background work that does not fit Lambda's
// 15-minute ceiling or modest vCPU allocation (transcode, batch inference,
// bulk indexing).
//
// VCPU and MemoryMB are both REQUIRED and must form a legal pair in
// fargateSizes. There is deliberately no default size: guessing one would spend
// the app owner's money on a number nobody declared or approved.
//
//	ms.WithCompute(ms.Heavy(ms.Res{VCPU: 4, MemoryMB: 8192}))
func Heavy(r Res) Compute {
	if r.Instance != "" {
		panic(fmt.Sprintf(
			"mirrorstack: Heavy(Res{Instance: %q}) — Instance is GPU-only: Fargate is sized by VCPU and MemoryMB, not by instance type",
			r.Instance,
		))
	}
	if r.VCPU == 0 || r.MemoryMB == 0 {
		panic(fmt.Sprintf(
			"mirrorstack: Heavy(Res{VCPU: %v, MemoryMB: %d}) — both VCPU and MemoryMB are required: Heavy has no default size, and the platform will not pick one for you. Legal VCPU values are %s",
			r.VCPU, r.MemoryMB, legalVCPUList(),
		))
	}

	// Compare in Fargate CPU units (1 vCPU = 1024) rather than as floats: it is
	// the unit the ECS API actually takes, and it removes float equality — 0.25
	// and 0.5 match exactly as 256 and 512.
	want := cpuUnits(r.VCPU)
	for _, size := range fargateSizes {
		if want != cpuUnits(size.VCPU) {
			continue
		}
		if !size.admits(r.MemoryMB) {
			panic(fmt.Sprintf(
				"mirrorstack: Heavy(Res{VCPU: %v, MemoryMB: %d}) — %d MB is not a legal Fargate memory value at %v vCPU; legal values are %s",
				r.VCPU, r.MemoryMB, r.MemoryMB, r.VCPU, size.describe(),
			))
		}
		return Compute{class: classHeavy, vcpu: size.VCPU, memoryMB: r.MemoryMB}
	}
	panic(fmt.Sprintf(
		"mirrorstack: Heavy(Res{VCPU: %v}) — %v is not a legal Fargate vCPU value; legal values are %s",
		r.VCPU, r.VCPU, legalVCPUList(),
	))
}

// GPU selects ECS on an allowlisted EC2 GPU instance — NVENC transcode and
// model inference.
//
// The INSTANCE is the request: g5g.xlarge gives the task 4 vCPU / 7168 MiB /
// 1 GPU (with 1 GiB of host memory reserved for the OS and ECS agent), so
// accepting VCPU or MemoryMB alongside it would invite a contradiction the
// platform would have to silently resolve. Pick the instance; its allocatable
// dimensions are resolved into the descriptor for the manifest.
//
//	ms.WithCompute(ms.GPU(ms.Res{Instance: "g5g.xlarge"}))
func GPU(r Res) Compute {
	if r.VCPU != 0 || r.MemoryMB != 0 {
		panic(fmt.Sprintf(
			"mirrorstack: GPU(Res{Instance: %q, VCPU: %v, MemoryMB: %d}) — the instance type determines vCPU and memory, so neither may be set alongside it. Name the instance only",
			r.Instance, r.VCPU, r.MemoryMB,
		))
	}
	inst, ok := gpuInstances[r.Instance]
	if !ok {
		panic(fmt.Sprintf(
			"mirrorstack: GPU(Res{Instance: %q}) — unsupported instance type; this SDK wave supports g5g.xlarge only (production admission remains platform-controlled)",
			r.Instance,
		))
	}
	return Compute{class: classGPU, vcpu: inst.VCPU, memoryMB: inst.MemoryMB, instance: r.Instance}
}

// cpuUnits converts vCPU to Fargate's integer CPU units (1 vCPU = 1024).
func cpuUnits(vcpu float64) int { return int(math.Round(vcpu * 1024)) }

// admits reports whether memoryMB is legal for this row.
func (s fargateSize) admits(memoryMB int) bool {
	if len(s.Memory) > 0 {
		return slices.Contains(s.Memory, memoryMB)
	}
	return memoryMB >= s.Min && memoryMB <= s.Max && (memoryMB-s.Min)%s.Step == 0
}

// describe renders the row's legal memory values for a panic message, so the
// rule an author violated is quoted from the same data that enforced it and the
// two cannot drift.
func (s fargateSize) describe() string {
	if len(s.Memory) > 0 {
		parts := make([]string, len(s.Memory))
		for i, m := range s.Memory {
			parts[i] = fmt.Sprintf("%d", m)
		}
		return strings.Join(parts, ", ") + " MB"
	}
	return fmt.Sprintf("%d..%d MB in steps of %d MB", s.Min, s.Max, s.Step)
}

// legalVCPUList renders the matrix's vCPU column for panic messages.
func legalVCPUList() string {
	parts := make([]string, len(fargateSizes))
	for i, s := range fargateSizes {
		parts[i] = fmt.Sprintf("%v", s.VCPU)
	}
	return strings.Join(parts, ", ")
}

// taskCompute projects the descriptor into its manifest form. Only called for
// tasks that declared WithCompute, so a Standard task's manifest entry stays
// byte-identical to what it was before this field existed.
func (c Compute) taskCompute() *registry.TaskCompute {
	return &registry.TaskCompute{
		Class:    c.class,
		VCPU:     c.vcpu,
		MemoryMB: c.memoryMB,
		Instance: c.instance,
	}
}
