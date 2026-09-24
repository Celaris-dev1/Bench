// Package stats provides the statistics Bench needs to report results
// honestly across multiple trials: pass@1 / pass@k with the unbiased
// estimator, Wilson and bootstrap confidence intervals, per-task
// variance/flakiness, and paired significance tests (McNemar and paired
// bootstrap) for comparing two agents on the same tasks.
package stats

import (
	"math"
	"math/rand"
	"sort"
)

// PassAtK is the unbiased pass@k estimator (Chen et al., "Evaluating Large
// Language Models Trained on Code"): given n trials of which c passed,
// the probability that at least one of k trials sampled without replacement
// from those n would pass. Equivalent to 1 - C(n-c,k)/C(n,k), computed as a
// product to avoid overflow. Returns 1 if k >= n-c+... i.e. more failures
// can't fill k slots (all k-subsets must include a success).
func PassAtK(n, c, k int) float64 {
	if n <= 0 || k <= 0 {
		return 0
	}
	if k > n {
		k = n
	}
	if n-c < k {
		return 1 // fewer than k failures exist, so every k-subset has a pass
	}
	// prod_{i=0}^{k-1} (n-c-i)/(n-i)
	prod := 1.0
	for i := 0; i < k; i++ {
		prod *= float64(n-c-i) / float64(n-i)
	}
	return 1 - prod
}

// TaskTrials is one task's outcomes across repeated trials.
type TaskTrials struct {
	TaskID string
	N      int // trials
	C      int // passes
}

// PassAtKMean averages PassAtK over tasks, equally weighting each task
// regardless of how many other tasks there are (the standard pass@k
// report for a benchmark).
func PassAtKMean(ts []TaskTrials, k int) float64 {
	if len(ts) == 0 {
		return 0
	}
	sum := 0.0
	for _, t := range ts {
		sum += PassAtK(t.N, t.C, k)
	}
	return sum / float64(len(ts))
}

// Variance is the per-task Bernoulli variance p(1-p) of a task's own pass
// rate across its trials; 0 for an always-pass or always-fail task.
func (t TaskTrials) Variance() float64 {
	if t.N == 0 {
		return 0
	}
	p := float64(t.C) / float64(t.N)
	return p * (1 - p)
}

// Flaky reports whether the task passed on some trials and failed on
// others.
func (t TaskTrials) Flaky() bool { return t.N > 1 && t.C > 0 && t.C < t.N }

// WilsonInterval returns the (1-alpha) Wilson score confidence interval for
// a binomial proportion of k successes out of n trials. It is well-behaved
// at p near 0 or 1 and for small n, unlike the naive normal approximation.
func WilsonInterval(k, n int, confidence float64) (lo, hi float64) {
	if n <= 0 {
		return 0, 0
	}
	z := zFor(confidence)
	p := float64(k) / float64(n)
	nf := float64(n)
	denom := 1 + z*z/nf
	center := p + z*z/(2*nf)
	adj := z * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf))
	lo = clamp01((center - adj) / denom)
	hi = clamp01((center + adj) / denom)
	return lo, hi
}

func zFor(confidence float64) float64 {
	switch {
	case confidence >= 0.995:
		return 2.807
	case confidence >= 0.99:
		return 2.576
	case confidence >= 0.95:
		return 1.95996
	case confidence >= 0.90:
		return 1.6449
	default:
		return 1.95996
	}
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// BootstrapCI returns a percentile bootstrap confidence interval for the
// mean of xs. rng is exposed so callers (and tests) can get reproducible
// results; pass rand.New(rand.NewSource(seed)).
func BootstrapCI(xs []float64, nBoot int, confidence float64, rng *rand.Rand) (lo, hi float64) {
	if len(xs) == 0 || nBoot <= 0 {
		return 0, 0
	}
	n := len(xs)
	means := make([]float64, nBoot)
	for b := 0; b < nBoot; b++ {
		sum := 0.0
		for i := 0; i < n; i++ {
			sum += xs[rng.Intn(n)]
		}
		means[b] = sum / float64(n)
	}
	sort.Float64s(means)
	return percentileInterval(means, confidence)
}

func percentileInterval(sorted []float64, confidence float64) (lo, hi float64) {
	n := len(sorted)
	if n == 0 {
		return 0, 0
	}
	alpha := 1 - confidence
	loIdx := int(alpha / 2 * float64(n))
	hiIdx := int((1-alpha/2)*float64(n)) - 1
	if loIdx < 0 {
		loIdx = 0
	}
	if hiIdx >= n {
		hiIdx = n - 1
	}
	if hiIdx < loIdx {
		hiIdx = loIdx
	}
	return sorted[loIdx], sorted[hiIdx]
}

// McNemar runs McNemar's test (with continuity correction) for paired
// binary outcomes between two agents on the same tasks: b is the count of
// tasks agent A passed and agent B failed, c the reverse (tasks both agents
// agree on are uninformative and excluded, as usual for this test). It
// returns the chi-square statistic (1 degree of freedom) and its p-value.
func McNemar(b, c int) (chi2, pValue float64) {
	if b+c == 0 {
		return 0, 1
	}
	d := math.Abs(float64(b-c)) - 1
	if d < 0 {
		d = 0
	}
	chi2 = d * d / float64(b+c)
	pValue = 1 - chiSquare1DFCDF(chi2)
	return chi2, pValue
}

// chiSquare1DFCDF is the CDF of a chi-square distribution with 1 degree of
// freedom, which has the closed form erf(sqrt(x/2)).
func chiSquare1DFCDF(x float64) float64 {
	if x < 0 {
		return 0
	}
	return math.Erf(math.Sqrt(x / 2))
}

// PairedBootstrapDiff bootstraps the difference in mean(a)-mean(b) for
// paired samples (a[i] and b[i] are the same unit under two conditions,
// e.g. the same task scored by two agents), resampling pairs together so
// the pairing is preserved. It returns the point estimate and a percentile
// CI; Significant reports whether that CI excludes zero.
func PairedBootstrapDiff(a, b []float64, nBoot int, confidence float64, rng *rand.Rand) (estimate, lo, hi float64) {
	n := len(a)
	if n == 0 || n != len(b) {
		return 0, 0, 0
	}
	estimate = mean(a) - mean(b)
	if nBoot <= 0 {
		return estimate, estimate, estimate
	}
	diffs := make([]float64, nBoot)
	for k := 0; k < nBoot; k++ {
		sa, sb := 0.0, 0.0
		for i := 0; i < n; i++ {
			j := rng.Intn(n)
			sa += a[j]
			sb += b[j]
		}
		diffs[k] = sa/float64(n) - sb/float64(n)
	}
	sort.Float64s(diffs)
	lo, hi = percentileInterval(diffs, confidence)
	return estimate, lo, hi
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// Significant reports whether a [lo,hi] confidence interval excludes zero
// -- Bench's rule for when a drift or a paired comparison is flagged as
// significant rather than noise.
func Significant(lo, hi float64) bool { return lo > 0 || hi < 0 }
