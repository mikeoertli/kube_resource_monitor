// Package model holds the resource types that every other package speaks in.
//
// Kubernetes reports resources as resource.Quantity, which is exact but awkward
// to sum, sort, and compare in a hot render loop. We normalize once at the API
// boundary into fixed integer units (milli-cores and bytes) and work with those
// everywhere else.
package model

import (
	"math"

	"k8s.io/apimachinery/pkg/api/resource"
)

// Amounts is a normalized bundle of resource values.
//
// CPU is in milli-cores (1000 = one core), memory and storage in bytes. Zero
// means "none"; absence is modeled separately by the Usage helpers below,
// because a container with no CPU limit is a very different thing from one with
// a limit of zero.
type Amounts struct {
	CPUMilli     int64 `json:"cpuMilli"`
	MemBytes     int64 `json:"memBytes"`
	StorageBytes int64 `json:"storageBytes,omitempty"`
}

// Add returns the element-wise sum of a and b.
func (a Amounts) Add(b Amounts) Amounts {
	return Amounts{
		CPUMilli:     a.CPUMilli + b.CPUMilli,
		MemBytes:     a.MemBytes + b.MemBytes,
		StorageBytes: a.StorageBytes + b.StorageBytes,
	}
}

// IsZero reports whether every field is zero.
func (a Amounts) IsZero() bool {
	return a.CPUMilli == 0 && a.MemBytes == 0 && a.StorageBytes == 0
}

// CPUFromQuantity converts a CPU quantity to milli-cores, rounding up so that a
// tiny non-zero usage never displays as 0m.
func CPUFromQuantity(q *resource.Quantity) int64 {
	if q == nil {
		return 0
	}
	return q.MilliValue()
}

// BytesFromQuantity converts a memory or storage quantity to bytes.
func BytesFromQuantity(q *resource.Quantity) int64 {
	if q == nil {
		return 0
	}
	return q.Value()
}

// Usage bundles observed consumption with the request/limit/capacity context
// needed to say whether that consumption is healthy.
//
// The Has* flags matter: Kubernetes lets a container omit requests, limits, or
// both, and a percentage against an absent denominator is meaningless rather
// than infinite. Every ratio accessor therefore returns (value, ok).
type Usage struct {
	Used     Amounts `json:"used"`
	Requests Amounts `json:"requests,omitempty"`
	Limits   Amounts `json:"limits,omitempty"`
	// Capacity is the node's allocatable, or a PVC's provisioned size. It is
	// the denominator for nodes and volumes the way Limits is for containers.
	Capacity Amounts `json:"capacity,omitempty"`

	HasCPURequest bool `json:"-"`
	HasMemRequest bool `json:"-"`
	HasCPULimit   bool `json:"-"`
	HasMemLimit   bool `json:"-"`
	// UsedKnown is false when metrics were unavailable for this row, which is
	// different from a genuinely idle workload reporting zero.
	UsedKnown bool `json:"-"`

	// PreferCapacity makes Capacity the primary denominator instead of Limits.
	//
	// Nodes and volumes need this. A node row aggregates its pods, so it ends
	// up carrying the sum of their limits -- but that sum routinely exceeds
	// allocatable on any overcommitted cluster and is not a bound on anything.
	// The number that matters for a machine is how full the machine is.
	PreferCapacity bool `json:"-"`
}

// Add merges b into a, summing every amount and OR-ing the presence flags.
//
// OR rather than AND is deliberate: a Deployment whose pods mostly declare
// limits should still show a limit column, with the caveat that the aggregate
// denominator only covers the containers that declared one. AND would blank the
// column entirely the moment one sidecar omitted a limit, which hides more than
// it clarifies.
func (u Usage) Add(b Usage) Usage {
	return Usage{
		Used:           u.Used.Add(b.Used),
		Requests:       u.Requests.Add(b.Requests),
		Limits:         u.Limits.Add(b.Limits),
		Capacity:       u.Capacity.Add(b.Capacity),
		HasCPURequest:  u.HasCPURequest || b.HasCPURequest,
		HasMemRequest:  u.HasMemRequest || b.HasMemRequest,
		HasCPULimit:    u.HasCPULimit || b.HasCPULimit,
		HasMemLimit:    u.HasMemLimit || b.HasMemLimit,
		UsedKnown:      u.UsedKnown || b.UsedKnown,
		PreferCapacity: u.PreferCapacity || b.PreferCapacity,
	}
}

func ratio(num, den int64) (float64, bool) {
	if den <= 0 {
		return 0, false
	}
	return float64(num) / float64(den), true
}

// CPUOfLimit returns used CPU as a fraction of the CPU limit.
func (u Usage) CPUOfLimit() (float64, bool) {
	if !u.HasCPULimit {
		return 0, false
	}
	return ratio(u.Used.CPUMilli, u.Limits.CPUMilli)
}

// MemOfLimit returns used memory as a fraction of the memory limit.
func (u Usage) MemOfLimit() (float64, bool) {
	if !u.HasMemLimit {
		return 0, false
	}
	return ratio(u.Used.MemBytes, u.Limits.MemBytes)
}

// CPUOfRequest returns used CPU as a fraction of the CPU request.
func (u Usage) CPUOfRequest() (float64, bool) {
	if !u.HasCPURequest {
		return 0, false
	}
	return ratio(u.Used.CPUMilli, u.Requests.CPUMilli)
}

// MemOfRequest returns used memory as a fraction of the memory request.
func (u Usage) MemOfRequest() (float64, bool) {
	if !u.HasMemRequest {
		return 0, false
	}
	return ratio(u.Used.MemBytes, u.Requests.MemBytes)
}

// CPUOfCapacity returns used CPU as a fraction of node allocatable CPU.
func (u Usage) CPUOfCapacity() (float64, bool) { return ratio(u.Used.CPUMilli, u.Capacity.CPUMilli) }

// MemOfCapacity returns used memory as a fraction of node allocatable memory.
func (u Usage) MemOfCapacity() (float64, bool) { return ratio(u.Used.MemBytes, u.Capacity.MemBytes) }

// StorageOfCapacity returns used storage as a fraction of provisioned capacity.
func (u Usage) StorageOfCapacity() (float64, bool) {
	return ratio(u.Used.StorageBytes, u.Capacity.StorageBytes)
}

// Metric identifies one measurable dimension of a row. Sorting, threshold
// rules, and column selection all key off this rather than duplicating switch
// statements over "cpu" / "mem" strings.
type Metric string

const (
	MetricCPU     Metric = "cpu"
	MetricMemory  Metric = "memory"
	MetricStorage Metric = "storage"
)

// Basis is the denominator a percentage is measured against.
type Basis string

const (
	BasisLimit    Basis = "limit"
	BasisRequest  Basis = "request"
	BasisCapacity Basis = "capacity"
)

// Fraction returns used/denominator for the given metric and basis, and whether
// that denominator exists at all.
func (u Usage) Fraction(m Metric, b Basis) (float64, bool) {
	switch {
	case m == MetricCPU && b == BasisLimit:
		return u.CPUOfLimit()
	case m == MetricCPU && b == BasisRequest:
		return u.CPUOfRequest()
	case m == MetricCPU && b == BasisCapacity:
		return u.CPUOfCapacity()
	case m == MetricMemory && b == BasisLimit:
		return u.MemOfLimit()
	case m == MetricMemory && b == BasisRequest:
		return u.MemOfRequest()
	case m == MetricMemory && b == BasisCapacity:
		return u.MemOfCapacity()
	case m == MetricStorage && b == BasisCapacity:
		return u.StorageOfCapacity()
	}
	return 0, false
}

// BestFraction picks the most meaningful percentage available for a metric,
// preferring the limit, then the request, then capacity.
//
// This is what drives default coloring: a pod at 95% of its limit is about to
// be throttled or OOM-killed, which is more urgent than the same pod sitting at
// 95% of a request it can freely exceed. When no limit is set we still want
// *some* signal rather than a blank cell, hence the fallbacks.
func (u Usage) BestFraction(m Metric) (float64, Basis, bool) {
	order := []Basis{BasisLimit, BasisRequest, BasisCapacity}
	if u.PreferCapacity {
		order = []Basis{BasisCapacity, BasisLimit, BasisRequest}
	}
	for _, b := range order {
		if f, ok := u.Fraction(m, b); ok {
			return f, b, true
		}
	}
	return 0, "", false
}

// Value returns the raw used amount for a metric, in its native unit.
func (u Usage) Value(m Metric) int64 {
	switch m {
	case MetricCPU:
		return u.Used.CPUMilli
	case MetricMemory:
		return u.Used.MemBytes
	case MetricStorage:
		return u.Used.StorageBytes
	}
	return 0
}

// ClampFraction bounds f to [0, 1] for display purposes. Usage above a limit is
// real and worth reporting numerically, but a progress bar cannot draw 140%.
func ClampFraction(f float64) float64 {
	if math.IsNaN(f) || f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}
